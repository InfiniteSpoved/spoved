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
