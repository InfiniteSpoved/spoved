// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretscan

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"

	"forgejo.org/modules/git"
)

// maxRecord is the longest line or record read from git: a path is at most 4096 bytes in any file system we serve,
// so a longer one means the output isn't what we expect.
const maxRecord = 1 << 20

var errRecordTooLong = errors.New("record too long")

// splitWriter calls fn for every sep-terminated record git writes. fn must not keep the slice.
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
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, w.sep)
		if i < 0 {
			break
		}
		if err := w.fn(w.buf[:i]); err != nil {
			w.err = err
			return 0, err
		}
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > maxRecord {
		w.err = errRecordTooLong
		return 0, w.err
	}
	return len(p), nil
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

// currentTips returns the sorted object IDs all refs of the repository point to (branches, tags, pull request refs,
// anything else).
func currentTips(ctx context.Context, dir string) ([]string, error) {
	var tips []string
	w := &splitWriter{sep: '\n', fn: func(line []byte) error {
		if isObjectID(line) {
			tips = append(tips, string(line))
		}
		return nil
	}}
	if err := git.NewCommand(ctx, "for-each-ref", "--format=%(objectname)").Run(&git.RunOpts{Dir: dir, Stdout: w, Stderr: io.Discard, UseContextTimeout: true}); err != nil {
		return nil, err
	}
	slices.Sort(tips)
	return slices.Compact(tips), nil
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
