// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package setting

// Trash settings: deleted repositories stay recoverable by site admins for RetentionDays.
var Trash = struct {
	RetentionDays  int    `ini:"RETENTION_DAYS"`
	RestoreHelpURL string `ini:"RESTORE_HELP_URL"`
}{
	RetentionDays: 365,
}

func loadTrashFrom(rootCfg ConfigProvider) {
	mustMapSetting(rootCfg, "trash", &Trash)
	if Trash.RetentionDays < 0 {
		Trash.RetentionDays = 0
	}
}
