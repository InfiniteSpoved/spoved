// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package code

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/git"
	"forgejo.org/modules/indexer/code/bleve"
	"forgejo.org/modules/indexer/code/elasticsearch"
	"forgejo.org/modules/indexer/code/internal"
	"forgejo.org/modules/indexer/code/zoekt"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	unittest.MainTest(m)
}

func testIndexer(name string, t *testing.T, indexer internal.Indexer) {
	t.Run(name, func(t *testing.T) {
		var repoID int64 = 1
		err := index(git.DefaultContext, indexer, repoID)
		require.NoError(t, err)
		keywords := []struct {
			RepoIDs  []int64
			Keyword  string
			IDs      []int64
			Langs    int
			Filename string
		}{
			{
				RepoIDs: nil,
				Keyword: "Description",
				IDs:     []int64{repoID},
				Langs:   1,
			},
			{
				RepoIDs: []int64{2},
				Keyword: "Description",
				IDs:     []int64{},
				Langs:   0,
			},
			{
				RepoIDs:  nil,
				Keyword:  "Description",
				IDs:      []int64{},
				Langs:    0,
				Filename: "NOT-README.md",
			},
			{
				RepoIDs:  nil,
				Keyword:  "Description",
				IDs:      []int64{repoID},
				Langs:    1,
				Filename: "README.md",
			},
			{
				RepoIDs: nil,
				Keyword: "Description for",
				IDs:     []int64{repoID},
				Langs:   1,
			},
			{
				RepoIDs: nil,
				Keyword: "repo1",
				IDs:     []int64{repoID},
				Langs:   1,
			},
			{
				RepoIDs: []int64{2},
				Keyword: "repo1",
				IDs:     []int64{},
				Langs:   0,
			},
			{
				RepoIDs: nil,
				Keyword: "non-exist",
				IDs:     []int64{},
				Langs:   0,
			},
		}

		for _, kw := range keywords {
			t.Run(kw.Keyword, func(t *testing.T) {
				total, res, langs, err := indexer.Search(t.Context(), &internal.SearchOptions{
					RepoIDs: kw.RepoIDs,
					Keyword: kw.Keyword,
					Paginator: &db.ListOptions{
						Page:     1,
						PageSize: 10,
					},
					Filename: kw.Filename,
					Mode:     SearchModeUnion,
				})
				require.NoError(t, err)
				assert.Len(t, kw.IDs, int(total))
				assert.Len(t, langs, kw.Langs)

				ids := make([]int64, 0, len(res))
				for _, hit := range res {
					ids = append(ids, hit.RepoID)
					assert.Equal(t, "# repo1\n\nDescription for repo1", hit.Content)
				}
				assert.Equal(t, kw.IDs, ids)
			})
		}

		t.Run("Fuzzy", func(t *testing.T) {
			for _, kw := range []struct {
				keyword string
				ids     []int64
			}{
				{
					keyword: "reppo1", // should match repo1
					ids:     []int64{repoID},
				},
				{
					keyword: "1", // must not be fuzzy match only repo1
					ids:     []int64{repoID},
				},
				{
					keyword: "Description!", // should match "Description"
					ids:     []int64{repoID},
				},
				{
					keyword: "escription", // should match "Description"
					ids:     []int64{repoID},
				},
				{
					keyword: "form", // should match "for"
					ids:     []int64{repoID},
				},
				{
					keyword: "invalid", // should not match anything
					ids:     []int64{},
				},
			} {
				t.Run(kw.keyword, func(t *testing.T) {
					_, res, _, err := indexer.Search(t.Context(), &internal.SearchOptions{
						Keyword: kw.keyword,
						Paginator: &db.ListOptions{
							Page:     1,
							PageSize: 10,
						},
						Mode: SearchModeFuzzy,
					})
					require.NoError(t, err)

					ids := make([]int64, 0, len(res))
					for _, hit := range res {
						ids = append(ids, hit.RepoID)
					}

					assert.Equal(t, kw.ids, ids)
				})
			}
		})

		t.Run("Punctuation", func(t *testing.T) { testPunctuation(t, indexer, repoID) })
		t.Run("Branches", func(t *testing.T) { testBranches(t, indexer, repoID) })

		require.NoError(t, indexer.Delete(t.Context(), repoID))
	})
}

// pushFiles commits the files to the default branch of the repo, or to a new branch made from it, the way a push would.
// It returns the pushed commit.
func pushFiles(t *testing.T, repoID int64, branch string, files map[string]string) string {
	t.Helper()
	repo, err := repo_model.GetRepositoryByID(t.Context(), repoID)
	require.NoError(t, err)
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	run("clone", "-q", "-b", repo.DefaultBranch, repo.RepoPath(), ".")
	if branch == "" {
		branch = repo.DefaultBranch
	} else {
		run("checkout", "-q", "-b", branch)
	}
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	run("add", ".")
	run("commit", "-q", "-m", "files")
	run("push", "-q", "origin", "HEAD:"+branch)
	return run("rev-parse", "HEAD")
}

// testPunctuation: exact mode keeps punctuation, files with the same words in
// another order or without the punctuation don't match.
func testPunctuation(t *testing.T, indexer internal.Indexer, repoID int64) {
	cases := []struct{ query, hit, miss string }{
		{"foo.bar(", "x=foo.bar(y)", "bar.foo( foo bar"},
		{"a->b", "if(a->b)", "b->a a b"},
		{"std::map<", "std::map<int> m;", "map::std< std map"},
		{"#include <stdio.h>", "#include <stdio.h>", "include stdio.h <stdio.h> #"},
		{"user_id=", "user_id=5", "id=user_ user id"},
		{"@Override", "@Override void f()", "Override @"},
		{"i++", "for(i++)", "+ + i"},
	}
	files := map[string]string{}
	for i, c := range cases {
		files[fmt.Sprintf("hit%d.txt", i)] = c.hit
		files[fmt.Sprintf("miss%d.txt", i)] = c.miss
	}
	pushFiles(t, repoID, "", files)
	require.NoError(t, index(git.DefaultContext, indexer, repoID))

	for i, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			_, res, _, err := indexer.Search(t.Context(), &internal.SearchOptions{
				RepoIDs:   []int64{repoID},
				Keyword:   c.query,
				Mode:      SearchModeExact,
				Paginator: &db.ListOptions{Page: 1, PageSize: 20},
			})
			require.NoError(t, err)
			names := make([]string, 0, len(res))
			for _, r := range res {
				names = append(names, r.Filename)
			}
			assert.Equal(t, []string{fmt.Sprintf("hit%d.txt", i)}, names)
		})
	}
}

func TestBleveIndexAndSearch(t *testing.T) {
	unittest.PrepareTestEnv(t)

	dir := t.TempDir()

	idx := bleve.NewIndexer(dir)
	_, err := idx.Init(t.Context())
	if err != nil {
		if idx != nil {
			idx.Close()
		}
		require.NoError(t, err)
	}
	defer idx.Close()

	testIndexer("bleve", t, idx)
}

func TestESIndexAndSearch(t *testing.T) {
	unittest.PrepareTestEnv(t)

	u := os.Getenv("TEST_INDEXER_CODE_ES_URL")
	if u == "" {
		t.SkipNow()
		return
	}

	indexer := elasticsearch.NewIndexer(u, "gitea_codes")
	if _, err := indexer.Init(t.Context()); err != nil {
		if indexer != nil {
			indexer.Close()
		}
		assert.FailNow(t, "Unable to init ES indexer", "error: %v", err)
	}

	defer indexer.Close()

	testIndexer("elastic_search", t, indexer)
}

// testBranches: the same file on the default branch and on another branch is found per branch,
// and removing the branch leaves the default one.
func testBranches(t *testing.T, indexer internal.Indexer, repoID int64) {
	pushFiles(t, repoID, "", map[string]string{"same.txt": "defaultcontent marker"})
	require.NoError(t, index(git.DefaultContext, indexer, repoID))

	sha := pushFiles(t, repoID, "idxbranch", map[string]string{"same.txt": "branchcontent marker"})
	repo, err := repo_model.GetRepositoryByID(t.Context(), repoID)
	require.NoError(t, err)
	changes, err := getRepoChanges(t.Context(), repo, "idxbranch", "", sha)
	require.NoError(t, err)
	require.NoError(t, indexer.Index(t.Context(), repo, "idxbranch", sha, false, changes))

	contents := func(branch string, all bool) []string {
		_, res, _, err := indexer.Search(t.Context(), &internal.SearchOptions{
			RepoIDs:     []int64{repoID},
			Keyword:     "marker",
			Mode:        SearchModeUnion,
			Branch:      branch,
			AllBranches: all,
			Paginator:   &db.ListOptions{Page: 1, PageSize: 20},
		})
		require.NoError(t, err)
		found := make([]string, 0, len(res))
		for _, r := range res {
			found = append(found, r.Content)
		}
		slices.Sort(found)
		return found
	}
	assert.Equal(t, []string{"defaultcontent marker"}, contents("", false))
	assert.Equal(t, []string{"branchcontent marker"}, contents("idxbranch", false))
	assert.Equal(t, []string{"branchcontent marker", "defaultcontent marker"}, contents("", true))

	require.NoError(t, indexer.DeleteBranch(t.Context(), repoID, "idxbranch"))
	assert.Empty(t, contents("idxbranch", false))
	assert.Equal(t, []string{"defaultcontent marker"}, contents("", true))
}

// zoekt indexes all branches in one go: a file that only exists on another branch is found there and
// with all branches, never on the default branch, and the files of the default branch stay as before.
func TestZoektBranches(t *testing.T) {
	unittest.PrepareTestEnv(t)

	idx := zoekt.NewIndexer(t.TempDir())
	if _, err := idx.Init(t.Context()); err != nil {
		t.Skipf("zoekt is not available: %v", err)
	}
	defer idx.Close()
	const repoID = 1
	setPatterns(t, repoID, "branch2")
	require.NoError(t, index(git.DefaultContext, idx, repoID))

	found := func(branch string, all bool, keyword string) []string {
		var names []string
		require.Eventually(t, func() bool { // the shards are picked up asynchronously
			_, res, _, err := idx.Search(t.Context(), &internal.SearchOptions{
				RepoIDs: []int64{repoID}, Keyword: keyword, Mode: SearchModeExact, Branch: branch, AllBranches: all,
				Paginator: &db.ListOptions{Page: 1, PageSize: 20},
			})
			require.NoError(t, err)
			names = names[:0]
			for _, r := range res {
				names = append(names, r.Filename)
			}
			return len(names) > 0
		}, 5*time.Second, 50*time.Millisecond, keyword)
		return names
	}

	assert.Equal(t, []string{"README.md"}, found("", false, "Description"))
	assert.Contains(t, found("", true, "Description"), "README.md")
	assert.Contains(t, found("branch2", false, "Description"), "README.md")
}

// A real zoekt search of a real repo returns chunk matches: the formatted hit keeps the file's
// line numbers and the highlight, as it did with whole files.
func TestZoektChunkHighlights(t *testing.T) {
	unittest.PrepareTestEnv(t)

	idx := zoekt.NewIndexer(t.TempDir())
	if _, err := idx.Init(t.Context()); err != nil {
		t.Skipf("zoekt is not available: %v", err)
	}
	defer idx.Close()
	const repoID = 1
	require.NoError(t, index(git.DefaultContext, idx, repoID))

	var res []*internal.SearchResult
	require.Eventually(t, func() bool { // the shards are picked up asynchronously
		var err error
		_, res, _, err = idx.Search(t.Context(), &internal.SearchOptions{
			RepoIDs: []int64{repoID}, Keyword: "Description", Mode: SearchModeExact,
			Paginator: &db.ListOptions{Page: 1, PageSize: 20},
		})
		require.NoError(t, err)
		return len(res) > 0
	}, 5*time.Second, 50*time.Millisecond)

	hit, err := idx.Formatter().Format(res[0])
	require.NoError(t, err)
	assert.Equal(t, "README.md", hit.Filename)
	require.NotEmpty(t, hit.Lines)
	highlighted := false
	for _, l := range hit.Lines {
		if strings.Contains(string(l.FormattedContent), `<span class="search-highlight">Description</span>`) {
			highlighted = true
			assert.Contains(t, res[0].LineNumbers, l.Num)
		}
	}
	assert.True(t, highlighted)
}
