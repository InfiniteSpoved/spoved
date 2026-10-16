// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo_test

import (
	"testing"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/util"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckCreateRepository(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	t.Run("Success", func(t *testing.T) {
		user := &user_model.User{MaxRepoCreation: 1}

		err := repo_model.CheckCreateRepository(db.DefaultContext, user, user, "testName")

		require.NoError(t, err)
	})

	t.Run("AdminIgnoresRepoLimit", func(t *testing.T) {
		user := &user_model.User{MaxRepoCreation: 0, IsAdmin: true}

		err := repo_model.CheckCreateRepository(db.DefaultContext, user, user, "testName")

		require.NoError(t, err)
	})

	t.Run("RepoLimitReached", func(t *testing.T) {
		user := &user_model.User{MaxRepoCreation: 0}

		err := repo_model.CheckCreateRepository(db.DefaultContext, user, user, "testName")

		require.ErrorIs(t, err, repo_model.ErrReachLimitOfRepo{})
	})

	t.Run("UnusableRepoName", func(t *testing.T) {
		user := &user_model.User{MaxRepoCreation: 1}

		err := repo_model.CheckCreateRepository(db.DefaultContext, user, user, "testName/")

		require.ErrorIs(t, err, db.ErrNameCharsNotAllowed{Name: "testName/"})
	})

	t.Run("RepoAlreadyExists", func(t *testing.T) {
		unittest.AssertExistsIf(t, true, &repo_model.Repository{Name: "repo1"})
		user := &user_model.User{ID: 2, MaxRepoCreation: 2} // the owner of repo1

		err := repo_model.CheckCreateRepository(db.DefaultContext, user, user, "repo1")

		require.ErrorIs(t, err, repo_model.ErrRepoAlreadyExist{Name: "repo1"})
	})

	t.Run("RepoDirAlreadyExists", func(t *testing.T) {
		db.DeleteByBean(db.DefaultContext, &repo_model.Repository{Name: "repo1"})
		user := &user_model.User{MaxRepoCreation: 2, Name: "user2"}

		exists, _ := util.IsExist(repo_model.RepoPath("user2", "repo1"))
		assert.True(t, exists)

		err := repo_model.CheckCreateRepository(db.DefaultContext, user, user, "repo1")

		require.ErrorIs(t, err, repo_model.ErrRepoAlreadyExist{Name: "repo1", Uname: "user2"})
	})
}

func TestCheckCreateRepositoryInGroup(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := db.DefaultContext
	admin := &user_model.User{IsAdmin: true}
	org3 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 3})
	group, err := repo_model.CreateRepoGroup(ctx, org3, "chk", "")
	require.NoError(t, err)
	require.NoError(t, db.Insert(ctx, &repo_model.Repository{OwnerID: org3.ID, OwnerName: org3.Name, GroupID: group.ID, GroupName: group.Name, Name: "taken", LowerName: "taken"}))

	require.NoError(t, repo_model.CheckCreateRepositoryInGroup(ctx, admin, org3, group.ID, "free"))
	require.NoError(t, repo_model.CheckCreateRepositoryInGroup(ctx, admin, org3, 0, "free"))
	assert.True(t, repo_model.IsErrRepoAlreadyExist(repo_model.CheckCreateRepositoryInGroup(ctx, admin, org3, group.ID, "TAKEN")))
	require.NoError(t, repo_model.CheckCreateRepositoryInGroup(ctx, admin, org3, 0, "taken")) // the root is another namespace
	require.ErrorIs(t, repo_model.CheckCreateRepositoryInGroup(ctx, admin, org3, group.ID+1000, "free"), repo_model.ErrRepoGroupNotExist)
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	require.ErrorIs(t, repo_model.CheckCreateRepositoryInGroup(ctx, admin, user2, group.ID, "free"), repo_model.ErrRepoGroupNotExist) // another owner's project
}
