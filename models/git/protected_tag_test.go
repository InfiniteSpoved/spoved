// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package git_test

import (
	"testing"

	asymkey_model "forgejo.org/models/asymkey"
	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	"forgejo.org/models/perm"
	"forgejo.org/models/unittest"

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
		ids, err := git_model.FilterWriteDeployKeyIDs(db.DefaultContext, 1, []int64{100, 101, 102, 103, 100})
		require.NoError(t, err)
		assert.Equal(t, []int64{100}, ids)

		ids, err = git_model.FilterWriteDeployKeyIDs(db.DefaultContext, 1, nil)
		require.NoError(t, err)
		assert.Empty(t, ids)
	})

	t.Run("RemoveDeployKeyIDFromProtectionRules", func(t *testing.T) {
		branch := &git_model.ProtectedBranch{RepoID: 1, RuleName: "main", WhitelistDeployKeyIDs: []int64{100, 104}}
		otherBranch := &git_model.ProtectedBranch{RepoID: 2, RuleName: "main", WhitelistDeployKeyIDs: []int64{100}}
		tag := &git_model.ProtectedTag{RepoID: 1, NamePattern: "v*", AllowlistDeployKeyIDs: []int64{104, 100}}
		require.NoError(t, db.Insert(db.DefaultContext, branch))
		require.NoError(t, db.Insert(db.DefaultContext, otherBranch))
		require.NoError(t, git_model.InsertProtectedTag(db.DefaultContext, tag))

		require.NoError(t, git_model.RemoveDeployKeyIDFromProtectionRules(db.DefaultContext, 1, 100))

		branch = unittest.AssertExistsAndLoadBean(t, &git_model.ProtectedBranch{ID: branch.ID})
		assert.Equal(t, []int64{104}, branch.WhitelistDeployKeyIDs)
		tag = unittest.AssertExistsAndLoadBean(t, &git_model.ProtectedTag{ID: tag.ID})
		assert.Equal(t, []int64{104}, tag.AllowlistDeployKeyIDs)
		// rules of other repositories are left alone
		otherBranch = unittest.AssertExistsAndLoadBean(t, &git_model.ProtectedBranch{ID: otherBranch.ID})
		assert.Equal(t, []int64{100}, otherBranch.WhitelistDeployKeyIDs)
	})
}
