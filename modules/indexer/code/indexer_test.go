// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package code

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/git"
	"forgejo.org/modules/indexer/code/bleve"
	"forgejo.org/modules/indexer/code/elasticsearch"
	"forgejo.org/modules/indexer/code/internal"

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

		require.NoError(t, indexer.Delete(t.Context(), repoID))
	})
}

// pushFiles commits the files to the default branch of the repo, the way a push would.
func pushFiles(t *testing.T, repoID int64, files map[string]string) {
	t.Helper()
	repo, err := repo_model.GetRepositoryByID(t.Context(), repoID)
	require.NoError(t, err)
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("clone", "-q", "-b", repo.DefaultBranch, repo.RepoPath(), ".")
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	run("add", ".")
	run("commit", "-q", "-m", "files")
	run("push", "-q", "origin", "HEAD:"+repo.DefaultBranch)
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
	pushFiles(t, repoID, files)
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
