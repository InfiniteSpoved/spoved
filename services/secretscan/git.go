// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretscan

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"io"
	"slices"
	"strconv"
	"strings"

	"forgejo.org/modules/git"
)

// maxRecord is the most bytes kept of one line or record read from git. A path is at most 4096 bytes in any file
// system we serve and a tree entry of a pushed object can be much longer: the rest is dropped, the leading bytes
// (the object ID first) are kept.
const maxRecord = 16 << 10

// splitWriter calls fn for every sep-terminated record git writes. A record over maxRecord is cut to its leading
// bytes. fn must not keep the slice.
type splitWriter struct {
	sep byte
	buf []byte
	fn  func([]byte) error
	err error
}

func (w *splitWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, w.sep)
		chunk := p
		if i >= 0 {
			chunk = p[:i]
		}
		w.buf = append(w.buf, chunk[:min(len(chunk), max(maxRecord-len(w.buf), 0))]...)
		if i < 0 {
			break
		}
		if err := w.fn(w.buf); err != nil {
			w.err = err
			return 0, err
		}
		w.buf = w.buf[:0]
		p = p[i+1:]
	}
	return n, nil
}

// isObjectID tells whether b is a full SHA-1 or SHA-256 object ID in lower case hex.
func isObjectID(b []byte) bool {
	if len(b) != 40 && len(b) != 64 {
		return false
	}
	for _, c := range b {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

// leadingID splits a line of `rev-list --objects` into the object ID and the name. Only the leading ID is taken
// from the line: a name is text a pusher chose and is used for display only.
func leadingID(line []byte) (id, name []byte, ok bool) {
	id, name, _ = bytes.Cut(line, []byte{' '})
	return id, name, isObjectID(id)
}

// The groups of tips, in the order they are scanned: what the owner pushes comes before what a reader can write
// (refs/pull/* of fork pull requests and AGit pushes), so the pull refs can't use up the budgets first.
const (
	groupDefault = iota // the default branch
	groupBranch         // the other branches
	groupOther          // tags and any other ref
	groupPull           // refs/pull/*
)

// tip is an object ID that refs point to, with the group of the best ref that does.
type tip struct {
	id    string
	group int
}

// readTips returns the objects all refs of the repository point to, sorted by group and then ID. defaultBranch is
// the name of the branch the first group is made of.
func readTips(ctx context.Context, dir, defaultBranch string) ([]tip, error) {
	best := map[string]int{}
	w := &splitWriter{sep: '\n', fn: func(line []byte) error {
		id, name, ok := leadingID(line)
		if !ok {
			return nil
		}
		g := groupOther
		switch ref := string(name); {
		case ref == "refs/heads/"+defaultBranch:
			g = groupDefault
		case strings.HasPrefix(ref, "refs/heads/"):
			g = groupBranch
		case strings.HasPrefix(ref, "refs/pull/"):
			g = groupPull
		}
		if old, seen := best[string(id)]; !seen || g < old {
			best[string(id)] = g
		}
		return nil
	}}
	if err := git.NewCommand(ctx, "for-each-ref", "--format=%(objectname) %(refname)").Run(&git.RunOpts{Dir: dir, Stdout: w, Stderr: io.Discard, UseContextTimeout: true}); err != nil {
		return nil, err
	}
	tips := make([]tip, 0, len(best))
	for id, g := range best {
		tips = append(tips, tip{id, g})
	}
	slices.SortFunc(tips, func(a, b tip) int { return cmp.Or(cmp.Compare(a.group, b.group), cmp.Compare(a.id, b.id)) })
	return tips, nil
}

// tipIDs is the sorted list of IDs of the tips.
func tipIDs(tips []tip) []string {
	ids := make([]string, len(tips))
	for i, t := range tips {
		ids[i] = t.id
	}
	slices.Sort(ids)
	return ids
}

// commitNode is a commit of a range with its parents.
type commitNode struct {
	id      string
	parents []string
}

// listCommits returns the commits of the range, parents before children, at most limit of them (the first ones in
// that order).
func listCommits(ctx context.Context, dir string, input io.Reader, limit int) ([]commitNode, error) {
	var commits []commitNode
	w := &splitWriter{sep: '\n', fn: func(line []byte) error {
		var node commitNode
		for i, f := range strings.Fields(string(line)) {
			if !isObjectID([]byte(f)) {
				return nil
			}
			if i == 0 {
				node.id = f
			} else {
				node.parents = append(node.parents, f)
			}
		}
		if node.id != "" {
			commits = append(commits, node)
		}
		if len(commits) >= limit {
			return errStopped
		}
		return nil
	}}
	err := git.NewCommand(ctx, "rev-list", "--topo-order", "--reverse", "--parents", "--stdin").
		Run(&git.RunOpts{Dir: dir, Stdin: input, Stdout: w, Stderr: io.Discard, UseContextTimeout: true})
	if len(commits) >= limit {
		return commits, nil // git was cut off by the limit
	}
	return commits, err
}

// heads are the commits that no other commit of the list has as a parent: the tips that stand for all of them.
func heads(commits []commitNode) []string {
	isParent := map[string]bool{}
	for _, c := range commits {
		for _, p := range c.parents {
			isParent[p] = true
		}
	}
	var out []string
	for _, c := range commits {
		if !isParent[c.id] {
			out = append(out, c.id)
		}
	}
	slices.Sort(out)
	return out
}

// existingTips keeps the IDs that are objects of the repository: a stored tip can be gone after the garbage
// collection took an unreachable commit, and rev-list refuses a revision it doesn't know.
func existingTips(ctx context.Context, gitRepo *git.Repository, tips []string) ([]string, error) {
	var kept []string
	err := gitRepo.WithCatFileBatchCheck(ctx, func(w io.Writer, r *bufio.Reader) error {
		for _, id := range tips {
			if !isObjectID([]byte(id)) {
				continue
			}
			if _, err := io.WriteString(w, id+"\n"); err != nil {
				return err
			}
			switch _, _, _, err := git.ReadBatchLine(r); {
			case err == nil:
				kept = append(kept, id)
			case !git.IsErrNotExist(err):
				return err
			}
		}
		return nil
	})
	return kept, err
}

// rangeInput is the standard input of rev-list and log: the tips to read and, after `--not`, the tips already
// scanned. Thousands of refs never go on the command line.
func rangeInput(tips, scanned []string) *strings.Reader {
	var b strings.Builder
	for _, id := range tips {
		b.WriteString(id + "\n")
	}
	if len(scanned) > 0 {
		b.WriteString("--not\n")
		for _, id := range scanned {
			b.WriteString(id + "\n")
		}
	}
	return strings.NewReader(b.String())
}

// listObjects runs `rev-list --objects` over the range and calls fn with the ID and the name of each object. The
// blob filter leaves out trees and tags; the commits it still prints are told apart from blobs by their type.
func listObjects(ctx context.Context, dir string, input io.Reader, fn func(id, name []byte) error) error {
	w := &splitWriter{sep: '\n', fn: func(line []byte) error {
		if id, name, ok := leadingID(line); ok {
			return fn(id, name)
		}
		return nil
	}}
	return git.NewCommand(ctx, "rev-list", "--objects", "--filter=object:type=blob", "--stdin").
		Run(&git.RunOpts{Dir: dir, Stdin: input, Stdout: w, Stderr: io.Discard, UseContextTimeout: true})
}

// change is one record of `git log --raw`: a file that a commit set to the blob NewID.
type change struct {
	NewID, Path string
	Commit      string
	AuthorName  string
	AuthorEmail string
	CommitUnix  int64
}

// listChanges runs `git log --raw -z` over the range and calls fn for every file that a commit added or changed
// (the diff of a merge is the one against its first parent). Paths come from the NUL-terminated output, so a path
// with a newline stays one path.
func listChanges(ctx context.Context, dir string, input io.Reader, fn func(change)) error {
	p := &logParser{fn: fn}
	w := &splitWriter{sep: 0, fn: p.token}
	return git.NewCommand(ctx, "log", "--stdin", "--raw", "-z", "--no-renames", "--no-abbrev", "--diff-merges=first-parent", "--format=%H%x00%an%x00%ae%x00%ct").
		Run(&git.RunOpts{Dir: dir, Stdin: input, Stdout: w, Stderr: io.Discard, UseContextTimeout: true})
}

// logParser reads the tokens of the log: four tokens per commit (ID, author name, author email, time), then, for
// each changed file, `:<mode> <mode> <old> <new> <status>` and the path. The fields are positional, so no commit
// text can pass for a record.
type logParser struct {
	fn      func(change)
	header  [4]string
	headerN int // fields of the header read so far
	pending *change
	skip    bool // the record in `pending` is not a file we want
}

func (p *logParser) token(tok []byte) error {
	switch {
	case p.pending != nil:
		c := p.pending
		p.pending = nil
		if !p.skip {
			c.Path = string(tok)
			p.fn(*c)
		}
	case p.headerN > 0 && p.headerN < 4:
		p.header[p.headerN] = string(tok)
		p.headerN++
	default:
		tok = bytes.TrimPrefix(tok, []byte{'\n'})
		if p.headerN == 4 && len(tok) > 0 && tok[0] == ':' {
			f := strings.Fields(string(tok))
			p.pending = &change{}
			p.skip = len(f) < 5 || f[4] != "A" && f[4] != "M" && f[4] != "T" || !isObjectID([]byte(f[3])) || !isObjectID([]byte(p.header[0]))
			if !p.skip {
				unix, _ := strconv.ParseInt(p.header[3], 10, 64)
				*p.pending = change{NewID: f[3], Commit: p.header[0], AuthorName: p.header[1], AuthorEmail: p.header[2], CommitUnix: unix}
			}
			return nil
		}
		p.header[0], p.headerN = string(tok), 1
	}
	return nil
}
