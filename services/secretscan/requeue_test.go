// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretscan

import (
	"slices"
	"testing"

	audit_model "forgejo.org/models/audit"
	"forgejo.org/models/db"
	secretscan_model "forgejo.org/models/secretscan"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func collectRequeued(t *testing.T, orgID int64) []int64 {
	t.Helper()
	var got []int64
	require.NoError(t, requeueScanned(t.Context(), orgID, func(id int64) { got = append(got, id) }))
	slices.Sort(got)
	return got
}

// Turning scanning on queues the repositories that were scanned before: the organization's (or all), not the ones whose
// own switch is off, not the trashed ones, and across more than one batch.
func TestRequeueScanned(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	// org3 owns repositories 3, 5 and 32; user2 owns 1
	for _, id := range []int64{1, 3, 5, 32} {
		_, err := db.GetEngine(ctx).Insert(&secretscan_model.Scan{RepoID: id, Kind: secretscan_model.KindRepo, Status: secretscan_model.StatusOK})
		require.NoError(t, err)
	}
	assert.Equal(t, []int64{5, 32}, collectRequeued(t, 3), "repository 3 is still being migrated in the fixtures")
	_, err := db.GetEngine(ctx).Exec("UPDATE repository SET status = 0 WHERE id = 3")
	require.NoError(t, err)
	assert.Equal(t, []int64{3, 5, 32}, collectRequeued(t, 3), "the organization's repositories only")
	assert.Equal(t, []int64{1, 3, 5, 32}, collectRequeued(t, 0))

	test.MockVariableValue(&requeueBatch, 1)
	assert.Equal(t, []int64{3, 5, 32}, collectRequeued(t, 3), "more than one batch")
	assert.Equal(t, []int64{1, 3, 5, 32}, collectRequeued(t, 0), "more than one batch")

	_, err = db.GetEngine(ctx).Exec("UPDATE repository SET secret_scanning_disabled = ? WHERE id = 5", true)
	require.NoError(t, err)
	assert.Equal(t, []int64{3, 32}, collectRequeued(t, 3), "a repository that turned it off is left out")

	_, err = db.GetEngine(ctx).Exec("UPDATE repository SET trashed_unix = 1 WHERE id = 32")
	require.NoError(t, err)
	assert.Equal(t, []int64{3}, collectRequeued(t, 3), "a trashed repository is left out")

	_, err = db.GetEngine(ctx).Exec("DELETE FROM secret_scan WHERE repo_id = 3")
	require.NoError(t, err)
	assert.Empty(t, collectRequeued(t, 3), "a repository that was never scanned is the cron task's")
}

// Only a change that turns scanning on for repositories asks for the queue; unlocking an organization that stays on,
// turning it off and a save without a change don't.
func TestOrgSwitchRequeues(t *testing.T) {
	unittest.PrepareTestEnv(t)
	var asked []int64
	test.MockVariableValue(&requeue, func(orgID int64) { asked = append(asked, orgID) })
	ctx := audit_model.WithSystem(t.Context(), "test")
	org := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 3})

	step := func(on, locked bool, want []int64, msg string) {
		t.Helper()
		asked = nil
		require.NoError(t, SetOrgSwitch(ctx, org, on, locked))
		assert.Equal(t, want, asked, msg)
	}
	step(true, false, nil, "no change")
	step(false, false, nil, "off")
	step(true, false, []int64{3}, "off to on")
	step(true, true, []int64{3}, "locking on turns it on in repositories that turned it off")
	step(true, false, nil, "unlocking an organization that stays on")
	step(false, true, nil, "locked off")
	step(true, true, []int64{3}, "locked off to locked on")
}
