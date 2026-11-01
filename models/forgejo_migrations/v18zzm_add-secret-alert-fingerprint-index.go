// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations

import (
	"code.forgejo.org/xorm/xorm"
)

func init() {
	registerMigration(&Migration{
		Description: "add an index of secret scanning alerts by repository and fingerprint, and the requeue mark of the scan state",
		Upgrade:     addSecretAlertFingerprintIndex,
	})
}

// v18zzmSecretScanningAlert names the columns of the index. The unique key (repo, rule, fingerprint) can't serve a
// lookup by repository and fingerprint alone, since the rule sits between them.
type v18zzmSecretScanningAlert struct {
	RepoID      int64  `xorm:"INDEX(repo_fingerprint) NOT NULL"`
	Fingerprint string `xorm:"VARCHAR(64) INDEX(repo_fingerprint) NOT NULL"`
}

func (v18zzmSecretScanningAlert) TableName() string {
	return "secret_scanning_alert"
}

// v18zzmSecretScan is the new column of the scan state: a repository to scan again after a switch turned on, which
// survives a restart until the scan takes it.
type v18zzmSecretScan struct {
	Requeue bool `xorm:"INDEX NOT NULL DEFAULT false"`
}

func (v18zzmSecretScan) TableName() string {
	return "secret_scan"
}

func addSecretAlertFingerprintIndex(x *xorm.Engine) error {
	// the structs name only a few columns: without the options, Sync would drop every other index and unique key
	// of the tables (alert: repo_index, repo_rule_fingerprint, repo_state; scan: repo_kind, retry_unix, updated_unix)
	_, err := x.SyncWithOptions(xorm.SyncOptions{IgnoreDropIndices: true, IgnoreConstrains: true}, new(v18zzmSecretScanningAlert), new(v18zzmSecretScan))
	return err
}
