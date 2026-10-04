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
