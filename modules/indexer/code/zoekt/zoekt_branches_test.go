// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build unix && !openbsd

package zoekt

import (
	"context"
	"os"
	"regexp/syntax"
	"strings"
	"testing"
	"time"

	"forgejo.org/models/db"
	"forgejo.org/modules/indexer/code/internal"
	inner_zoekt "forgejo.org/modules/indexer/internal/zoekt"

	"github.com/sourcegraph/zoekt"
	"github.com/sourcegraph/zoekt/index"
	"github.com/sourcegraph/zoekt/query"
	"github.com/sourcegraph/zoekt/search"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// andChildren flattens the nested And nodes.
func andChildren(t *testing.T, q query.Q) []query.Q {
	t.Helper()
	and, ok := q.(*query.And)
	if !ok {
		return []query.Q{q}
	}
	var out []query.Q
	for _, c := range and.Children {
		if _, nested := c.(*query.And); nested {
			out = append(out, andChildren(t, c)...)
		} else {
			out = append(out, c)
		}
	}
	return out
}

func findQuery[T query.Q](children []query.Q) (T, bool) {
	for _, c := range children {
		if v, ok := c.(T); ok {
			return v, true
		}
	}
	var zero T
	return zero, false
}

// AC10: the regex is built directly (never through query.Parse) on content only.
func TestGenerateZoektQueryRegex(t *testing.T) {
	b := &Indexer{}
	q, err := b.generateZoektQuery(&internal.SearchOptions{Keyword: `func\s+Test\w+`, Mode: internal.CodeSearchModeRegex, RepoIDs: []int64{1}})
	require.NoError(t, err)
	re, ok := findQuery[*query.Regexp](andChildren(t, q))
	require.True(t, ok)
	assert.True(t, re.Content)
	assert.False(t, re.FileName)
	want, err := syntax.Parse(`func\s+Test\w+`, syntax.Perl)
	require.NoError(t, err)
	assert.Equal(t, want.String(), re.Regexp.String())

	// operators of the zoekt query language stay literal in the other modes
	q, err = b.generateZoektQuery(&internal.SearchOptions{Keyword: `file:secret.go`, Mode: internal.CodeSearchModeExact})
	require.NoError(t, err)
	re, ok = findQuery[*query.Regexp](andChildren(t, q))
	require.True(t, ok)
	assert.True(t, re.Content)
	assert.False(t, re.FileName, "file: is an operator of the zoekt query language")
}

func TestGenerateZoektQueryRegexErrors(t *testing.T) {
	b := &Indexer{}
	for _, keyword := range []string{`(unclosed`, `a**`, `[z-a]`, strings.Repeat("a", internal.MaxKeywordLength+1)} {
		_, err := b.generateZoektQuery(&internal.SearchOptions{Keyword: keyword, Mode: internal.CodeSearchModeRegex})
		require.ErrorIs(t, err, internal.ErrInvalidQuery, keyword)
	}
}

// The branch filter is a structural exact match; the default branch is HEAD.
func TestGenerateZoektQueryBranch(t *testing.T) {
	b := &Indexer{}
	branchOf := func(opts *internal.SearchOptions) (*query.Branch, bool) {
		opts.Keyword = "x"
		q, err := b.generateZoektQuery(opts)
		require.NoError(t, err)
		return findQuery[*query.Branch](andChildren(t, q))
	}

	br, ok := branchOf(&internal.SearchOptions{})
	require.True(t, ok)
	assert.Equal(t, &query.Branch{Pattern: "HEAD", Exact: true}, br)

	br, ok = branchOf(&internal.SearchOptions{Branch: "feat/x"})
	require.True(t, ok)
	assert.Equal(t, &query.Branch{Pattern: "feat/x", Exact: true}, br)

	_, ok = branchOf(&internal.SearchOptions{AllBranches: true})
	assert.False(t, ok)
}

type stubSearcher struct {
	zoekt.Streamer
	opts  *zoekt.SearchOptions
	files []zoekt.FileMatch
	err   error
}

func (s *stubSearcher) Search(_ context.Context, _ query.Q, opts *zoekt.SearchOptions) (*zoekt.SearchResult, error) {
	s.opts = opts
	return &zoekt.SearchResult{Files: s.files, Stats: zoekt.Stats{FileCount: 12345}}, s.err
}

func matchFile(name string) zoekt.FileMatch {
	return zoekt.FileMatch{
		RepositoryID: 1, FileName: name, Content: []byte("x\n"), Branches: []string{"HEAD"},
		LineMatches: []zoekt.LineMatch{{LineNumber: 1, LineFragments: []zoekt.LineFragmentMatch{{Offset: 0, MatchLength: 1}}}},
	}
}

// F4: the caps are constants, the displayed documents follow the page but never exceed the window,
// and only the page is converted.
func TestSearchCaps(t *testing.T) {
	files := make([]zoekt.FileMatch, 0, 50)
	for i := range 50 {
		files = append(files, matchFile(string(rune('a'+i%26))+strings.Repeat("x", i)))
	}
	stub := &stubSearcher{files: files}
	b := &Indexer{inner: &inner_zoekt.Indexer{Searcher: stub}}

	total, res, _, err := b.Search(t.Context(), &internal.SearchOptions{Keyword: "x", Paginator: &db.ListOptions{Page: 2, PageSize: 10}})
	require.NoError(t, err)
	assert.Equal(t, 20, stub.opts.MaxDocDisplayCount)
	assert.Positive(t, stub.opts.ShardMaxMatchCount)
	assert.Positive(t, stub.opts.TotalMaxMatchCount)
	assert.True(t, stub.opts.Whole)
	assert.EqualValues(t, 12345, total) // from the stats, not from len(files)
	assert.Len(t, res, 10)
	assert.Equal(t, files[10].FileName, res[0].Filename)

	// far pages are clamped to the window
	_, _, _, err = b.Search(t.Context(), &internal.SearchOptions{Keyword: "x", Paginator: &db.ListOptions{Page: 1000, PageSize: 50}})
	require.NoError(t, err)
	assert.LessOrEqual(t, stub.opts.MaxDocDisplayCount, internal.MaxSearchWindow)
}

// Z4: a regex that runs into the timeout is an error of its own.
func TestSearchRegexTimeout(t *testing.T) {
	stub := &stubSearcher{err: context.DeadlineExceeded}
	b := &Indexer{inner: &inner_zoekt.Indexer{Searcher: stub}}
	_, _, _, err := b.Search(t.Context(), &internal.SearchOptions{Keyword: "a+", Mode: internal.CodeSearchModeRegex, Paginator: &db.ListOptions{Page: 1, PageSize: 10}})
	require.ErrorIs(t, err, internal.ErrQueryTooExpensive)
}

func TestSearchResultBranch(t *testing.T) {
	f := matchFile("a.go")
	f.Branches = []string{"HEAD", "dev"}
	assert.Equal(t, "HEAD", convertZoektResult([]zoekt.FileMatch{f})[0].Branch)
}

// AC3: files are grouped by (path, blob), so identical files on several branches are one document.
func TestGroupBranchFiles(t *testing.T) {
	docs := groupBranchFiles([]internal.BranchFiles{
		{Name: "main", Files: []internal.FileUpdate{{Filename: "a.go", BlobSha: "1"}, {Filename: "b.go", BlobSha: "2"}}},
		{Name: "dev", Files: []internal.FileUpdate{{Filename: "a.go", BlobSha: "1"}, {Filename: "b.go", BlobSha: "3"}, {Filename: "c.go", BlobSha: "4"}}},
	}, "main")

	byKey := map[string][]string{}
	for _, d := range docs {
		byKey[d.update.Filename+":"+d.update.BlobSha] = d.branches
	}
	assert.Equal(t, map[string][]string{
		"a.go:1": {"HEAD", "dev"}, // same blob on both
		"b.go:2": {"HEAD"},
		"b.go:3": {"dev"},
		"c.go:4": {"dev"},
	}, byKey)
}

// buildBranchIndex is IndexBranches without git: a non-delta build of the given documents.
func buildBranchIndex(dir string, repoID int64, branches []zoekt.RepositoryBranch, docs []index.Document, sizeMax int) error {
	opts := zoektOptions(dir, repoID, branches, false)
	opts.SizeMax = sizeMax
	opts.SetDefaults()
	builder, err := index.NewBuilder(opts)
	if err != nil {
		return err
	}
	for _, d := range docs {
		if err := builder.Add(d); err != nil {
			return err
		}
	}
	return builder.Finish()
}

func shardCount(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	n := 0
	for _, e := range entries {
		if strings.Contains(e.Name(), ".zoekt") {
			n++
		}
	}
	return n
}

func searchBranch(t *testing.T, dir, branch string) []string {
	t.Helper()
	s, err := search.NewDirectorySearcherFast(dir)
	require.NoError(t, err)
	defer s.Close()
	var files []string
	require.Eventually(t, func() bool {
		res, err := s.Search(t.Context(), query.NewAnd(&query.Substring{Pattern: "marker", Content: true}, &query.Branch{Pattern: branch, Exact: true}), &zoekt.SearchOptions{})
		require.NoError(t, err)
		files = files[:0]
		for _, f := range res.Files {
			files = append(files, f.FileName)
		}
		return res.Stats.ShardsScanned > 0
	}, 5*time.Second, 50*time.Millisecond)
	return files
}

// Z2: a rebuild replaces the shards of the repo, a branch dropped in the second build is gone
// and no stale shard files are left.
func TestBuildBranchIndexReplacesShards(t *testing.T) {
	dir := t.TempDir()
	doc := func(name string, branches ...string) index.Document {
		return index.Document{Name: name, Content: []byte("marker " + name), Branches: branches}
	}

	require.NoError(t, buildBranchIndex(dir, 7, []zoekt.RepositoryBranch{{Name: "HEAD", Version: "aaa"}, {Name: "dev", Version: "bbb"}},
		[]index.Document{doc("main.txt", "HEAD"), doc("dev.txt", "dev")}, 1<<20))
	assert.Equal(t, []string{"dev.txt"}, searchBranch(t, dir, "dev"))
	assert.Equal(t, []string{"main.txt"}, searchBranch(t, dir, "HEAD"))
	first := shardCount(t, dir)

	require.NoError(t, buildBranchIndex(dir, 7, []zoekt.RepositoryBranch{{Name: "HEAD", Version: "ccc"}},
		[]index.Document{doc("main.txt", "HEAD")}, 1<<20))
	assert.Empty(t, searchBranch(t, dir, "dev"))
	assert.Equal(t, []string{"main.txt"}, searchBranch(t, dir, "HEAD"))
	assert.Equal(t, first, shardCount(t, dir), "stale shards left behind")
}
