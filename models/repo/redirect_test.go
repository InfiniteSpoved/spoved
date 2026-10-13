// Copyright 2017 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo_test

import (
	"testing"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRedirect(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	require.NoError(t, repo_model.NewRedirect(db.DefaultContext, repo.OwnerID, 0, repo.ID, repo.Name))
	unittest.AssertExistsAndLoadBean(t, &repo_model.Redirect{OwnerID: repo.OwnerID, LowerName: repo.LowerName, RedirectRepoID: repo.ID})
	unittest.AssertExistsAndLoadBean(t, &repo_model.Redirect{OwnerID: repo.OwnerID, LowerName: "oldrepo1", RedirectRepoID: repo.ID})

	// a redirect in a project has its own key, and the root lookup never finds it
	require.NoError(t, repo_model.NewRedirect(db.DefaultContext, 3, 7, 2, "api"))
	_, err := repo_model.GetRedirect(db.DefaultContext, 3, 0, "api")
	require.True(t, repo_model.IsErrRedirectNotExist(err))
	id, err := repo_model.GetRedirect(db.DefaultContext, 3, 7, "API")
	require.NoError(t, err)
	assert.EqualValues(t, 2, id)
}

func TestTakeRedirectPath(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	org17 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 17})

	// a user's redirect is replaced, as upstream
	require.NoError(t, repo_model.TakeRedirectPath(ctx, user2, 0, "OldRepo1", 0))
	unittest.AssertNotExistsBean(t, &repo_model.Redirect{OwnerID: user2.ID, LowerName: "oldrepo1"})

	// an organization's redirect refuses another repository, and is replaced for its own repository
	require.True(t, repo_model.IsErrRepoAlreadyExist(repo_model.TakeRedirectPath(ctx, org17, 0, "oldrepo24", 0)))
	require.True(t, repo_model.IsErrRepoAlreadyExist(repo_model.TakeRedirectPath(ctx, org17, 0, "oldrepo24", 25)))
	unittest.AssertExistsAndLoadBean(t, &repo_model.Redirect{OwnerID: org17.ID, LowerName: "oldrepo24"})
	require.NoError(t, repo_model.TakeRedirectPath(ctx, org17, 5, "oldrepo24", 0), "another project")
	require.NoError(t, repo_model.TakeRedirectPath(ctx, org17, 0, "oldrepo24", 24))
	unittest.AssertNotExistsBean(t, &repo_model.Redirect{OwnerID: org17.ID, LowerName: "oldrepo24"})
}
