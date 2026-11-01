// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations

import (
	"testing"

	migration_tests "forgejo.org/models/gitea_migrations/test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_addSecretAlertFingerprintIndex(t *testing.T) {
	x, deferable := migration_tests.PrepareTestEnv(t, 0)
	defer deferable()
	require.NotNil(t, x)
	require.NoError(t, x.Sync(new(v18zzlSecretScanningAlert)))

	require.NoError(t, addSecretAlertFingerprintIndex(x))
	require.NoError(t, addSecretAlertFingerprintIndex(x), "the migration can run again")

	tables, err := x.DBMetas()
	require.NoError(t, err)
	found := false
	for _, table := range tables {
		if table.Name != "secret_scanning_alert" {
			continue
		}
		for _, index := range table.Indexes {
			if len(index.Cols) == 2 && index.Cols[0] == "repo_id" && index.Cols[1] == "fingerprint" {
				found = true
				assert.Equal(t, 1, index.Type, "a plain index (xorm IndexType)")
			}
		}
	}
	assert.True(t, found, "an index on (repo_id, fingerprint)")
}
