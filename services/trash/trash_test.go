// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package trash

import (
	"testing"
	"time"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"
	"forgejo.org/modules/timeutil"

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
	require.NoError(t, PurgeExpired(t.Context()))

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

	require.NoError(t, Restore(t.Context(), owner, repo.ID))
	require.ErrorIs(t, Restore(t.Context(), owner, repo.ID), ErrTrashConflict)

	restored, err := repo_model.GetRepositoryByID(t.Context(), repo.ID)
	require.NoError(t, err)
	assert.False(t, restored.IsTrashed())
	assert.Equal(t, owner.NumRepos, unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: owner.ID}).NumRepos)
}
