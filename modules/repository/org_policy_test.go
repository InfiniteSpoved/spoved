// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repository

import (
	"testing"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/structs"
	"forgejo.org/modules/util"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckOrgPolicy(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := db.DefaultContext
	user := func(id int64) *user_model.User { return unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: id}) }
	owner, member, teamAdmin, siteAdmin, outsider := user(2), user(4), user(28), user(1), user(5)

	// org3 owns the private repo3 and the public repo21; user2 is its only owner, user28 is in an admin team
	org := user(3)
	org.RestrictRepoVisibilityChange, org.RestrictRepoDeleteTransfer = true, true
	repo := func(id int64) *repo_model.Repository {
		r := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: id})
		r.Owner = org
		return r
	}
	private, public := repo(3), repo(32)

	check := func(doer *user_model.User, r *repo_model.Repository, a OrgPolicyAction, into *user_model.User) error {
		return CheckOrgPolicy(ctx, doer, r, a, into)
	}
	refused := func(t *testing.T, err error, policy string) {
		t.Helper()
		var e ErrOrgPolicy
		require.ErrorAs(t, err, &e)
		assert.Equal(t, policy, e.Policy)
		require.ErrorIs(t, err, util.ErrPermissionDenied)
	}

	t.Run("visibility, delete and transfer", func(t *testing.T) {
		for _, a := range []OrgPolicyAction{OrgPolicyVisibility, OrgPolicyDelete, OrgPolicyTransfer} {
			policy := "restrict_repo_delete_transfer"
			if a == OrgPolicyVisibility {
				policy = "restrict_repo_visibility_change"
			}
			require.NoError(t, check(owner, private, a, nil))
			require.NoError(t, check(siteAdmin, private, a, nil))
			refused(t, check(member, private, a, nil), policy)
			refused(t, check(teamAdmin, public, a, nil), policy)
		}
	})

	t.Run("policies off", func(t *testing.T) {
		defer func() { org.RestrictRepoVisibilityChange, org.RestrictRepoDeleteTransfer = true, true }()
		org.RestrictRepoVisibilityChange, org.RestrictRepoDeleteTransfer = false, false
		for _, a := range []OrgPolicyAction{OrgPolicyVisibility, OrgPolicyDelete, OrgPolicyTransfer, OrgPolicyFork} {
			require.NoError(t, check(member, private, a, member))
		}
	})

	t.Run("inactive or prohibited site admin is bound", func(t *testing.T) {
		inactive, prohibited := *siteAdmin, *siteAdmin
		inactive.IsActive, prohibited.ProhibitLogin = false, true
		refused(t, check(&inactive, private, OrgPolicyDelete, nil), "restrict_repo_delete_transfer")
		refused(t, check(&prohibited, private, OrgPolicyVisibility, nil), "restrict_repo_visibility_change")
	})

	t.Run("system paths", func(t *testing.T) {
		require.NoError(t, check(nil, private, OrgPolicyDelete, nil))
		require.NoError(t, check(&user_model.User{ID: -1, Name: "(Cron)"}, private, OrgPolicyDelete, nil))
	})

	t.Run("user-owned repository", func(t *testing.T) {
		r := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 2}) // private, owned by user2
		for _, a := range []OrgPolicyAction{OrgPolicyVisibility, OrgPolicyDelete, OrgPolicyTransfer, OrgPolicyFork} {
			require.NoError(t, check(outsider, r, a, outsider))
		}
	})

	t.Run("fork", func(t *testing.T) {
		defer func() { org.PrivateForkPolicy, org.Visibility = user_model.PrivateForkAll, structs.VisibleTypePublic }()

		org.PrivateForkPolicy = user_model.PrivateForkOrg
		require.NoError(t, check(member, private, OrgPolicyFork, org))
		refused(t, check(member, private, OrgPolicyFork, member), "private_fork_policy")
		refused(t, check(owner, private, OrgPolicyFork, owner), "private_fork_policy")
		refused(t, check(member, private, OrgPolicyFork, user(6)), "private_fork_policy") // another organization
		require.NoError(t, check(member, public, OrgPolicyFork, member))

		org.PrivateForkPolicy = user_model.PrivateForkNone
		refused(t, check(member, private, OrgPolicyFork, org), "private_fork_policy")
		refused(t, check(owner, private, OrgPolicyFork, org), "private_fork_policy")
		require.NoError(t, check(siteAdmin, private, OrgPolicyFork, siteAdmin))
		require.NoError(t, check(member, public, OrgPolicyFork, member))

		// a public repository of a private organization counts as private, as in the fork code
		org.Visibility = structs.VisibleTypePrivate
		refused(t, check(member, public, OrgPolicyFork, member), "private_fork_policy")

		org.PrivateForkPolicy = 7 // unknown: refused
		org.Visibility = structs.VisibleTypePublic
		refused(t, check(member, private, OrgPolicyFork, org), "private_fork_policy")
	})

	t.Run("lookup error fails closed", func(t *testing.T) {
		r := &repo_model.Repository{ID: 9999, OwnerID: 9999}
		err := check(member, r, OrgPolicyDelete, nil)
		require.Error(t, err)
		var e ErrOrgPolicy
		assert.NotErrorAs(t, err, &e)

		allowed, err := AllowedByOrgPolicy(ctx, member, r, OrgPolicyDelete, nil)
		require.Error(t, err)
		assert.False(t, allowed)
	})

	t.Run("AllowedByOrgPolicy", func(t *testing.T) {
		allowed, err := AllowedByOrgPolicy(ctx, member, private, OrgPolicyDelete, nil)
		require.NoError(t, err)
		assert.False(t, allowed)
		allowed, err = AllowedByOrgPolicy(ctx, owner, private, OrgPolicyDelete, nil)
		require.NoError(t, err)
		assert.True(t, allowed)
	})

	t.Run("CanUserForkRepo", func(t *testing.T) {
		defer func() { org.PrivateForkPolicy = user_model.PrivateForkAll }()
		can, err := CanUserForkRepo(ctx, member, private)
		require.NoError(t, err)
		assert.True(t, can)
		// org3 is the only allowed target, and nobody forks a repository into its own owner
		org.PrivateForkPolicy = user_model.PrivateForkOrg
		can, err = CanUserForkRepo(ctx, member, private)
		require.NoError(t, err)
		assert.False(t, can)
		can, err = CanUserForkRepo(ctx, member, public)
		require.NoError(t, err)
		assert.True(t, can)
	})
}
