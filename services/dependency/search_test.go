// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import (
	"fmt"
	"testing"

	"forgejo.org/models/db"
	"forgejo.org/models/organization"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/dependency"
	"forgejo.org/modules/optional"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"
	"forgejo.org/modules/util"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The org search's SQL condition is GetUserRepoPermission(...).CanRead(Code) plus the rules of the search: every
// fixture user that may call (owners, site admins) against every repository that has a dependency row.
func TestDependencySearchCondMatchesPermission(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	defer test.MockVariableValue(&setting.DependencyGraph.Enabled, true)()
	ctx := db.DefaultContext

	var repos []*repo_model.Repository
	require.NoError(t, db.GetEngine(ctx).OrderBy("id").Find(&repos))
	for _, r := range repos {
		require.NoError(t, db.Insert(ctx, &repo_model.Dependency{RepoID: r.ID, Ecosystem: dependency.Go, Name: "example.com/x", Version: "v1.0.0", ManifestPath: "go.mod"}))
	}
	// one repository with the graph off, one trashed
	_, err := db.GetEngine(ctx).Exec("UPDATE repository SET dependency_graph_disabled = ? WHERE id = 3", true)
	require.NoError(t, err)
	_, err = db.GetEngine(ctx).Exec("UPDATE repository SET trashed_unix = 1 WHERE id = 5")
	require.NoError(t, err)
	// org3 requires two-factor authentication, and user4 (a member without it) is blocked there
	_, err = db.GetEngine(ctx).Exec("UPDATE `user` SET require_two_factor = ? WHERE id = 17", true)
	require.NoError(t, err)

	var orgs []*organization.Organization
	require.NoError(t, db.GetEngine(ctx).Where("type = ?", user_model.UserTypeOrganization).OrderBy("id").Find(&orgs))
	var users []*user_model.User
	require.NoError(t, db.GetEngine(ctx).Where("type = ?", user_model.UserTypeIndividual).OrderBy("id").Find(&users))

	listed := 0
	for _, org := range orgs {
		for _, u := range users {
			isOwner, err := org.IsOwnedBy(ctx, u.ID)
			require.NoError(t, err)
			if !isOwner && !u.IsAdmin {
				continue // only owners and site admins may call
			}
			rows, total, err := SearchUsage(ctx, SearchScope{Org: org, Doer: u, Admin: u.IsAdmin}, UsageFilter{Ecosystem: dependency.Go, Name: "example.com/x"}, db.ListOptions{Page: 1, PageSize: 500})
			require.NoError(t, err)
			got := map[int64]bool{}
			for _, r := range rows {
				got[r.RepoID] = true
			}
			assert.EqualValues(t, len(got), total)
			for _, r := range repos {
				if r.OwnerID != org.ID {
					assert.False(t, got[r.ID], "repo %d of another owner listed for org %d", r.ID, org.ID)
					continue
				}
				perm, err := access_model.GetUserRepoPermission(ctx, r, u)
				require.NoError(t, err)
				want := perm.CanRead(unit.TypeCode) && r.ID != 3 && r.ID != 5
				assert.Equal(t, want, got[r.ID], "org %d user %d repo %d", org.ID, u.ID, r.ID)
				if want {
					listed++
				}
			}
		}
	}
	assert.Positive(t, listed, "the test lists something")

	// the instance route: a site admin sees every owner's repository the rules allow
	admin := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	rows, _, err := SearchUsage(ctx, SearchScope{Doer: admin, Admin: true}, UsageFilter{Ecosystem: dependency.Go, Name: "example.com/x"}, db.ListOptions{Page: 1, PageSize: 500})
	require.NoError(t, err)
	got := map[int64]bool{}
	for _, r := range rows {
		got[r.RepoID] = true
	}
	for _, r := range repos {
		perm, err := access_model.GetUserRepoPermission(ctx, r, admin)
		require.NoError(t, err)
		assert.Equal(t, perm.CanRead(unit.TypeCode) && r.ID != 3 && r.ID != 5, got[r.ID], fmt.Sprint("instance, repo ", r.ID))
	}
}

func TestSearchUsageValidation(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	defer test.MockVariableValue(&setting.DependencyGraph.Enabled, true)()
	admin := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	scope := SearchScope{Doer: admin, Admin: true}
	for name, f := range map[string]UsageFilter{
		"nothing":                {},
		"ecosystem only":         {Ecosystem: dependency.NPM},
		"direct only":            {Direct: optional.Some(true)},
		"name without ecosystem": {Name: "x"},
		"unknown ecosystem":      {Ecosystem: "Cobol", Name: "x"},
		"version and range":      {Ecosystem: dependency.NPM, Name: "x", Version: "1.0.0", Range: "^1"},
		"unparseable range":      {Ecosystem: dependency.NPM, Name: "x", Range: "^^^"},
		"range without a name":   {Ecosystem: dependency.NPM, Range: "^1"},
		"long name":              {Ecosystem: dependency.NPM, Name: string(make([]byte, 513))},
		"version without a name": {Ecosystem: dependency.NPM, Version: "1"},
	} {
		_, _, err := SearchUsage(t.Context(), scope, f, db.ListOptions{})
		require.ErrorIs(t, err, util.ErrInvalidArgument, name)
	}
}

func TestSearchScopeNeedsAnOrganizationOrAnAdmin(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	defer test.MockVariableValue(&setting.DependencyGraph.Enabled, true)()
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	_, _, err := SearchScope{Doer: user}.RepoCond(db.DefaultContext)
	require.Error(t, err)
}

// An export reads batch after batch and lists the rows of SearchUsage, in its order; start sees the count first.
func TestExportUsageBatches(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	defer test.MockVariableValue(&setting.DependencyGraph.Enabled, true)()
	defer test.MockVariableValue(&ExportBatch, 2)()
	ctx := db.DefaultContext
	admin := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	scope := SearchScope{Doer: admin, Admin: true}
	for _, id := range []int64{1, 2, 3, 4, 5} {
		require.NoError(t, db.Insert(ctx, &repo_model.Dependency{RepoID: id, Ecosystem: dependency.Go, Name: "example.com/x", Version: "v1.0.0", ManifestPath: "go.mod"}))
	}
	f := UsageFilter{Ecosystem: dependency.Go, Name: "example.com/x"}
	want, total, err := SearchUsage(ctx, scope, f, db.ListOptions{Page: 1, PageSize: 50})
	require.NoError(t, err)
	require.Greater(t, total, int64(2))

	var got, wantIDs []int64
	batches := 0
	require.NoError(t, ExportUsage(ctx, scope, f, func(n int64) error {
		assert.Equal(t, total, n)
		assert.Empty(t, got)
		return nil
	}, func(rows []*UsageRow) error {
		batches++
		for _, r := range rows {
			got = append(got, r.ID)
		}
		return nil
	}))
	for _, r := range want {
		wantIDs = append(wantIDs, r.ID)
	}
	assert.Equal(t, wantIDs, got)
	assert.Equal(t, (int(total)+1)/2, batches)

	defer test.MockVariableValue(&MaxExportRows, 2)()
	called := false
	err = ExportUsage(ctx, scope, f, func(int64) error { called = true; return nil }, func([]*UsageRow) error { return nil })
	require.ErrorIs(t, err, util.ErrInvalidArgument)
	assert.False(t, called, "nothing starts above the cap")
}

// Rows deleted while an export runs are dropped; no other row is skipped or repeated, since the export loads by the
// IDs it read first (an OFFSET read would skip a row of the second repository here).
func TestExportUsageSnapshot(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	defer test.MockVariableValue(&setting.DependencyGraph.Enabled, true)()
	defer test.MockVariableValue(&ExportBatch, 2)()
	ctx := db.DefaultContext
	admin := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	scope := SearchScope{Doer: admin, Admin: true}
	for _, id := range []int64{1, 2} {
		for _, m := range []string{"a/go.mod", "b/go.mod", "c/go.mod"} {
			require.NoError(t, db.Insert(ctx, &repo_model.Dependency{RepoID: id, Ecosystem: dependency.Go, Name: "example.com/x", Version: "v1.0.0", ManifestPath: m}))
		}
	}
	f := UsageFilter{Ecosystem: dependency.Go, Name: "example.com/x"}
	all, _, err := SearchUsage(ctx, scope, f, db.ListOptions{Page: 1, PageSize: 50})
	require.NoError(t, err)
	require.Len(t, all, 6)
	first, second := all[0].RepoID, all[5].RepoID
	require.NotEqual(t, first, second)

	deleted := false
	afterExportBatch = func() {
		if !deleted { // after the first batch (two rows of the first repository): they go
			deleted = true
			_, err := db.GetEngine(ctx).Where("repo_id = ? AND manifest_path <> ?", first, "c/go.mod").Delete(&repo_model.Dependency{})
			require.NoError(t, err)
		}
	}
	defer func() { afterExportBatch = nil }()

	var got []int64
	var count int64
	require.NoError(t, ExportUsage(ctx, scope, f, func(n int64) error { count = n; return nil }, func(rows []*UsageRow) error {
		for _, r := range rows {
			got = append(got, r.ID)
		}
		return nil
	}))
	assert.EqualValues(t, 6, count)
	seen := map[int64]bool{}
	secondRows := 0
	for _, id := range got {
		assert.False(t, seen[id], "row %d repeated", id)
		seen[id] = true
	}
	for _, r := range all {
		if r.RepoID == second {
			assert.True(t, seen[r.ID], "row %d of the other repository is missing", r.ID)
			secondRows++
		}
	}
	assert.Equal(t, 3, secondRows)
	assert.Len(t, got, 6) // the two deleted rows were sent already
}
