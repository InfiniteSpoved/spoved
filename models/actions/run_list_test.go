// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package actions

import (
	"testing"

	"forgejo.org/models/db"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/translation"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"xorm.io/builder"
)

func TestActionStatusList(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	translation.InitLocales(t.Context())

	statusInfoList := GetStatusInfoList(t.Context(), translation.NewLocale("en-US"))
	assert.Len(t, statusInfoList, 7)
	statuses := []string{"Blocked", "Canceled", "Failure", "Running", "Skipped", "Success", "Waiting"}
	statusInts := []int{7, 3, 2, 6, 4, 1, 5}
	for i, statusString := range statuses {
		assert.Equal(t, statusInfoList[i].Status, statusInts[i])
		assert.Equal(t, statusInfoList[i].DisplayedStatus, statusString)
	}

	statusInfoList = GetStatusInfoList(t.Context(), translation.NewLocale("de-DE"))
	assert.Len(t, statusInfoList, 7)
	statuses = []string{"Abgebrochen", "Blockiert", "Erfolg", "Fehler", "Laufend", "Übersprungen", "Wartend"}
	statusInts = []int{3, 7, 1, 2, 6, 4, 5}
	for i, statusString := range statuses {
		assert.Equal(t, statusInfoList[i].Status, statusInts[i])
		assert.Equal(t, statusInfoList[i].DisplayedStatus, statusString)
	}
}

func TestFindRunOptionsRepoCondAndCreated(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	all, err := db.Find[ActionRun](t.Context(), FindRunOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, all)
	first := all[len(all)-1] // oldest ID

	only, err := db.Find[ActionRun](t.Context(), FindRunOptions{RepoCond: builder.Eq{"repository.id": first.RepoID}})
	require.NoError(t, err)
	for _, r := range only {
		assert.Equal(t, first.RepoID, r.RepoID)
	}

	none, err := db.Find[ActionRun](t.Context(), FindRunOptions{RepoCond: builder.Eq{"repository.id": -1}})
	require.NoError(t, err)
	assert.Empty(t, none)

	from, err := db.Find[ActionRun](t.Context(), FindRunOptions{CreatedFrom: first.Created + 1})
	require.NoError(t, err)
	for _, r := range from {
		assert.Greater(t, int64(r.Created), int64(first.Created))
	}
	to, err := db.Find[ActionRun](t.Context(), FindRunOptions{CreatedTo: first.Created})
	require.NoError(t, err)
	for _, r := range to {
		assert.LessOrEqual(t, int64(r.Created), int64(first.Created))
	}
	assert.Len(t, from, len(all)-len(to), "from/to split the runs")
}
