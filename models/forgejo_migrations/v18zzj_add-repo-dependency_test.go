// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations

import (
	"testing"

	migration_tests "forgejo.org/models/gitea_migrations/test"

	"github.com/stretchr/testify/require"
)

func Test_addRepoDependency(t *testing.T) {
	x, deferable := migration_tests.PrepareTestEnv(t, 0)
	defer deferable()
	require.NotNil(t, x)

	require.NoError(t, addRepoDependency(x))
	require.NoError(t, addRepoDependency(x), "the migration can run again")

	_, err := x.Exec("INSERT INTO repo_dependency (repo_id, ecosystem, name, version, manifest_path, direct, license) VALUES (1, 'Go', 'a', 'v1', 'go.mod', ?, '')", true)
	require.NoError(t, err)
	_, err = x.Exec("INSERT INTO repo_dependency_scan (repo_id, commit_sha, scanned_unix, status, error, manifest_count, dependency_count) VALUES (1, 'abc', 1, 'ok', '', 1, 1)")
	require.NoError(t, err)
	_, err = x.Exec("INSERT INTO repo_dependency_scan (repo_id) VALUES (1)")
	require.Error(t, err, "one scan row per repository")
}
