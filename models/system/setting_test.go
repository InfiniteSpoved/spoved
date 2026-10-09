// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package system_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"forgejo.org/models/db"
	"forgejo.org/models/system"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSettings(t *testing.T) {
	keyName := "test.key"
	require.NoError(t, unittest.PrepareTestDatabase())

	require.NoError(t, db.TruncateBeans(db.DefaultContext, &system.Setting{}))

	rev, settings, err := system.GetAllSettings(db.DefaultContext)
	require.NoError(t, err)
	assert.Equal(t, 1, rev)
	assert.Len(t, settings, 1) // there is only one "revision" key

	err = system.SetSettings(db.DefaultContext, map[string]string{keyName: "true"})
	require.NoError(t, err)
	rev, settings, err = system.GetAllSettings(db.DefaultContext)
	require.NoError(t, err)
	assert.Equal(t, 2, rev)
	assert.Len(t, settings, 2)
	assert.Equal(t, "true", settings[keyName])

	err = system.SetSettings(db.DefaultContext, map[string]string{keyName: "false"})
	require.NoError(t, err)
	rev, settings, err = system.GetAllSettings(db.DefaultContext)
	require.NoError(t, err)
	assert.Equal(t, 3, rev)
	assert.Len(t, settings, 2)
	assert.Equal(t, "false", settings[keyName])

	// setting the same value should not trigger DuplicateKey error, and the "version" should be increased
	err = system.SetSettings(db.DefaultContext, map[string]string{keyName: "false"})
	require.NoError(t, err)

	rev, settings, err = system.GetAllSettings(db.DefaultContext)
	require.NoError(t, err)
	assert.Len(t, settings, 2)
	assert.Equal(t, 4, rev)
}

// A refresh waiting for a pooled connection must not hold the lock that a caller inside a transaction (which holds
// the only connection) needs to read a setting.
func TestDynKeyGetterNoDeadlockInTransaction(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	eng, err := db.GetMasterEngine(db.GetEngine(db.DefaultContext))
	require.NoError(t, err)
	eng.SetMaxOpenConns(1)
	defer eng.SetMaxOpenConns(setting.Database.MaxOpenConns)

	getter := system.NewDatabaseDynKeyGetter()
	outside := make(chan struct{})
	require.NoError(t, db.WithTx(db.DefaultContext, func(ctx context.Context) error {
		go func() {
			getter.GetRevision(context.Background()) // waits for the connection the transaction holds
			close(outside)
		}()
		time.Sleep(200 * time.Millisecond)

		inside := make(chan int)
		go func() { inside <- getter.GetRevision(ctx) }()
		select {
		case rev := <-inside:
			assert.Positive(t, rev)
		case <-time.After(5 * time.Second):
			t.Error("GetRevision inside the transaction is blocked by the refresh waiting for a connection")
		}
		return nil
	}))
	<-outside
}

// After a fixture reset the revision goes down; the cache must still follow it.
func TestDynKeyGetterLowerRevision(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	require.NoError(t, system.SetSettings(db.DefaultContext, map[string]string{"test.key": "a"}))
	getter := system.NewDatabaseDynKeyGetter()
	require.Greater(t, getter.GetRevision(db.DefaultContext), 1)

	_, err := db.Exec(db.DefaultContext, "UPDATE system_setting SET version=1 WHERE setting_key='revision'")
	require.NoError(t, err)
	getter.InvalidateCache()
	assert.Equal(t, 1, getter.GetRevision(db.DefaultContext))
}

// #337: concurrent first calls all find or create the one revision row.
func TestGetRevisionConcurrentInsert(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	for range 10 {
		require.NoError(t, db.TruncateBeans(db.DefaultContext, &system.Setting{}))
		var wg sync.WaitGroup
		start := make(chan struct{})
		revisions := make([]int, 20)
		for i := range revisions {
			wg.Go(func() {
				<-start
				revisions[i] = system.GetRevision(db.DefaultContext)
			})
		}
		close(start)
		wg.Wait()
		for _, rev := range revisions {
			assert.Equal(t, 1, rev)
		}
		unittest.AssertCount(t, &system.Setting{}, 1)
	}
}
