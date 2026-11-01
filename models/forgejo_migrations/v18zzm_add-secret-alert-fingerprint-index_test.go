// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations

import (
	"testing"

	migration_tests "forgejo.org/models/gitea_migrations/test"
	secretscan_model "forgejo.org/models/secretscan"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var secretAlertIndexes = []string{
	"unique:repo_id,index",
	"unique:repo_id,rule_id,fingerprint",
	"index:repo_id,state",
	"index:repo_id,fingerprint",
}

func Test_addSecretAlertFingerprintIndex(t *testing.T) {
	x, deferable := migration_tests.PrepareTestEnv(t, 0)
	defer deferable()
	require.NotNil(t, x)
	require.NoError(t, x.Sync(new(v18zzlSecretScanningAlert)))
	require.ElementsMatch(t, secretAlertIndexes[:3], indexSets(t, x, "secret_scanning_alert"))

	require.NoError(t, addSecretAlertFingerprintIndex(x))
	require.NoError(t, addSecretAlertFingerprintIndex(x), "the migration can run again")

	assert.ElementsMatch(t, secretAlertIndexes, indexSets(t, x, "secret_scanning_alert"), "the unique keys and the state index survive, the new index exists")
}

// A fresh install creates the table from the model: it must have the same indexes as an upgraded one.
func Test_secretAlertModelHasTheIndexes(t *testing.T) {
	x, deferable := migration_tests.PrepareTestEnv(t, 0)
	defer deferable()
	require.NotNil(t, x)
	require.NoError(t, x.Sync(new(secretscan_model.Alert)))
	assert.ElementsMatch(t, secretAlertIndexes, indexSets(t, x, "secret_scanning_alert"))
}
