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
