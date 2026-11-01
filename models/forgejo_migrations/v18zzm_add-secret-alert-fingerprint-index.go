// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations

import (
	"code.forgejo.org/xorm/xorm"
)

func init() {
	registerMigration(&Migration{
		Description: "add an index of secret scanning alerts by repository and fingerprint",
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

func addSecretAlertFingerprintIndex(x *xorm.Engine) error {
	// the struct names only the two columns: without the options, Sync would drop every other index and unique key
	// of the table (repo_index, repo_rule_fingerprint, repo_state)
	_, err := x.SyncWithOptions(xorm.SyncOptions{IgnoreDropIndices: true, IgnoreConstrains: true}, new(v18zzmSecretScanningAlert))
	return err
}
