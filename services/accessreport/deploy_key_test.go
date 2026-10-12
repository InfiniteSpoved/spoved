// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package accessreport

import (
	"slices"
	"testing"

	asymkey_model "forgejo.org/models/asymkey"
	"forgejo.org/models/db"
	perm_model "forgejo.org/models/perm"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The report's organization deploy key rows, the bulk lookup they come from, the lookup for one repository and the
// SSH decision (GetEffectiveDeployKey) agree on which keys cover which repositories.
func TestOrgDeployKeyRowsMatchLookup(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	t.Cleanup(func() { require.NoError(t, unittest.PrepareTestDatabase()) })
	ctx := db.DefaultContext

	// org3 owns repos 3, 5 and 32, org17 owns 23 and 24, user2 owns 1
	keys := []*asymkey_model.OrgDeployKey{
		{KeyID: 901, OrgID: 3, Name: "all write", Mode: perm_model.AccessModeWrite, IncludesAllRepos: true},
		{KeyID: 902, OrgID: 3, Name: "repo5 read", Mode: perm_model.AccessModeRead},
		{KeyID: 903, OrgID: 17, Name: "org17 all", Mode: perm_model.AccessModeRead, IncludesAllRepos: true},
		{KeyID: 904, OrgID: 3, Name: "expired", Mode: perm_model.AccessModeWrite, ExpiresUnix: timeutil.TimeStampNow() - 10},
	}
	for _, k := range keys {
		require.NoError(t, db.Insert(ctx, k))
	}
	for _, sel := range []*asymkey_model.OrgDeployKeyRepo{
		{OrgDeployKeyID: keys[1].ID, RepoID: 5},
		{OrgDeployKeyID: keys[1].ID, RepoID: 1},  // forged: another owner's repository
		{OrgDeployKeyID: keys[1].ID, RepoID: 23}, // forged: another organization's repository
		{OrgDeployKeyID: keys[3].ID, RepoID: 3},
	} {
		require.NoError(t, db.Insert(ctx, sel))
	}
	// repo 32 moved to org17, repo 3 archived
	_, err := db.Exec(ctx, "UPDATE repository SET owner_id = 17 WHERE id = 32")
	require.NoError(t, err)
	_, err = db.Exec(ctx, "UPDATE repository SET is_archived = ? WHERE id = 3", true)
	require.NoError(t, err)

	repoIDs := []int64{1, 3, 5, 23, 24, 32}
	bulk, err := asymkey_model.FindOrgDeployKeysCoveringRepos(ctx, repoIDs)
	require.NoError(t, err)
	rows, err := Compute(ctx, Options{RepoIDs: repoIDs})
	require.NoError(t, err)

	want := map[int64][]int64{ // repo -> covering keys
		3:  {keys[0].ID, keys[3].ID},
		5:  {keys[0].ID, keys[1].ID},
		23: {keys[2].ID},
		24: {keys[2].ID},
		32: {keys[2].ID},
	}
	for _, repoID := range repoIDs {
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: repoID})

		single, err := asymkey_model.FindOrgDeployKeysCoveringRepo(ctx, repo)
		require.NoError(t, err)
		var singleIDs, bulkIDs, rowIDs []int64
		for _, k := range single {
			singleIDs = append(singleIDs, k.ID)
		}
		for _, k := range bulk {
			if k.RepoID == repoID {
				bulkIDs = append(bulkIDs, k.ID)
			}
		}
		for _, r := range rows {
			if r.OrgDeployKey != nil && r.Repo.ID == repoID {
				rowIDs = append(rowIDs, r.OrgDeployKey.ID)
			}
		}
		assert.ElementsMatch(t, want[repoID], singleIDs, "repo %d", repoID)
		assert.ElementsMatch(t, want[repoID], bulkIDs, "repo %d", repoID)
		assert.ElementsMatch(t, want[repoID], rowIDs, "repo %d", repoID)

		for _, k := range keys {
			effective, err := asymkey_model.GetEffectiveDeployKey(ctx, k.KeyID, repo)
			if !slices.Contains(want[repoID], k.ID) {
				assert.True(t, asymkey_model.IsErrDeployKeyNotExist(err), "key %d repo %d: %v", k.ID, repoID, err)
				continue
			}
			require.NoError(t, err)
			assert.Equal(t, k.ID, effective.OrgKey.ID, "key %d repo %d", k.ID, repoID)
		}
	}

	for _, r := range rows {
		if r.OrgDeployKey == nil {
			continue
		}
		switch {
		case r.OrgDeployKey.ID == keys[3].ID:
			assert.False(t, r.Paths[0].Applied, "an expired key is not applied")
			assert.Equal(t, perm_model.AccessModeNone, r.Perm.AccessMode)
		case r.Repo.ID == 3:
			assert.Equal(t, perm_model.AccessModeRead, r.Perm.UnitAccessMode(unit.TypeCode), "a write key reads an archived repository")
			assert.True(t, r.Paths[0].Applied)
		default:
			assert.Equal(t, r.OrgDeployKey.Mode, r.Perm.UnitAccessMode(unit.TypeCode))
			assert.Equal(t, KindOrgDeployKey, r.Paths[0].Kind)
		}
		assert.Equal(t, "key", r.Type())
	}
}
