// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations

import (
	"forgejo.org/modules/timeutil"

	"code.forgejo.org/xorm/xorm"
)

func init() {
	registerMigration(&Migration{
		Description: "add scheduled time and pinned head commit to pull request auto merges",
		Upgrade:     addPullAutoMergeScheduledAfter,
	})
}

type v18kAutoMerge struct {
	ScheduledAfterUnix timeutil.TimeStamp `xorm:"NOT NULL DEFAULT 0"`
	HeadCommitID       string             `xorm:"VARCHAR(64)"`
}

func (v18kAutoMerge) TableName() string {
	return "pull_auto_merge"
}

func addPullAutoMergeScheduledAfter(x *xorm.Engine) error {
	_, err := x.SyncWithOptions(xorm.SyncOptions{IgnoreDropIndices: true}, new(v18kAutoMerge))
	return err
}
