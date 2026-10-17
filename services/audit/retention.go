// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package audit

import (
	"context"
	"time"

	audit_model "forgejo.org/models/audit"
	"forgejo.org/models/db"
	system_model "forgejo.org/models/system"
	"forgejo.org/modules/json"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/setting/config"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"
)

// PurgeBatchSize is how many sealed entries one purge transaction deletes. A variable for tests.
var PurgeBatchSize = 10_000

// Cleanup deletes the sealed entries older than the retention in force, batch after batch. It reads the setting
// before any query is built and has no option to choose another age; a pending lowering is not applied early.
func Cleanup(ctx context.Context) error {
	now := timeutil.TimeStampNow().AsTime()
	days := setting.Config().AuditRetention.Value(ctx).Effective(now)
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	for ctx.Err() == nil {
		if n, err := audit_model.PurgeBatch(ctx, cutoff, days, PurgeBatchSize); err != nil || n == 0 {
			return err
		}
	}
	return ctx.Err()
}

// SetRetention stores the retention an admin chose (see setting.NextAuditRetention). The stored row is read and
// replaced in one transaction under its lock, so two admins saving together can't compute from a stale previous
// value. The change is audited by SetSettings.
func SetRetention(ctx context.Context, days int) error {
	key := setting.Config().AuditRetention.DynKey()
	if err := db.WithTx(ctx, func(ctx context.Context) error {
		var cur setting.AuditRetentionState
		if v, has, err := system_model.GetSettingForUpdate(ctx, key); err != nil {
			return err
		} else if has && json.Unmarshal([]byte(v), &cur) != nil {
			cur = setting.AuditRetentionState{} // a stored value that can't be read is the default
		}
		next, err := setting.NextAuditRetention(cur, days, time.Now())
		if err != nil {
			return util.NewInvalidArgumentErrorf("%v", err)
		}
		v, err := json.Marshal(next)
		if err != nil {
			return err
		}
		return system_model.SetSettings(ctx, map[string]string{key: string(v)})
	}); err != nil {
		return err
	}
	if g := config.GetDynGetter(); g != nil {
		g.InvalidateCache()
	}
	return nil
}
