// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package trash

import (
	"testing"
	"time"

	"forgejo.org/models/db"
	"forgejo.org/models/organization"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"
	"forgejo.org/modules/timeutil"
	org_service "forgejo.org/services/org"
	repo_service "forgejo.org/services/repository"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setTrashed(t *testing.T, id, unix int64) {
	t.Helper()
	_, err := db.GetEngine(t.Context()).Exec("UPDATE `repository` SET trashed_unix = ? WHERE id = ?", unix, id)
	require.NoError(t, err)
}

// AC13/AC14: an expired item is purged, a younger one is left, and a purge claimed long ago (a crash) is retried
// while a fresh claim is left alone.
func TestPurgeExpired(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	defer test.MockVariableValue(&setting.Trash.RetentionDays, 30)()
	now := time.Now().Unix()
	day := int64(24 * 3600)

	setTrashed(t, 1, now-31*day)             // expired
	setTrashed(t, 2, now-29*day)             // not yet
	setTrashed(t, 3, -(now - int64(7*3600))) // purge claimed seven hours ago, never finished
	setTrashed(t, 4, -(now - int64(60)))     // purge claimed a minute ago, still running
	require.NoError(t, PurgeExpired(t.Context(), unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})))

	unittest.AssertNotExistsBean(t, &repo_model.Repository{ID: 1})
	unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 2})
	unittest.AssertNotExistsBean(t, &repo_model.Repository{ID: 3})
	unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 4})
}

// AC6: restoring brings the repository back and counts it for its owner again.
func TestRestore(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: repo.OwnerID})
	setTrashed(t, repo.ID, timeutil.TimeStampNow().AsTime().Unix())
	_, err := db.GetEngine(t.Context()).Exec("UPDATE `user` SET num_repos = num_repos - 1 WHERE id = ?", owner.ID)
	require.NoError(t, err)

	require.NoError(t, RestoreRepo(t.Context(), owner, repo.ID, RestoreOptions{}))
	require.ErrorIs(t, RestoreRepo(t.Context(), owner, repo.ID, RestoreOptions{}), ErrTrashConflict)

	restored, err := repo_model.GetRepositoryByID(t.Context(), repo.ID)
	require.NoError(t, err)
	assert.False(t, restored.IsTrashed())
	assert.Equal(t, owner.NumRepos, unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: owner.ID}).NumRepos)
}

// Restore puts a repository back at its place by the name it has, never taking a project's name, and purging an
// organization removes its projects and every name claim.
func TestRepoGroupRestoreAndOrgPurge(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	org3 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 3})
	pay, err := repo_model.CreateRepoGroup(ctx, org3, "pay", "")
	require.NoError(t, err)
	nested, err := repo_service.CreateRepositoryDirectly(ctx, doer, org3, repo_service.CreateRepoOptions{Name: "api", GroupID: pay.ID})
	require.NoError(t, err)

	root, err := repo_service.CreateRepositoryDirectly(ctx, doer, org3, repo_service.CreateRepoOptions{Name: "web"})
	require.NoError(t, err)
	require.NoError(t, repo_service.TrashRepository(ctx, doer, root))
	err = RestoreRepo(ctx, doer, root.ID, RestoreOptions{NewName: "pay"})
	require.True(t, repo_model.IsErrRepoAlreadyExist(err), "%v", err)
	require.NoError(t, RestoreRepo(ctx, doer, root.ID, RestoreOptions{}))
	assert.Equal(t, "web", unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: root.ID}).Name)

	require.NoError(t, repo_service.TrashRepository(ctx, doer, nested))
	require.NoError(t, RestoreRepo(ctx, doer, nested.ID, RestoreOptions{}))
	restored, err := repo_model.GetRepositoryByPath(ctx, org3.ID, "pay", "api")
	require.NoError(t, err)
	assert.Equal(t, nested.ID, restored.ID)
	unittest.CheckConsistencyFor(t, &user_model.User{}, &repo_model.Repository{})

	require.NoError(t, org_service.TrashOrganization(ctx, doer, organization.OrgFromUser(org3)))
	unittest.AssertExistsAndLoadBean(t, &repo_model.RepoGroup{ID: pay.ID}) // the trash takes the projects along
	require.NoError(t, PurgeOrg(ctx, doer, org3.ID))
	unittest.AssertNotExistsBean(t, &repo_model.RepoGroup{OwnerID: org3.ID})
	unittest.AssertNotExistsBean(t, &repo_model.RepoGroupNameClaim{OwnerID: org3.ID})
	unittest.CheckRepoGroupNameClaims(t)
}
