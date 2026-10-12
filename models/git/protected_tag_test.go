// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package git_test

import (
	"testing"

	asymkey_model "forgejo.org/models/asymkey"
	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	"forgejo.org/models/perm"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsUserAllowed(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	pt := &git_model.ProtectedTag{}
	allowed, err := git_model.IsUserAllowedModifyTag(db.DefaultContext, pt, 1)
	require.NoError(t, err)
	assert.False(t, allowed)

	pt = &git_model.ProtectedTag{
		AllowlistUserIDs: []int64{1},
	}
	allowed, err = git_model.IsUserAllowedModifyTag(db.DefaultContext, pt, 1)
	require.NoError(t, err)
	assert.True(t, allowed)

	allowed, err = git_model.IsUserAllowedModifyTag(db.DefaultContext, pt, 2)
	require.NoError(t, err)
	assert.False(t, allowed)

	pt = &git_model.ProtectedTag{
		AllowlistTeamIDs: []int64{1},
	}
	allowed, err = git_model.IsUserAllowedModifyTag(db.DefaultContext, pt, 1)
	require.NoError(t, err)
	assert.False(t, allowed)

	allowed, err = git_model.IsUserAllowedModifyTag(db.DefaultContext, pt, 2)
	require.NoError(t, err)
	assert.True(t, allowed)

	pt = &git_model.ProtectedTag{
		AllowlistUserIDs: []int64{1},
		AllowlistTeamIDs: []int64{1},
	}
	allowed, err = git_model.IsUserAllowedModifyTag(db.DefaultContext, pt, 1)
	require.NoError(t, err)
	assert.True(t, allowed)

	allowed, err = git_model.IsUserAllowedModifyTag(db.DefaultContext, pt, 2)
	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestIsUserAllowedToControlTag(t *testing.T) {
	cases := []struct {
		name    string
		userid  int64
		allowed bool
	}{
		{
			name:    "test",
			userid:  1,
			allowed: true,
		},
		{
			name:    "test",
			userid:  3,
			allowed: true,
		},
		{
			name:    "gitea",
			userid:  1,
			allowed: true,
		},
		{
			name:    "gitea",
			userid:  3,
			allowed: false,
		},
		{
			name:    "test-gitea",
			userid:  1,
			allowed: true,
		},
		{
			name:    "test-gitea",
			userid:  3,
			allowed: false,
		},
		{
			name:    "gitea-test",
			userid:  1,
			allowed: true,
		},
		{
			name:    "gitea-test",
			userid:  3,
			allowed: true,
		},
		{
			name:    "v-1",
			userid:  1,
			allowed: false,
		},
		{
			name:    "v-1",
			userid:  2,
			allowed: true,
		},
		{
			name:    "release",
			userid:  1,
			allowed: false,
		},
	}

	t.Run("Glob", func(t *testing.T) {
		protectedTags := []*git_model.ProtectedTag{
			{
				NamePattern:      `*gitea`,
				AllowlistUserIDs: []int64{1},
			},
			{
				NamePattern:      `v-*`,
				AllowlistUserIDs: []int64{2},
			},
			{
				NamePattern: "release",
			},
		}

		for n, c := range cases {
			isAllowed, err := git_model.IsUserAllowedToControlTag(db.DefaultContext, protectedTags, c.name, c.userid)
			require.NoError(t, err)
			assert.Equal(t, c.allowed, isAllowed, "case %d: error should match", n)
		}
	})

	t.Run("Regex", func(t *testing.T) {
		protectedTags := []*git_model.ProtectedTag{
			{
				NamePattern:      `/gitea\z/`,
				AllowlistUserIDs: []int64{1},
			},
			{
				NamePattern:      `/\Av-/`,
				AllowlistUserIDs: []int64{2},
			},
			{
				NamePattern: "/release/",
			},
		}

		for n, c := range cases {
			isAllowed, err := git_model.IsUserAllowedToControlTag(db.DefaultContext, protectedTags, c.name, c.userid)
			require.NoError(t, err)
			assert.Equal(t, c.allowed, isAllowed, "case %d: error should match", n)
		}
	})
}

func TestIsDeployKeyAllowedToControlTag(t *testing.T) {
	protectedTags := []*git_model.ProtectedTag{
		{
			NamePattern:           `v-*`,
			AllowlistUserIDs:      []int64{1},
			AllowlistDeployKeyIDs: []int64{10},
		},
		{
			NamePattern:           `/\Arelease-/`,
			AllowlistDeployKeyIDs: []int64{11},
		},
		{
			NamePattern:      "users-only",
			AllowlistUserIDs: []int64{1, 10},
		},
	}

	cases := []struct {
		name    string
		keyID   int64
		allowed bool
	}{
		{"v-1", 10, true},
		{"v-1", 11, false},
		{"v-1", 1, false},
		{"release-1", 11, true},
		{"release-1", 10, false},
		{"users-only", 10, false},
		{"unprotected", 12, true},
	}
	for _, c := range cases {
		isAllowed, err := git_model.IsDeployKeyAllowedToControlTag(protectedTags, c.name, c.keyID)
		require.NoError(t, err)
		assert.Equal(t, c.allowed, isAllowed, "tag %s, key %d", c.name, c.keyID)
	}
}

func TestDeployKeyAllowlists(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	keys := []*asymkey_model.DeployKey{
		{KeyID: 100, RepoID: 1, Name: "write", Mode: perm.AccessModeWrite},
		{KeyID: 101, RepoID: 1, Name: "read", Mode: perm.AccessModeRead},
		{KeyID: 102, RepoID: 2, Name: "other repo", Mode: perm.AccessModeWrite},
	}
	for _, key := range keys {
		require.NoError(t, db.Insert(db.DefaultContext, key))
	}

	t.Run("FilterWriteDeployKeyIDs", func(t *testing.T) {
		repo1 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
		ids, err := git_model.FilterWriteDeployKeyIDs(db.DefaultContext, repo1, []int64{100, 101, 102, 103, 100})
		require.NoError(t, err)
		assert.Equal(t, []int64{100}, ids)

		ids, err = git_model.FilterWriteDeployKeyIDs(db.DefaultContext, repo1, nil)
		require.NoError(t, err)
		assert.Empty(t, ids)
	})

	t.Run("FilterWriteDeployKeyIDs org keys", func(t *testing.T) {
		orgKeys := []*asymkey_model.OrgDeployKey{
			{KeyID: 110, OrgID: 3, Name: "all write", Mode: perm.AccessModeWrite, IncludesAllRepos: true},
			{KeyID: 111, OrgID: 3, Name: "all read", Mode: perm.AccessModeRead, IncludesAllRepos: true},
			{KeyID: 112, OrgID: 3, Name: "repo5 write", Mode: perm.AccessModeWrite},
			{KeyID: 113, OrgID: 17, Name: "other org", Mode: perm.AccessModeWrite, IncludesAllRepos: true},
		}
		for _, key := range orgKeys {
			require.NoError(t, db.Insert(db.DefaultContext, key))
		}
		require.NoError(t, db.Insert(db.DefaultContext, &asymkey_model.OrgDeployKeyRepo{OrgDeployKeyID: orgKeys[2].ID, RepoID: 5}))
		// a selection row pointing at another owner's repository never applies
		require.NoError(t, db.Insert(db.DefaultContext, &asymkey_model.OrgDeployKeyRepo{OrgDeployKeyID: orgKeys[2].ID, RepoID: 1}))

		all := []int64{110, 111, 112, 113}
		repo3 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 3})
		ids, err := git_model.FilterWriteDeployKeyIDs(db.DefaultContext, repo3, all)
		require.NoError(t, err)
		assert.Equal(t, []int64{110}, ids)

		repo5 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 5})
		ids, err = git_model.FilterWriteDeployKeyIDs(db.DefaultContext, repo5, all)
		require.NoError(t, err)
		assert.ElementsMatch(t, []int64{110, 112}, ids)

		repo1 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
		ids, err = git_model.FilterWriteDeployKeyIDs(db.DefaultContext, repo1, all)
		require.NoError(t, err)
		assert.Empty(t, ids)
	})

	t.Run("RemoveDeployKeyIDFromProtectionRules", func(t *testing.T) {
		branch := &git_model.ProtectedBranch{
			RepoID: 1, RuleName: "main", WhitelistDeployKeyIDs: []int64{100, 104},
			DeleteAllowlistDeployKeyIDs: []int64{100}, ForcePushAllowlistDeployKeyIDs: []int64{104, 100},
		}
		otherBranch := &git_model.ProtectedBranch{RepoID: 2, RuleName: "main", WhitelistDeployKeyIDs: []int64{100}}
		tag := &git_model.ProtectedTag{RepoID: 1, NamePattern: "v*", AllowlistDeployKeyIDs: []int64{104, 100}}
		require.NoError(t, db.Insert(db.DefaultContext, branch))
		require.NoError(t, db.Insert(db.DefaultContext, otherBranch))
		require.NoError(t, git_model.InsertProtectedTag(db.DefaultContext, tag))

		require.NoError(t, git_model.RemoveDeployKeyIDFromProtectionRules(db.DefaultContext, 1, 100))

		branch = unittest.AssertExistsAndLoadBean(t, &git_model.ProtectedBranch{ID: branch.ID})
		assert.Equal(t, []int64{104}, branch.WhitelistDeployKeyIDs)
		assert.Empty(t, branch.DeleteAllowlistDeployKeyIDs)
		assert.Equal(t, []int64{104}, branch.ForcePushAllowlistDeployKeyIDs)
		tag = unittest.AssertExistsAndLoadBean(t, &git_model.ProtectedTag{ID: tag.ID})
		assert.Equal(t, []int64{104}, tag.AllowlistDeployKeyIDs)
		// rules of other repositories are left alone
		otherBranch = unittest.AssertExistsAndLoadBean(t, &git_model.ProtectedBranch{ID: otherBranch.ID})
		assert.Equal(t, []int64{100}, otherBranch.WhitelistDeployKeyIDs)
	})
}

func TestProtectedBranchDeleteForcePushAllowlists(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
	user5 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 5})

	t.Run("empty lists block everyone", func(t *testing.T) {
		rule := &git_model.ProtectedBranch{}
		assert.False(t, rule.CanUserDelete(db.DefaultContext, user2))
		assert.False(t, rule.CanUserForcePush(db.DefaultContext, user2))
		assert.False(t, rule.CanDeployKeyDelete(1))
		assert.False(t, rule.CanDeployKeyForcePush(1))
	})

	t.Run("lists are separate", func(t *testing.T) {
		rule := &git_model.ProtectedBranch{
			DeleteAllowlistUserIDs:         []int64{2},
			DeleteAllowlistDeployKeyIDs:    []int64{10},
			ForcePushAllowlistTeamIDs:      []int64{2}, // team 2 has users 2 and 4
			ForcePushAllowlistDeployKeyIDs: []int64{11},
		}
		assert.True(t, rule.CanUserDelete(db.DefaultContext, user2))
		assert.False(t, rule.CanUserDelete(db.DefaultContext, user4))
		assert.True(t, rule.CanUserForcePush(db.DefaultContext, user2))
		assert.True(t, rule.CanUserForcePush(db.DefaultContext, user4))
		assert.False(t, rule.CanUserForcePush(db.DefaultContext, user5))
		assert.True(t, rule.CanDeployKeyDelete(10))
		assert.False(t, rule.CanDeployKeyDelete(11))
		assert.True(t, rule.CanDeployKeyForcePush(11))
		assert.False(t, rule.CanDeployKeyForcePush(10))
	})

	t.Run("allowing push does not allow delete or force push", func(t *testing.T) {
		rule := &git_model.ProtectedBranch{CanPush: true, WhitelistDeployKeys: true}
		assert.False(t, rule.CanUserDelete(db.DefaultContext, user2))
		assert.False(t, rule.CanDeployKeyForcePush(1))
	})

	t.Run("RemoveUserIDFromProtectedBranch", func(t *testing.T) {
		p := &git_model.ProtectedBranch{RepoID: 1, RuleName: "rm-user", DeleteAllowlistUserIDs: []int64{2, 4}, ForcePushAllowlistUserIDs: []int64{2}}
		require.NoError(t, db.Insert(db.DefaultContext, p))
		require.NoError(t, git_model.RemoveUserIDFromProtectedBranch(db.DefaultContext, p, 2))
		p = unittest.AssertExistsAndLoadBean(t, &git_model.ProtectedBranch{ID: p.ID})
		assert.Equal(t, []int64{4}, p.DeleteAllowlistUserIDs)
		assert.Empty(t, p.ForcePushAllowlistUserIDs)
	})

	t.Run("RemoveTeamIDFromProtectedBranch", func(t *testing.T) {
		p := &git_model.ProtectedBranch{RepoID: 3, RuleName: "rm-team", DeleteAllowlistTeamIDs: []int64{1}, ForcePushAllowlistTeamIDs: []int64{1, 2}}
		require.NoError(t, db.Insert(db.DefaultContext, p))
		require.NoError(t, git_model.RemoveTeamIDFromProtectedBranch(db.DefaultContext, p, 1))
		p = unittest.AssertExistsAndLoadBean(t, &git_model.ProtectedBranch{ID: p.ID})
		assert.Empty(t, p.DeleteAllowlistTeamIDs)
		assert.Equal(t, []int64{2}, p.ForcePushAllowlistTeamIDs)
	})

	t.Run("UpdateProtectBranch filters the lists", func(t *testing.T) {
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 3}) // org3/repo3
		key := &asymkey_model.DeployKey{KeyID: 200, RepoID: 3, Name: "write", Mode: perm.AccessModeWrite}
		require.NoError(t, db.Insert(db.DefaultContext, key))

		p := &git_model.ProtectedBranch{RepoID: repo.ID, RuleName: "update"}
		require.NoError(t, git_model.UpdateProtectBranch(db.DefaultContext, repo, p, git_model.WhitelistOptions{
			DeleteUserIDs:         []int64{2, 5}, // user 5 can't write to repo3
			DeleteTeamIDs:         []int64{1, 9999},
			DeleteDeployKeyIDs:    []int64{200, 201},
			ForcePushUserIDs:      []int64{2},
			ForcePushDeployKeyIDs: []int64{200},
		}))
		p = unittest.AssertExistsAndLoadBean(t, &git_model.ProtectedBranch{ID: p.ID})
		assert.Equal(t, []int64{2}, p.DeleteAllowlistUserIDs)
		assert.Equal(t, []int64{1}, p.DeleteAllowlistTeamIDs)
		assert.Equal(t, []int64{200}, p.DeleteAllowlistDeployKeyIDs)
		assert.Equal(t, []int64{2}, p.ForcePushAllowlistUserIDs)
		assert.Equal(t, []int64{200}, p.ForcePushAllowlistDeployKeyIDs)
	})
}
