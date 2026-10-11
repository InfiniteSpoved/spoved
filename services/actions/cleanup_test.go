// Copyright 2024 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package actions

import (
	"fmt"
	"strings"
	"testing"
	"time"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/storage"
	"forgejo.org/modules/test"
	"forgejo.org/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCleanup(t *testing.T) {
	t.Run("Deletes no longer existing logs", func(t *testing.T) {
		require.NoError(t, unittest.PrepareTestDatabase())

		unittest.AssertSuccessfulInsert(t, &actions_model.ActionTask{ID: 1001, LogExpired: false, LogIndexes: []int64{1, 2, 3, 4}, LogFilename: "does-not-exist", Stopped: timeutil.TimeStamp(1)})

		require.NoError(t, CleanupLogs(db.DefaultContext))

		task := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionTask{ID: 1001})
		assert.Equal(t, "does-not-exist", task.LogFilename)
		assert.True(t, task.LogExpired)
		assert.Nil(t, task.LogIndexes)
	})

	t.Run("Expires tasks without logs", func(t *testing.T) {
		require.NoError(t, unittest.PrepareTestDatabase())

		for i := int64(0); i <= deleteLogBatchSize; i++ {
			task := &actions_model.ActionTask{
				ID:          1000 + i,
				LogExpired:  false,
				LogIndexes:  []int64{},
				LogFilename: "",
				Stopped:     timeutil.TimeStamp(1),
				TokenHash:   fmt.Sprintf("hash-%d", i),
			}
			unittest.AssertSuccessfulInsert(t, task)
		}

		cleanupSucceeded := func(collect *assert.CollectT) {
			require.NoError(t, CleanupLogs(t.Context()))
		}
		require.EventuallyWithT(t, cleanupSucceeded, 10*time.Second, 100*time.Millisecond)

		for i := int64(0); i <= deleteLogBatchSize; i++ {
			task := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionTask{ID: 1000 + i})
			assert.Empty(t, task.LogFilename)
			assert.True(t, task.LogExpired)
			assert.Nil(t, task.LogIndexes)
		}
	})
}

func TestCleanupEphemeralRunners(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	t.Run("Deletes ephemeral runner with successful task", func(t *testing.T) {
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionRunner{ID: 2001, UUID: "2001-uuid", TokenHash: "2001-hash", Ephemeral: true})
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionTask{ID: 2001, RunnerID: 2001, TokenHash: "task-2001-hash", Status: actions_model.StatusSuccess})

		require.NoError(t, CleanupEphemeralRunners(db.DefaultContext))

		unittest.AssertNotExistsBean(t, &actions_model.ActionRunner{ID: 2001})
	})

	t.Run("Deletes ephemeral runner with failed task", func(t *testing.T) {
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionRunner{ID: 2002, UUID: "2002-uuid", TokenHash: "2002-hash", Ephemeral: true})
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionTask{ID: 2002, RunnerID: 2002, TokenHash: "task-2002-hash", Status: actions_model.StatusFailure})

		require.NoError(t, CleanupEphemeralRunners(db.DefaultContext))

		unittest.AssertNotExistsBean(t, &actions_model.ActionRunner{ID: 2002})
	})

	t.Run("Deletes ephemeral runner with cancelled task", func(t *testing.T) {
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionRunner{ID: 2003, UUID: "2003-uuid", TokenHash: "2003-hash", Ephemeral: true})
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionTask{ID: 2003, RunnerID: 2003, TokenHash: "task-2003-hash", Status: actions_model.StatusCancelled})

		require.NoError(t, CleanupEphemeralRunners(db.DefaultContext))

		unittest.AssertNotExistsBean(t, &actions_model.ActionRunner{ID: 2003})
	})

	t.Run("Deletes ephemeral runner with skipped task", func(t *testing.T) {
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionRunner{ID: 2004, UUID: "2004-uuid", TokenHash: "2004-hash", Ephemeral: true})
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionTask{ID: 2004, RunnerID: 2004, TokenHash: "task-2004-hash", Status: actions_model.StatusSkipped})

		require.NoError(t, CleanupEphemeralRunners(db.DefaultContext))

		unittest.AssertNotExistsBean(t, &actions_model.ActionRunner{ID: 2004})
	})

	t.Run("Keeps ephemeral runner with running task", func(t *testing.T) {
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionRunner{ID: 2005, UUID: "2005-uuid", TokenHash: "2005-hash", Ephemeral: true})
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionTask{ID: 2005, RunnerID: 2005, TokenHash: "task-2005-hash", Status: actions_model.StatusRunning})

		require.NoError(t, CleanupEphemeralRunners(db.DefaultContext))

		unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: 2005})
	})

	t.Run("Keeps ephemeral runner with waiting task", func(t *testing.T) {
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionRunner{ID: 2006, UUID: "2006-uuid", TokenHash: "2006-hash", Ephemeral: true})
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionTask{ID: 2006, RunnerID: 2006, TokenHash: "task-2006-hash", Status: actions_model.StatusWaiting})

		require.NoError(t, CleanupEphemeralRunners(db.DefaultContext))

		unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: 2006})
	})

	t.Run("Keeps ephemeral runner with no tasks", func(t *testing.T) {
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionRunner{ID: 2007, UUID: "2007-uuid", TokenHash: "2007-hash", Ephemeral: true})

		require.NoError(t, CleanupEphemeralRunners(db.DefaultContext))

		unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: 2007})
	})

	t.Run("Keeps non-ephemeral runner with completed task", func(t *testing.T) {
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionRunner{ID: 2008, UUID: "2008-uuid", TokenHash: "2008-hash", Ephemeral: false})
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionTask{ID: 2008, RunnerID: 2008, TokenHash: "task-2008-hash", Status: actions_model.StatusSuccess})

		require.NoError(t, CleanupEphemeralRunners(db.DefaultContext))

		unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: 2008})
	})
}

func TestCleanupRuns(t *testing.T) {
	const old = timeutil.TimeStamp(1683636108)
	insertRun := func(t *testing.T, id int64, ref, workflowID string, status actions_model.Status, created timeutil.TimeStamp) {
		t.Helper()
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionRun{ID: id, Index: id, RepoID: 4, OwnerID: 5, Ref: ref, WorkflowID: workflowID, Status: status})
		_, err := db.GetEngine(t.Context()).Exec("UPDATE action_run SET created = ? WHERE id = ?", created, id)
		require.NoError(t, err)
	}

	t.Run("Keeps everything when disabled", func(t *testing.T) {
		require.NoError(t, unittest.PrepareTestDatabase())
		defer test.MockVariableValue(&setting.Actions.RunRetentionDays, 0)()

		insertRun(t, 500001, "refs/heads/feature", "a.yaml", actions_model.StatusSuccess, old)

		require.NoError(t, CleanupRuns(t.Context()))

		unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: 500001})
	})

	t.Run("Deletes old runs", func(t *testing.T) {
		require.NoError(t, unittest.PrepareTestDatabase())
		defer test.MockVariableValue(&setting.Actions.RunRetentionDays, 30)()

		// old, with a job and an artifact
		insertRun(t, 500001, "refs/heads/master", "a.yaml", actions_model.StatusSuccess, old)
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionRunJob{ID: 500001, Handle: "500001", RunID: 500001, RepoID: 4, OwnerID: 5, Status: actions_model.StatusSuccess})
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionArtifact{RunID: 500001, RepoID: 4, OwnerID: 5, ArtifactName: "a", Status: int64(actions_model.ArtifactStatusUploadConfirmed)})
		// latest run of a.yaml on the default branch
		insertRun(t, 500002, "refs/heads/master", "a.yaml", actions_model.StatusSuccess, old)
		// newer, but not on the default branch
		insertRun(t, 500003, "refs/heads/feature", "a.yaml", actions_model.StatusSuccess, old)
		// not done
		insertRun(t, 500004, "refs/heads/feature", "a.yaml", actions_model.StatusRunning, old)
		// done, but DeleteRun fails because its job is still running
		insertRun(t, 500005, "refs/heads/feature", "a.yaml", actions_model.StatusSuccess, old)
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionRunJob{ID: 500005, Handle: "500005", RunID: 500005, RepoID: 4, OwnerID: 5, Status: actions_model.StatusRunning})
		// done and old, but kept
		insertRun(t, 500007, "refs/heads/feature", "a.yaml", actions_model.StatusSuccess, old)
		require.NoError(t, actions_model.SetRunKeep(t.Context(), 500007, true))
		// recent
		insertRun(t, 500006, "refs/heads/feature", "a.yaml", actions_model.StatusSuccess, timeutil.TimeStampNow())
		// more than one batch after the failing run, the last one is the latest of b.yaml
		for id := int64(500100); id <= 500100+deleteRunBatchSize; id++ {
			insertRun(t, id, "refs/heads/master", "b.yaml", actions_model.StatusFailure, old)
		}

		require.NoError(t, CleanupRuns(t.Context()))

		unittest.AssertNotExistsBean(t, &actions_model.ActionRun{ID: 500001})
		unittest.AssertNotExistsBean(t, &actions_model.ActionRunJob{ID: 500001})
		unittest.AssertCount(t, &actions_model.ActionArtifact{RunID: 500001, Status: int64(actions_model.ArtifactStatusPendingDeletion)}, 1)
		unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: 500002})
		unittest.AssertNotExistsBean(t, &actions_model.ActionRun{ID: 500003})
		unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: 500004})
		unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: 500005})
		unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: 500006})
		unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: 500007})
		for id := int64(500100); id < 500100+deleteRunBatchSize; id++ {
			unittest.AssertNotExistsBean(t, &actions_model.ActionRun{ID: id})
		}
		unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: 500100 + deleteRunBatchSize})
	})
}

func TestDeleteRunUnlessKept(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	unittest.AssertSuccessfulInsert(t, &actions_model.ActionRun{ID: 500001, Index: 500001, RepoID: 4, OwnerID: 5, Status: actions_model.StatusSuccess, Keep: true})
	unittest.AssertSuccessfulInsert(t, &actions_model.ActionRunJob{ID: 500001, Handle: "500001", RunID: 500001, RepoID: 4, OwnerID: 5, Status: actions_model.StatusSuccess})

	unittest.AssertSuccessfulInsert(t, &actions_model.ActionTask{ID: 500001, JobID: 500001, Status: actions_model.StatusSuccess, TokenHash: "keep-test", LogInStorage: true, LogFilename: "keep-test/500001.log", LogIndexes: []int64{0}})
	_, err := storage.Actions.Save("keep-test/500001.log", strings.NewReader("log"), -1)
	require.NoError(t, err)

	// a run kept after the scan was selected is spared, with its jobs and its logs
	require.ErrorIs(t, DeleteRunUnlessKept(t.Context(), 500001), ErrRunKept)
	unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: 500001})
	unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 500001})
	unittest.AssertExistsAndLoadBean(t, &actions_model.ActionTask{ID: 500001})
	_, err = storage.Actions.Stat("keep-test/500001.log")
	require.NoError(t, err)

	// the explicit delete still removes it
	require.NoError(t, DeleteRun(t.Context(), 500001))
	unittest.AssertNotExistsBean(t, &actions_model.ActionRun{ID: 500001})
}

// AC2: the retention of a repository comes from its own setting, its organization's, the instance default and the
// instance maximum.
func TestCleanupRunsRetentionSettings(t *testing.T) {
	now := timeutil.TimeStampNow()
	nextID := int64(600000)
	// a run of repo 4 (owned by user5) or repo 3 (owned by the organization org3) that finished `days` days ago
	insert := func(t *testing.T, repoID, ownerID, days int64, keep bool) int64 {
		t.Helper()
		nextID++
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionRun{ID: nextID, Index: nextID, RepoID: repoID, OwnerID: ownerID, Ref: "refs/heads/feature", WorkflowID: "a.yaml", Status: actions_model.StatusSuccess, Keep: keep})
		_, err := db.GetEngine(t.Context()).Exec("UPDATE action_run SET created = ? WHERE id = ?", now.AddDuration(-time.Duration(days)*24*time.Hour), nextID)
		require.NoError(t, err)
		return nextID
	}
	repoDays := func(t *testing.T, repoID, days int64) {
		t.Helper()
		unittest.AssertSuccessfulInsert(t, &repo_model.RepoUnit{RepoID: repoID, Type: unit.TypeActions, Config: &repo_model.ActionsConfig{RunRetentionDays: days}})
	}
	deleted := func(id int64) bool {
		has, err := db.GetEngine(t.Context()).ID(id).Exist(new(actions_model.ActionRun))
		require.NoError(t, err)
		return !has
	}
	setup := func(t *testing.T, def, max int64) {
		t.Helper()
		require.NoError(t, unittest.PrepareTestDatabase())
		t.Cleanup(test.MockVariableValue(&setting.Actions.RunRetentionDays, def))
		t.Cleanup(test.MockVariableValue(&setting.Actions.RunRetentionMaxDays, max))
	}

	t.Run("Repository shortens the instance default", func(t *testing.T) {
		setup(t, 30, 0)
		repoDays(t, 4, 7)
		old, recent, otherRepoOld, otherRepoRecent := insert(t, 4, 5, 10, false), insert(t, 4, 5, 3, false), insert(t, 1, 2, 40, false), insert(t, 1, 2, 20, false)

		require.NoError(t, CleanupRuns(t.Context()))

		assert.True(t, deleted(old))
		assert.False(t, deleted(recent))
		assert.True(t, deleted(otherRepoOld)) // no setting: the instance default
		assert.False(t, deleted(otherRepoRecent))
	})

	t.Run("A repository can't keep runs longer than the instance default", func(t *testing.T) {
		setup(t, 30, 0)
		repoDays(t, 4, 90)
		old, recent := insert(t, 4, 5, 40, false), insert(t, 4, 5, 20, false)

		require.NoError(t, CleanupRuns(t.Context()))

		assert.True(t, deleted(old))
		assert.False(t, deleted(recent))
	})

	t.Run("The organization is a ceiling for its repositories", func(t *testing.T) {
		setup(t, 0, 0)
		require.NoError(t, actions_model.SetOrgRunRetentionDays(t.Context(), 3, 14))
		repoDays(t, 3, 60) // longer than the organization allows
		repoDays(t, 4, 60) // a user's repository is not affected by an organization's setting
		orgOld, orgRecent, userOld := insert(t, 3, 3, 20, false), insert(t, 3, 3, 10, false), insert(t, 4, 5, 20, false)

		require.NoError(t, CleanupRuns(t.Context()))

		assert.True(t, deleted(orgOld))
		assert.False(t, deleted(orgRecent))
		assert.False(t, deleted(userOld))
	})

	t.Run("The organization setting applies to repositories without their own", func(t *testing.T) {
		setup(t, 0, 0)
		require.NoError(t, actions_model.SetOrgRunRetentionDays(t.Context(), 3, 14))
		old, recent := insert(t, 3, 3, 20, false), insert(t, 3, 3, 10, false)

		require.NoError(t, CleanupRuns(t.Context()))

		assert.True(t, deleted(old))
		assert.False(t, deleted(recent))
	})

	t.Run("The instance maximum caps every setting", func(t *testing.T) {
		setup(t, 0, 5)
		require.NoError(t, actions_model.SetOrgRunRetentionDays(t.Context(), 3, 400))
		repoDays(t, 4, 400)
		userOld, userRecent, orgOld, orgRecent := insert(t, 4, 5, 6, false), insert(t, 4, 5, 4, false), insert(t, 3, 3, 6, false), insert(t, 3, 3, 4, false)

		require.NoError(t, CleanupRuns(t.Context()))

		assert.True(t, deleted(userOld))
		assert.False(t, deleted(userRecent))
		assert.True(t, deleted(orgOld))
		assert.False(t, deleted(orgRecent))
	})

	t.Run("An override deletes although the instance keeps runs forever", func(t *testing.T) {
		setup(t, 0, 0)
		repoDays(t, 4, 7)
		old, recent, otherRepo := insert(t, 4, 5, 10, false), insert(t, 4, 5, 3, false), insert(t, 1, 2, 4000, false)

		require.NoError(t, CleanupRuns(t.Context()))

		assert.True(t, deleted(old))
		assert.False(t, deleted(recent))
		assert.False(t, deleted(otherRepo))
	})

	t.Run("Nothing is deleted without any setting", func(t *testing.T) {
		setup(t, 0, 0)
		old := insert(t, 4, 5, 4000, false)

		require.NoError(t, CleanupRuns(t.Context()))

		assert.False(t, deleted(old))
	})

	t.Run("Kept runs are exempt from every limit", func(t *testing.T) {
		setup(t, 30, 5)
		require.NoError(t, actions_model.SetOrgRunRetentionDays(t.Context(), 3, 2))
		repoDays(t, 4, 1)
		keptUser, keptOrg, old := insert(t, 4, 5, 100, true), insert(t, 3, 3, 100, true), insert(t, 4, 5, 100, false)

		require.NoError(t, CleanupRuns(t.Context()))

		assert.False(t, deleted(keptUser))
		assert.False(t, deleted(keptOrg))
		assert.True(t, deleted(old))
	})

	t.Run("Runs of a repository in the trash are left alone", func(t *testing.T) {
		setup(t, 30, 0)
		_, err := db.GetEngine(t.Context()).Exec("UPDATE repository SET trashed_unix = 1 WHERE id = 4")
		require.NoError(t, err)
		trashed, other := insert(t, 4, 5, 100, false), insert(t, 1, 2, 100, false)

		require.NoError(t, CleanupRuns(t.Context()))

		assert.False(t, deleted(trashed))
		assert.True(t, deleted(other))
	})
}
