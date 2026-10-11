// Copyright 2026 Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package automerge

import (
	"testing"
	"time"

	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	"forgejo.org/models/perm"
	access_model "forgejo.org/models/perm/access"
	pull_model "forgejo.org/models/pull"
	"forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"
	pull_service "forgejo.org/services/pull"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRemoveScheduledAutoMerge(t *testing.T) {
	defer unittest.OverrideFixtures("services/automerge/fixtures/TestRemoveScheduledAutoMerge")()
	require.NoError(t, unittest.PrepareTestDatabase())

	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	user5 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 5})
	pull1 := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: 1})
	pull2 := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: 2})

	t.Run("No automerge", func(t *testing.T) {
		err := RemoveScheduledAutoMerge(t.Context(), user5, pull2, access_model.Permission{})
		require.ErrorIs(t, err, db.ErrNotExist{Resource: "auto_merge", ID: 2})
	})

	t.Run("No permission", func(t *testing.T) {
		err := RemoveScheduledAutoMerge(t.Context(), user5, pull1, access_model.Permission{})
		require.ErrorIs(t, err, util.ErrPermissionDenied)

		err = RemoveScheduledAutoMerge(t.Context(), user5, pull1, access_model.Permission{UnitsMode: map[unit.Type]perm.AccessMode{
			unit.TypePullRequests: perm.AccessModeRead,
		}})
		require.ErrorIs(t, err, util.ErrPermissionDenied)
	})

	t.Run("Normal", func(t *testing.T) {
		err := RemoveScheduledAutoMerge(t.Context(), user2, pull1, access_model.Permission{UnitsMode: map[unit.Type]perm.AccessMode{
			unit.TypePullRequests: perm.AccessModeWrite,
		}})
		require.NoError(t, err)

		unittest.AssertExistsIf(t, false, &pull_model.AutoMerge{PullID: pull1.ID})
		unittest.AssertExistsIf(t, true, &issues_model.Comment{IssueID: pull1.IssueID, Type: issues_model.CommentTypePRUnScheduledToAutoMerge})
	})
}

func TestChangeScheduledAutoMerge(t *testing.T) {
	defer unittest.OverrideFixtures("services/automerge/fixtures/TestChangeScheduledAutoMerge")()
	require.NoError(t, unittest.PrepareTestDatabase())
	defer timeutil.MockUnset()
	timeutil.MockSet(time.Unix(1771800000, 0))

	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	user5 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 5})
	pull1 := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: 1})
	pull2 := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: 2})
	write := access_model.Permission{UnitsMode: map[unit.Type]perm.AccessMode{unit.TypePullRequests: perm.AccessModeWrite}}
	newAfter, newGiveUp := timeutil.TimeStamp(1771807200), timeutil.TimeStamp(1771950000)
	countEntries := func(typ issues_model.CommentType) int {
		return unittest.GetCount(t, &issues_model.Comment{IssueID: pull1.IssueID, Type: typ})
	}

	t.Run("No automerge", func(t *testing.T) {
		err := ChangeScheduledAutoMerge(t.Context(), user2, pull2, write, newAfter, 0)
		require.ErrorIs(t, err, db.ErrNotExist{Resource: "auto_merge", ID: 2})
	})

	t.Run("No permission", func(t *testing.T) {
		err := ChangeScheduledAutoMerge(t.Context(), user5, pull1, access_model.Permission{}, newAfter, 0)
		require.ErrorIs(t, err, util.ErrPermissionDenied)
		am := unittest.AssertExistsAndLoadBean(t, &pull_model.AutoMerge{ID: 1001})
		assert.Equal(t, timeutil.TimeStamp(1771803600), am.ScheduledAfterUnix)
		assert.Zero(t, countEntries(issues_model.CommentTypePRAutoMergeTimeChanged))
	})

	t.Run("Normal", func(t *testing.T) {
		require.NoError(t, ChangeScheduledAutoMerge(t.Context(), user2, pull1, write, newAfter, newGiveUp))

		am := unittest.AssertExistsAndLoadBean(t, &pull_model.AutoMerge{ID: 1001})
		assert.Equal(t, newAfter, am.ScheduledAfterUnix)
		assert.Equal(t, newGiveUp, am.GiveUpAfterUnix)
		// nothing else changes: not the pinned head, the style, the message, the branch deletion or who scheduled it
		assert.Equal(t, "0123456789abcdef0123456789abcdef01234567", am.HeadCommitID)
		assert.Equal(t, "squash", string(am.MergeStyle))
		assert.Equal(t, "Scheduled message", am.Message)
		assert.True(t, am.DeleteBranchAfterMerge)
		assert.EqualValues(t, 2, am.DoerID)

		assert.Equal(t, 1, countEntries(issues_model.CommentTypePRAutoMergeTimeChanged))
		c := unittest.AssertExistsAndLoadBean(t, &issues_model.Comment{IssueID: pull1.IssueID, Type: issues_model.CommentTypePRAutoMergeTimeChanged})
		assert.EqualValues(t, 2, c.PosterID)
		assert.Equal(t, issues_model.AutoMergeInfo{Old: 1771803600, New: int64(newAfter), GiveUp: int64(newGiveUp)}, c.AutoMergeInfo())
	})

	t.Run("A merger who did not schedule it may change it", func(t *testing.T) {
		codeWrite := access_model.Permission{UnitsMode: map[unit.Type]perm.AccessMode{unit.TypeCode: perm.AccessModeWrite}}
		require.NoError(t, ChangeScheduledAutoMerge(t.Context(), user5, pull1, codeWrite, newAfter+60, 0))
		assert.Equal(t, 2, countEntries(issues_model.CommentTypePRAutoMergeTimeChanged))
		am := unittest.AssertExistsAndLoadBean(t, &pull_model.AutoMerge{ID: 1001})
		assert.EqualValues(t, 2, am.DoerID)
	})
}

func TestCancelAutoMergeReason(t *testing.T) {
	defer unittest.OverrideFixtures("services/automerge/fixtures/TestRemoveScheduledAutoMerge")()
	require.NoError(t, unittest.PrepareTestDatabase())

	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	pull1 := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: 1})
	am := unittest.AssertExistsAndLoadBean(t, &pull_model.AutoMerge{PullID: 1})

	require.NoError(t, pull_service.CancelAutoMerge(t.Context(), pull1, am, user2, "permission"))
	c := unittest.AssertExistsAndLoadBean(t, &issues_model.Comment{IssueID: pull1.IssueID, Type: issues_model.CommentTypePRUnScheduledToAutoMerge})
	assert.EqualValues(t, 2, c.PosterID)
	assert.Equal(t, "permission", c.AutoMergeInfo().Reason)

	// already gone: no second entry
	require.NoError(t, pull_service.CancelAutoMerge(t.Context(), pull1, am, user2, "permission"))
	assert.Equal(t, 1, unittest.GetCount(t, &issues_model.Comment{IssueID: pull1.IssueID, Type: issues_model.CommentTypePRUnScheduledToAutoMerge}))
}

func TestSkipAutoMerge(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	pull1 := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: 1})
	am := &pull_model.AutoMerge{PullID: 1, DoerID: 2, Doer: unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})}
	count := func() int {
		return unittest.GetCount(t, &issues_model.Comment{IssueID: pull1.IssueID, Type: issues_model.CommentTypePRAutoMergeSkipped})
	}

	// repeated evaluations of the same state make one entry, posted by the scheduler
	for range 3 {
		skipAutoMerge(t.Context(), pull1, am, "checks_failed", "abc")
	}
	assert.Equal(t, 1, count())
	c := unittest.AssertExistsAndLoadBean(t, &issues_model.Comment{IssueID: pull1.IssueID, Type: issues_model.CommentTypePRAutoMergeSkipped})
	assert.EqualValues(t, 2, c.PosterID)
	assert.Equal(t, issues_model.AutoMergeInfo{Reason: "checks_failed", SHA: "abc"}, c.AutoMergeInfo())

	// a new reason or a new head is a new state
	skipAutoMerge(t.Context(), pull1, am, "conflict", "abc")
	skipAutoMerge(t.Context(), pull1, am, "conflict", "def")
	assert.Equal(t, 3, count())
}

func TestTakeDueScheduledAutoMerges(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	defer timeutil.MockUnset()
	timeutil.MockSet(time.Unix(1771800000, 0))
	now := timeutil.TimeStampNow()

	for pullID, after := range map[int64]timeutil.TimeStamp{1: 0, 2: now + 3600, 3: now - 60, 4: now} {
		require.NoError(t, db.Insert(t.Context(), &pull_model.AutoMerge{PullID: pullID, DoerID: 2, ScheduledAfterUnix: after}))
	}

	pullIDs, err := pull_model.TakeDueScheduledAutoMerges(t.Context(), now)
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{3, 4}, pullIDs)

	for pullID, after := range map[int64]timeutil.TimeStamp{1: 0, 2: now + 3600, 3: 0, 4: 0} {
		am := unittest.AssertExistsAndLoadBean(t, &pull_model.AutoMerge{PullID: pullID})
		assert.Equal(t, after, am.ScheduledAfterUnix, "pull %d", pullID)
	}

	// already taken
	pullIDs, err = pull_model.TakeDueScheduledAutoMerges(t.Context(), now)
	require.NoError(t, err)
	assert.Empty(t, pullIDs)
}

func TestTakeExpiredScheduledAutoMerges(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	defer timeutil.MockUnset()
	timeutil.MockSet(time.Unix(1771800000, 0))
	now := timeutil.TimeStampNow()

	for pullID, giveUp := range map[int64]timeutil.TimeStamp{1: 0, 2: now + 3600, 3: now - 60, 4: now} {
		require.NoError(t, db.Insert(t.Context(), &pull_model.AutoMerge{PullID: pullID, DoerID: 2, GiveUpAfterUnix: giveUp}))
	}

	expired, err := pull_model.TakeExpiredScheduledAutoMerges(t.Context(), now)
	require.NoError(t, err)
	pullIDs := make([]int64, 0, len(expired))
	for _, am := range expired {
		pullIDs = append(pullIDs, am.PullID)
	}
	assert.ElementsMatch(t, []int64{3, 4}, pullIDs)

	unittest.AssertExistsIf(t, true, &pull_model.AutoMerge{PullID: 1})
	unittest.AssertExistsIf(t, true, &pull_model.AutoMerge{PullID: 2})
	unittest.AssertExistsIf(t, false, &pull_model.AutoMerge{PullID: 3})
	unittest.AssertExistsIf(t, false, &pull_model.AutoMerge{PullID: 4})

	// already taken
	expired, err = pull_model.TakeExpiredScheduledAutoMerges(t.Context(), now)
	require.NoError(t, err)
	assert.Empty(t, expired)
}

func TestDeleteAutoMergeByID(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	am := &pull_model.AutoMerge{PullID: 1, DoerID: 2}
	require.NoError(t, db.Insert(t.Context(), am))

	deleted, err := pull_model.DeleteAutoMergeByID(t.Context(), am.ID)
	require.NoError(t, err)
	assert.True(t, deleted)
	unittest.AssertNotExistsBean(t, &pull_model.AutoMerge{PullID: 1})

	deleted, err = pull_model.DeleteAutoMergeByID(t.Context(), am.ID)
	require.NoError(t, err)
	assert.False(t, deleted)
}
