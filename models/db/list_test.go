// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package db_test

import (
	"math"
	"testing"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"xorm.io/builder"
)

type mockListOptions struct {
	db.ListOptions
}

func (opts mockListOptions) IsListAll() bool {
	return true
}

func (opts mockListOptions) ToConds() builder.Cond {
	return builder.NewCond()
}

func TestFind(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	xe, err := unittest.GetXORMEngine()
	require.NoError(t, err)
	require.NoError(t, xe.Sync(&repo_model.RepoUnit{}))

	var repoUnitCount int
	_, err = db.GetEngine(db.DefaultContext).SQL("SELECT COUNT(*) FROM repo_unit").Get(&repoUnitCount)
	require.NoError(t, err)
	assert.NotEmpty(t, repoUnitCount)

	opts := mockListOptions{}
	repoUnits, err := db.Find[repo_model.RepoUnit](db.DefaultContext, opts)
	require.NoError(t, err)
	assert.Len(t, repoUnits, repoUnitCount)

	cnt, err := db.Count[repo_model.RepoUnit](db.DefaultContext, opts)
	require.NoError(t, err)
	assert.EqualValues(t, repoUnitCount, cnt)

	repoUnits, newCnt, err := db.FindAndCount[repo_model.RepoUnit](db.DefaultContext, opts)
	require.NoError(t, err)
	assert.Equal(t, cnt, newCnt)
	assert.Len(t, repoUnits, repoUnitCount)
}

func TestGetSkipTakeNeverNegative(t *testing.T) {
	for _, page := range []int{1, 2, 1 << 30, math.MaxInt / 2, math.MaxInt} {
		opts := db.ListOptions{Page: page, PageSize: 50}
		skip, take := opts.GetSkipTake()
		assert.GreaterOrEqual(t, skip, 0, "page %d", page)
		assert.GreaterOrEqual(t, skip+take, 0, "page %d", page) // bleve adds the page size
		assert.Equal(t, 50, take)
	}
	opts := db.ListOptions{Page: 3, PageSize: 10}
	skip, _ := opts.GetSkipTake()
	assert.Equal(t, 20, skip)
	opts = db.ListOptions{Page: math.MaxInt, PageSize: 50}
	skip, _ = opts.GetSkipTake()
	assert.Equal(t, math.MaxInt/2, skip)
}
