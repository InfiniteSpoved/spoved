// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/dependency"
	"forgejo.org/modules/git"
	"forgejo.org/modules/gitrepo"
	"forgejo.org/modules/log"
	"forgejo.org/modules/process"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
)

var (
	errTreeTooLarge = errors.New("tree too large")

	// excludedSegments are directory names whose content is never scanned (vendored or generated copies)
	excludedSegments = map[string]bool{"vendor": true, "node_modules": true, "testdata": true}

	// auxNames are the files a parser may ask for next to a manifest (see dependency.Finder)
	auxNames = map[string]bool{"package.json": true, "composer.json": true, "pyproject.toml": true, "Directory.Packages.props": true}

	// parseManifest parses one manifest; tests replace it
	parseManifest = func(_ context.Context, f *dependency.Format, data []byte, find dependency.Finder) ([]dependency.Dependency, error) {
		return f.Parse(data, find)
	}
)

type treeEntry struct {
	path string
	id   string
	size int64
}

// walkTree runs `git ls-tree -r -z --long` on the commit and calls fn for every regular file (symlinks, submodules
// and trees are left out), counting entries so a huge tree ends the walk with errTreeTooLarge.
func walkTree(ctx context.Context, gitRepo *git.Repository, sha string, limit int, fn func(treeEntry)) error {
	w := &lsTreeWriter{limit: limit, fn: fn}
	cmd := git.NewCommand(ctx, "ls-tree", "-r", "-z", "--long").AddDynamicArguments(sha)
	err := cmd.Run(&git.RunOpts{Dir: gitRepo.Path, Stdout: w, Stderr: io.Discard, UseContextTimeout: true})
	if errors.Is(w.err, errTreeTooLarge) {
		return errTreeTooLarge
	}
	if err != nil {
		return err
	}
	return w.err
}

// lsTreeWriter parses the NUL-terminated records `<mode> <type> <id> <size>\t<path>` as git writes them; a record
// is never held longer than a path can be.
type lsTreeWriter struct {
	limit int
	n     int
	buf   []byte
	fn    func(treeEntry)
	err   error
}

func (w *lsTreeWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, 0)
		if i < 0 {
			break
		}
		rec := w.buf[:i]
		w.buf = w.buf[i+1:]
		if w.n++; w.n > w.limit {
			w.err = errTreeTooLarge
			return 0, w.err
		}
		w.record(rec)
	}
	if len(w.buf) > 64<<10 {
		w.err = errTreeTooLarge // a path longer than any filesystem allows
		return 0, w.err
	}
	return len(p), nil
}

func (w *lsTreeWriter) record(rec []byte) {
	meta, name, ok := bytes.Cut(rec, []byte{'\t'})
	f := strings.Fields(string(meta))
	if !ok || len(f) != 4 || f[0] == "120000" || f[1] != "blob" {
		return // a symlink, a submodule or something odd
	}
	size, err := strconv.ParseInt(f[3], 10, 64)
	if err != nil {
		return
	}
	w.fn(treeEntry{path: string(name), id: f[2], size: size})
}

func hasExcludedSegment(p string) bool {
	for seg := range strings.SplitSeq(p, "/") {
		if excludedSegments[seg] {
			return true
		}
	}
	return false
}

// Scan reads the manifests of the repository's default branch, at the head as it is when the scan starts, and
// replaces the stored dependencies. A scan that fails keeps the old dependencies and records an error code.
func Scan(ctx context.Context, repoID int64) error {
	if !setting.DependencyGraph.Enabled {
		return nil
	}
	repo, err := repo_model.GetRepositoryByID(ctx, repoID)
	if repo_model.IsErrRepoNotExist(err) {
		return nil // deleted or trashed since it was queued
	}
	if err != nil {
		return err
	}
	if repo.IsEmpty || repo.Status != repo_model.RepositoryReady || repo.DefaultBranch == "" {
		return nil
	}
	old, err := repo_model.GetRepoDependencyScan(ctx, repo.ID)
	if err != nil {
		return err
	}

	scanCtx, cancel, finished := process.GetManager().AddContextTimeout(ctx, setting.DependencyGraph.ScanTimeout, fmt.Sprintf("dependency scan of repository %d", repo.ID))
	defer finished()
	defer cancel()

	gitRepo, err := gitrepo.OpenRepository(scanCtx, repo)
	if err != nil {
		return fail(ctx, repo, old, repo_model.DependencyErrGit, err)
	}
	defer gitRepo.Close()
	sha, err := gitRepo.GetBranchCommitID(repo.DefaultBranch)
	if err != nil {
		return fail(ctx, repo, old, repo_model.DependencyErrGit, err)
	}
	if old != nil && old.CommitSHA == sha && old.Error == "" {
		return nil // nothing changed, e.g. a mirror sync of other branches
	}

	scan, deps, code, err := scanCommit(scanCtx, repo, gitRepo, sha)
	if err != nil {
		return fail(ctx, repo, old, code, err)
	}
	// the stored rows belong to sha; the database write is not bound to the scan's time budget
	return repo_model.ReplaceRepoDependencies(ctx, scan, deps)
}

// fail records the error code of a scan and keeps the old rows (and the commit they belong to).
func fail(ctx context.Context, repo *repo_model.Repository, old *repo_model.DependencyScan, code string, cause error) error {
	if ctx.Err() != nil {
		return ctx.Err() // shutting down: not a failure of the repository
	}
	log.Warn("dependency graph: scan of repository %d failed (%s): %v", repo.ID, code, cause)
	s := &repo_model.DependencyScan{RepoID: repo.ID, ScannedUnix: timeutil.TimeStampNow(), Status: repo_model.DependencyScanFailed, Error: code}
	if old != nil {
		s.CommitSHA, s.ManifestCount, s.DependencyCount, s.Skipped = old.CommitSHA, old.ManifestCount, old.DependencyCount, old.Skipped
	}
	return repo_model.ReplaceRepoDependencies(ctx, s, nil)
}

func scanCommit(ctx context.Context, repo *repo_model.Repository, gitRepo *git.Repository, sha string) (*repo_model.DependencyScan, []*repo_model.Dependency, string, error) {
	cfg := &setting.DependencyGraph
	var manifests []treeEntry
	aux := map[string]treeEntry{}
	err := walkTree(ctx, gitRepo, sha, cfg.MaxTreeEntries, func(e treeEntry) {
		if hasExcludedSegment(e.path) {
			return
		}
		base := path.Base(e.path)
		if auxNames[base] {
			aux[e.path] = e
		}
		if dependency.FormatFor(base) != nil {
			manifests = append(manifests, e)
		}
	})
	switch {
	case errors.Is(err, errTreeTooLarge):
		return nil, nil, repo_model.DependencyErrTreeTooLarge, err
	case ctx.Err() != nil:
		return nil, nil, repo_model.DependencyErrTimeout, ctx.Err()
	case err != nil:
		return nil, nil, repo_model.DependencyErrGit, err
	}

	readBlob := func(e treeEntry, limit int64) ([]byte, error) {
		if e.size > limit {
			return nil, io.ErrShortBuffer
		}
		blob, err := gitRepo.GetBlob(e.id)
		if err != nil {
			return nil, err
		}
		r, _, err := blob.NewReader(limit)
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return io.ReadAll(io.LimitReader(r, limit+1))
	}
	cache := map[string][]byte{}
	find := func(manifestPath string) dependency.Finder {
		return func(name string, up bool) []byte {
			for dir := path.Dir(manifestPath); ; dir = path.Dir(dir) {
				p := path.Join(dir, name)
				if e, ok := aux[p]; ok {
					if b, seen := cache[p]; seen {
						return b
					}
					b, err := readBlob(e, dependency.MaxSmallManifestSize)
					if err != nil {
						b = nil
					}
					cache[p] = b
					return b
				}
				if !up || dir == "." {
					return nil
				}
			}
		}
	}

	scan := &repo_model.DependencyScan{RepoID: repo.ID, CommitSHA: sha, ScannedUnix: timeutil.TimeStampNow(), Status: repo_model.DependencyScanOK}
	deps := []*repo_model.Dependency{}
	var skipped []repo_model.SkippedManifest
	skip := func(p, code string) {
		if len(skipped) < 100 {
			skipped = append(skipped, repo_model.SkippedManifest{Path: dependency.SafeText(p), Code: code})
		}
	}
	for i, m := range manifests {
		if ctx.Err() != nil {
			return nil, nil, repo_model.DependencyErrTimeout, ctx.Err()
		}
		if i >= cfg.MaxManifests {
			skip(m.path, repo_model.DependencySkipTooMany)
			continue
		}
		if !dependency.ValidPath(m.path) {
			skip(m.path, repo_model.DependencySkipInvalidPath)
			continue
		}
		format := dependency.FormatFor(path.Base(m.path))
		limit := cfg.MaxManifestSize
		if format.Small {
			limit = min(limit, dependency.MaxSmallManifestSize)
		}
		if m.size > limit {
			skip(m.path, repo_model.DependencySkipTooLarge)
			continue
		}
		data, err := readBlob(m, limit)
		if err != nil {
			skip(m.path, repo_model.DependencySkipRead)
			continue
		}
		parsed, err := parseManifest(ctx, format, data, find(m.path))
		if ctx.Err() != nil {
			return nil, nil, repo_model.DependencyErrTimeout, ctx.Err()
		}
		if err != nil {
			skip(m.path, repo_model.DependencySkipParse)
			continue
		}
		if len(deps)+len(parsed) > cfg.MaxDependencies {
			skip(m.path, repo_model.DependencySkipTooManyDeps)
			continue
		}
		scan.ManifestCount++
		for _, d := range parsed {
			deps = append(deps, &repo_model.Dependency{
				RepoID: repo.ID, Ecosystem: d.Ecosystem, Name: d.Name, Version: d.Version,
				ManifestPath: m.path, Direct: d.Direct, License: d.License,
			})
		}
	}
	scan.DependencyCount = len(deps)
	scan.SetSkippedManifests(skipped)
	return scan, deps, "", nil
}
