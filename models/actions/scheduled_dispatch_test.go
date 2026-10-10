// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package actions

import (
	"sync"
	"testing"
	"time"

	"forgejo.org/models/db"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newScheduledDispatch(t *testing.T, repoID int64, runAt timeutil.TimeStamp) *ActionScheduledDispatch {
	t.Helper()
	sd := &ActionScheduledDispatch{RepoID: repoID, WorkflowID: "a.yml", Ref: "main", Inputs: `{"k":"v"}`, DoerID: 2, RunAt: runAt}
	require.NoError(t, InsertScheduledDispatch(t.Context(), sd))
	return sd
}

func TestScheduledDispatchCRUD(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	a := newScheduledDispatch(t, 1, 100)
	b := newScheduledDispatch(t, 1, 200)
	newScheduledDispatch(t, 2, 300)

	list, total, err := ListScheduledDispatches(ctx, 1, db.ListOptions{})
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	require.Len(t, list, 2)
	assert.Equal(t, a.ID, list[0].ID, "ordered by time")

	n, err := CountPendingScheduledDispatches(ctx, 1)
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)

	_, err = GetScheduledDispatch(ctx, 2, a.ID)
	require.ErrorIs(t, err, ErrScheduledDispatchNotFound, "ids are scoped by repository")
	got, err := GetScheduledDispatch(ctx, 1, a.ID)
	require.NoError(t, err)
	assert.JSONEq(t, `{"k":"v"}`, got.Inputs)

	require.NoError(t, UpdateScheduledDispatchRunAt(ctx, 1, a.ID, 500))
	got, err = GetScheduledDispatch(ctx, 1, a.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 500, got.RunAt)
	require.ErrorIs(t, UpdateScheduledDispatchRunAt(ctx, 2, a.ID, 600), ErrScheduledDispatchNotFound)

	require.NoError(t, CancelScheduledDispatch(ctx, 1, b.ID, "no_permission"))
	require.ErrorIs(t, UpdateScheduledDispatchRunAt(ctx, 1, b.ID, 600), ErrScheduledDispatchNotFound, "only a pending row changes")
	require.ErrorIs(t, CancelScheduledDispatch(ctx, 1, b.ID, "x"), ErrScheduledDispatchNotFound)
	n, err = CountPendingScheduledDispatches(ctx, 1)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	got, err = GetScheduledDispatch(ctx, 1, b.ID)
	require.NoError(t, err)
	assert.Equal(t, ScheduledDispatchCancelled, got.Status)
	assert.Equal(t, "no_permission", got.CancelReason)

	require.ErrorIs(t, DeleteScheduledDispatch(ctx, 2, b.ID), ErrScheduledDispatchNotFound)
	require.NoError(t, DeleteScheduledDispatch(ctx, 1, b.ID))

	require.NoError(t, DeleteScheduledDispatchesByRepo(ctx, 1))
	_, total, err = ListScheduledDispatches(ctx, 1, db.ListOptions{})
	require.NoError(t, err)
	assert.Zero(t, total)
	_, total, err = ListScheduledDispatches(ctx, 2, db.ListOptions{})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
}

func TestTakeDueScheduledDispatches(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()
	due := newScheduledDispatch(t, 1, 100)
	newScheduledDispatch(t, 1, 300)

	taken, err := TakeDueScheduledDispatches(ctx, 99)
	require.NoError(t, err)
	assert.Empty(t, taken)

	taken, err = TakeDueScheduledDispatches(ctx, 200)
	require.NoError(t, err)
	require.Len(t, taken, 1)
	assert.Equal(t, due.ID, taken[0].ID)

	taken, err = TakeDueScheduledDispatches(ctx, 200)
	require.NoError(t, err)
	assert.Empty(t, taken, "a row is returned once")

	// claimed rows can't be cancelled or moved
	require.ErrorIs(t, CancelScheduledDispatch(ctx, 1, due.ID, "x"), ErrScheduledDispatchNotFound)
}

func TestTakeDueScheduledDispatchesConcurrent(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	for range 5 {
		newScheduledDispatch(t, 1, 100)
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	seen := map[int64]int{}
	var errs []error
	for range 4 {
		wg.Go(func() {
			taken, err := TakeDueScheduledDispatches(t.Context(), 200)
			mu.Lock()
			defer mu.Unlock()
			errs = append(errs, err)
			for _, sd := range taken {
				seen[sd.ID]++
			}
		})
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	assert.Len(t, seen, 5)
	for _, c := range seen {
		assert.Equal(t, 1, c)
	}
}

func TestCleanupOldScheduledDispatches(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()
	now := timeutil.TimeStampNow()
	age := func(sd *ActionScheduledDispatch, d time.Duration) {
		// xorm stamps `updated` with the real clock, so backdate it
		_, err := db.GetEngine(ctx).Exec("UPDATE action_scheduled_dispatch SET updated = ? WHERE id = ?", int64(now)-int64(d.Seconds()), sd.ID)
		require.NoError(t, err)
	}
	oldCancelled := newScheduledDispatch(t, 1, 100)
	require.NoError(t, CancelScheduledDispatch(ctx, 1, oldCancelled.ID, "x"))
	age(oldCancelled, 8*24*time.Hour)
	recentCancelled := newScheduledDispatch(t, 1, 100)
	require.NoError(t, CancelScheduledDispatch(ctx, 1, recentCancelled.ID, "x"))
	age(recentCancelled, time.Hour)
	staleClaimed := newScheduledDispatch(t, 1, 100)
	freshClaimed := newScheduledDispatch(t, 1, 100)
	_, err := TakeDueScheduledDispatches(ctx, 200)
	require.NoError(t, err)
	age(staleClaimed, time.Hour)
	age(freshClaimed, 2*time.Minute)

	require.NoError(t, CleanupOldScheduledDispatches(ctx, now))

	_, err = GetScheduledDispatch(ctx, 1, oldCancelled.ID)
	require.ErrorIs(t, err, ErrScheduledDispatchNotFound)
	got, err := GetScheduledDispatch(ctx, 1, staleClaimed.ID)
	require.NoError(t, err)
	assert.Equal(t, ScheduledDispatchCancelled, got.Status)
	assert.Equal(t, "interrupted", got.CancelReason)
	unittest.AssertExistsAndLoadBean(t, &ActionScheduledDispatch{ID: freshClaimed.ID, Status: ScheduledDispatchClaimed})
	_, err = GetScheduledDispatch(ctx, 1, recentCancelled.ID)
	require.NoError(t, err)
}
