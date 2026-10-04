// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repo_test

import (
	"testing"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/optional"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStorageReport(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	require.NoError(t, repo_model.UpdateRepoSize(db.DefaultContext, 1, 1000, 1<<40)) // user2
	require.NoError(t, repo_model.UpdateRepoSize(db.DefaultContext, 3, 1<<30, 7))    // org3

	var all []*repo_model.Repository
	require.NoError(t, db.GetEngine(db.DefaultContext).Find(&all))
	want := map[int64]*repo_model.StorageStats{}
	var total repo_model.StorageStats
	for _, r := range all {
		if want[r.OwnerID] == nil {
			want[r.OwnerID] = &repo_model.StorageStats{}
		}
		for _, s := range []*repo_model.StorageStats{want[r.OwnerID], &total} {
			s.NumRepos++
			s.GitSize += r.GitSize
			s.LFSSize += r.LFSSize
		}
	}

	t.Run("Totals", func(t *testing.T) {
		got, err := repo_model.GetStorageTotals(db.DefaultContext)
		require.NoError(t, err)
		assert.Equal(t, total, got)
		assert.Equal(t, total.GitSize+total.LFSSize, got.Size())
	})

	t.Run("AllOwners", func(t *testing.T) {
		owners, count, err := repo_model.FindOwnerStorage(db.DefaultContext, repo_model.FindOwnerStorageOptions{})
		require.NoError(t, err)
		assert.EqualValues(t, len(want), count)
		require.Len(t, owners, len(want))
		for i, o := range owners {
			assert.Equal(t, *want[o.OwnerID], o.StorageStats, "owner %d", o.OwnerID)
			assert.Equal(t, o.OwnerID, o.Owner.ID)
			if i > 0 {
				assert.GreaterOrEqual(t, owners[i-1].Size(), o.Size())
			}
		}
		assert.EqualValues(t, 2, owners[0].OwnerID)
	})

	t.Run("Sort", func(t *testing.T) {
		owners, _, err := repo_model.FindOwnerStorage(db.DefaultContext, repo_model.FindOwnerStorageOptions{OrderBy: "reversegitsize"})
		require.NoError(t, err)
		assert.EqualValues(t, 3, owners[0].OwnerID)

		owners, _, err = repo_model.FindOwnerStorage(db.DefaultContext, repo_model.FindOwnerStorageOptions{OrderBy: "mostrepos"})
		require.NoError(t, err)
		for i := 1; i < len(owners); i++ {
			assert.GreaterOrEqual(t, owners[i-1].NumRepos, owners[i].NumRepos)
		}

		// an unknown key falls back to reversesize
		owners, _, err = repo_model.FindOwnerStorage(db.DefaultContext, repo_model.FindOwnerStorageOptions{OrderBy: "id; DROP TABLE repository"})
		require.NoError(t, err)
		assert.EqualValues(t, 2, owners[0].OwnerID)
	})

	t.Run("Filters", func(t *testing.T) {
		owners, count, err := repo_model.FindOwnerStorage(db.DefaultContext, repo_model.FindOwnerStorageOptions{Keyword: "USER2"})
		require.NoError(t, err)
		assert.EqualValues(t, len(owners), count)
		assert.NotEmpty(t, owners)
		for _, o := range owners {
			assert.Contains(t, o.Owner.LowerName, "user2")
		}

		owners, count, err = repo_model.FindOwnerStorage(db.DefaultContext, repo_model.FindOwnerStorageOptions{OwnerType: optional.Some(user_model.UserTypeOrganization)})
		require.NoError(t, err)
		assert.EqualValues(t, len(owners), count)
		assert.NotEmpty(t, owners)
		for _, o := range owners {
			assert.True(t, o.Owner.IsOrganization())
		}
	})

	t.Run("Pagination", func(t *testing.T) {
		owners, count, err := repo_model.FindOwnerStorage(db.DefaultContext, repo_model.FindOwnerStorageOptions{ListOptions: db.ListOptions{Page: 2, PageSize: 2}})
		require.NoError(t, err)
		assert.EqualValues(t, len(want), count)
		assert.Len(t, owners, 2)
	})
}
