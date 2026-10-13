// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package doctor

import (
	"testing"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	secret_model "forgejo.org/models/secret"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Project secrets and variables whose project is gone, belongs to another organization, or whose owner is not an
// organization are orphans; everything else stays.
func TestOrphanedGroupRows(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()
	org3 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 3})
	group, err := repo_model.CreateRepoGroup(ctx, org3, "pa", "")
	require.NoError(t, err)
	_, err = secret_model.InsertEncryptedSecret(ctx, 3, group.ID, 0, "GOOD", "x")
	require.NoError(t, err)
	_, err = secret_model.InsertEncryptedSecret(ctx, 3, 0, 0, "ORG", "x")
	require.NoError(t, err)
	_, err = actions_model.InsertVariable(ctx, 3, group.ID, 0, "GOOD", "x")
	require.NoError(t, err)
	for _, table := range []string{"secret", "action_variable"} {
		count := func() int64 {
			n, err := db.GetEngine(ctx).Table(table).Where(orphanedGroupRowsCond(table)).Count()
			require.NoError(t, err)
			return n
		}
		assert.EqualValues(t, 0, count(), table)
		extraCol, extraVal := "created_unix", "0"
		if table == "action_variable" {
			extraCol, extraVal = "created_unix, data", "0, ''"
		}
		for _, ids := range [][2]int64{{3, 999}, {4, group.ID}, {2, group.ID}} { // missing project, another owner's, a user
			_, err := db.Exec(ctx, "INSERT INTO `"+table+"` (owner_id, group_id, repo_id, name, "+extraCol+") VALUES (?, ?, 0, 'BAD', "+extraVal+")", ids[0], ids[1])
			require.NoError(t, err)
		}
		assert.EqualValues(t, 3, count(), table)
		_, err := db.GetEngine(ctx).Table(table).Where(orphanedGroupRowsCond(table)).Delete()
		require.NoError(t, err)
		assert.EqualValues(t, 0, count(), table)
		n, err := db.GetEngine(ctx).Table(table).Where("name = 'GOOD'").Count()
		require.NoError(t, err)
		assert.EqualValues(t, 1, n, table)
	}
}
