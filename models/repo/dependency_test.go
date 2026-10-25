// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repo_test

import (
	"testing"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/optional"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplaceRepoDependencies(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	first := []*repo_model.Dependency{
		{RepoID: 1, Ecosystem: "Go", Name: "a", Version: "v1", ManifestPath: "go.mod", Direct: true},
		{RepoID: 1, Ecosystem: "npm", Name: "b", Version: "2", ManifestPath: "web/package-lock.json"},
	}
	scan := &repo_model.DependencyScan{RepoID: 1, CommitSHA: "aaaa", Status: repo_model.DependencyScanOK, ManifestCount: 2, DependencyCount: 2}
	scan.SetSkippedManifests([]repo_model.SkippedManifest{{Path: "x/yarn.lock", Code: repo_model.DependencySkipParse}})
	require.NoError(t, repo_model.ReplaceRepoDependencies(ctx, scan, first))
	other := &repo_model.Dependency{RepoID: 2, Ecosystem: "Go", Name: "z"}
	require.NoError(t, db.Insert(ctx, other))

	got, err := repo_model.GetRepoDependencyScan(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, "aaaa", got.CommitSHA)
	assert.Equal(t, []repo_model.SkippedManifest{{Path: "x/yarn.lock", Code: "parse_error"}}, got.SkippedManifests())

	// a failed scan keeps the rows and the commit
	fail := &repo_model.DependencyScan{RepoID: 1, CommitSHA: "aaaa", Status: repo_model.DependencyScanFailed, Error: repo_model.DependencyErrTimeout}
	require.NoError(t, repo_model.ReplaceRepoDependencies(ctx, fail, nil))
	n, err := repo_model.CountRepoDependencies(ctx, 1)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
	got, _ = repo_model.GetRepoDependencyScan(ctx, 1)
	assert.Equal(t, "timeout", got.Error)

	// a scan replaces the rows of its repository only
	second := []*repo_model.Dependency{{RepoID: 1, Ecosystem: "Go", Name: "c", Version: "v3", ManifestPath: "go.mod", Direct: true}}
	require.NoError(t, repo_model.ReplaceRepoDependencies(ctx, &repo_model.DependencyScan{RepoID: 1, CommitSHA: "bbbb", Status: repo_model.DependencyScanOK}, second))
	n, _ = repo_model.CountRepoDependencies(ctx, 1)
	assert.EqualValues(t, 1, n)
	n, _ = repo_model.CountRepoDependencies(ctx, 2)
	assert.EqualValues(t, 1, n)
	count, err := db.GetEngine(ctx).Where("repo_id = 1").Count(new(repo_model.DependencyScan))
	require.NoError(t, err)
	assert.EqualValues(t, 1, count, "one scan row")

	// an empty result (no manifests) clears the rows
	require.NoError(t, repo_model.ReplaceRepoDependencies(ctx, &repo_model.DependencyScan{RepoID: 1, CommitSHA: "cccc", Status: repo_model.DependencyScanOK}, []*repo_model.Dependency{}))
	n, _ = repo_model.CountRepoDependencies(ctx, 1)
	assert.EqualValues(t, 0, n)
}

func TestSkippedManifests(t *testing.T) {
	s := &repo_model.DependencyScan{Skipped: `[{"path":"a","code":"<script>"},{"path":"b","code":"parse_error"}]`}
	assert.Equal(t, []repo_model.SkippedManifest{{Path: "a", Code: "internal_error"}, {Path: "b", Code: "parse_error"}}, s.SkippedManifests())
	assert.Nil(t, (&repo_model.DependencyScan{Skipped: "{"}).SkippedManifests())

	var many []repo_model.SkippedManifest
	for range 300 {
		many = append(many, repo_model.SkippedManifest{Path: "p", Code: "parse_error"})
	}
	s = &repo_model.DependencyScan{}
	s.SetSkippedManifests(many)
	assert.Len(t, s.SkippedManifests(), 100)
}

func TestSearchRepoDependencies(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()
	var rows []*repo_model.Dependency
	for _, n := range []string{"100%_sure", "left-pad", "Left_Pad2", "x!y", "other"} {
		rows = append(rows, &repo_model.Dependency{RepoID: 1, Ecosystem: "npm", Name: n, Version: "1", ManifestPath: "a/package-lock.json", Direct: n == "other"})
	}
	rows = append(rows, &repo_model.Dependency{RepoID: 1, Ecosystem: "Go", Name: "go-thing", ManifestPath: "go.mod"}, &repo_model.Dependency{RepoID: 2, Ecosystem: "Go", Name: "left-pad"})
	require.NoError(t, repo_model.ReplaceRepoDependencies(ctx, &repo_model.DependencyScan{RepoID: 1, Status: "ok"}, rows[:6]))
	require.NoError(t, repo_model.ReplaceRepoDependencies(ctx, &repo_model.DependencyScan{RepoID: 2, Status: "ok"}, rows[6:]))

	names := func(o *repo_model.DependencySearchOptions) []string {
		o.RepoID = 1
		got, total, err := db.FindAndCount[repo_model.Dependency](ctx, o)
		require.NoError(t, err)
		assert.EqualValues(t, len(got), total)
		var out []string
		for _, d := range got {
			out = append(out, d.Name)
		}
		return out
	}
	assert.Len(t, names(&repo_model.DependencySearchOptions{}), 6)
	assert.Equal(t, []string{"100%_sure"}, names(&repo_model.DependencySearchOptions{Q: "%"}), "% is a literal")
	assert.Equal(t, []string{"100%_sure", "Left_Pad2"}, names(&repo_model.DependencySearchOptions{Q: "_"}), "_ is a literal")
	assert.Equal(t, []string{"x!y"}, names(&repo_model.DependencySearchOptions{Q: "!"}))
	assert.Equal(t, []string{"Left_Pad2", "left-pad"}, names(&repo_model.DependencySearchOptions{Q: "LEFT"}))
	assert.Equal(t, []string{"go-thing"}, names(&repo_model.DependencySearchOptions{Ecosystem: "Go"}))
	assert.Equal(t, []string{"other"}, names(&repo_model.DependencySearchOptions{Direct: optional.Some(true)}))
	assert.Equal(t, []string{"go-thing"}, names(&repo_model.DependencySearchOptions{Manifest: "go.mod"}))
	assert.Empty(t, names(&repo_model.DependencySearchOptions{Q: "left-pad", Ecosystem: "Go"}))
}
