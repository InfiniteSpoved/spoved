// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package system

import (
	"context"
	"maps"
	"math"
	"slices"
	"sync"
	"time"

	"forgejo.org/models/db"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/setting/config"
	"forgejo.org/modules/timeutil"

	"xorm.io/builder"
)

type Setting struct {
	ID           int64  `xorm:"pk autoincr"`
	SettingKey   string `xorm:"varchar(255) unique"` // key should be lowercase
	SettingValue string `xorm:"text"`
	Version      int
	Created      timeutil.TimeStamp `xorm:"created"`
	Updated      timeutil.TimeStamp `xorm:"updated"`
}

// TableName sets the table name for the settings struct
func (s *Setting) TableName() string {
	return "system_setting"
}

func init() {
	db.RegisterModel(new(Setting))
}

const keyRevision = "revision"

func GetRevision(ctx context.Context) int {
	revision, exist, err := db.Get[Setting](ctx, builder.Eq{"setting_key": keyRevision})
	if err != nil {
		return 0
	} else if !exist {
		// Concurrent first callers may all get here: insert only if absent, then read whichever row was stored.
		if err = EnsureSetting(ctx, keyRevision); err != nil {
			return 0
		}
		if revision, exist, err = db.Get[Setting](ctx, builder.Eq{"setting_key": keyRevision}); err != nil || !exist {
			return 0
		}
	}
	if revision.Version <= 0 || revision.Version >= math.MaxInt-1 {
		_, err = db.Exec(ctx, "UPDATE system_setting SET version=1 WHERE setting_key=?", keyRevision)
		if err != nil {
			return 0
		}
		return 1
	}
	return revision.Version
}

func GetAllSettings(ctx context.Context) (revision int, res map[string]string, err error) {
	_ = GetRevision(ctx) // prepare the "revision" key ahead
	var settings []*Setting
	if err := db.GetEngine(ctx).
		Find(&settings); err != nil {
		return 0, nil, err
	}
	res = make(map[string]string)
	for _, s := range settings {
		if s.SettingKey == keyRevision {
			revision = s.Version
		}
		res[s.SettingKey] = s.SettingValue
	}
	return revision, res, nil
}

// GetSettingForUpdate returns the stored value of key (has = false when it isn't stored) and, inside a transaction,
// locks its row until the transaction ends, so that a read-modify-write of the value can't lose a concurrent one.
// EnsureSetting stores key with an empty value unless it exists, so that GetSettingForUpdate has a row to lock on the
// first save too. Concurrent callers don't fail: the insert does nothing on a conflict (a failed statement would also
// abort a PostgreSQL transaction).
func EnsureSetting(ctx context.Context, key string) error {
	insert := "INSERT INTO system_setting (setting_key, setting_value, version, created, updated) VALUES (?, '', 1, ?, ?) ON CONFLICT (setting_key) DO NOTHING"
	if setting.Database.Type.IsMySQL() {
		insert = "INSERT IGNORE INTO system_setting (setting_key, setting_value, version, created, updated) VALUES (?, '', 1, ?, ?)"
	}
	now := timeutil.TimeStampNow()
	_, err := db.Exec(ctx, insert, key, now, now)
	return err
}

func GetSettingForUpdate(ctx context.Context, key string) (value string, has bool, err error) {
	if _, err := db.Exec(ctx, "UPDATE system_setting SET version=version WHERE setting_key=?", key); err != nil {
		return "", false, err
	}
	s, has, err := db.Get[Setting](ctx, builder.Eq{"setting_key": key})
	if err != nil || !has {
		return "", false, err
	}
	return s.SettingValue, true, nil
}

func SetSettings(ctx context.Context, settings map[string]string) error {
	_ = GetRevision(ctx) // prepare the "revision" key ahead
	return db.WithTx(ctx, func(ctx context.Context) error {
		e := db.GetEngine(ctx)
		// lock the rows (a no-op update, as GetSettingForUpdate) so that the values read are the ones replaced; in key
		// order, so that two calls with overlapping keys can't deadlock
		keys := slices.Sorted(maps.Keys(settings))
		for _, k := range keys {
			if _, err := db.Exec(ctx, "UPDATE system_setting SET version=version WHERE setting_key=?", k); err != nil {
				return err
			}
		}
		var stored []*Setting
		if err := e.In("setting_key", keys).Find(&stored); err != nil {
			return err
		}
		before := make(map[string]string, len(stored))
		for _, s := range stored {
			before[s.SettingKey] = s.SettingValue
		}
		_, err := db.Exec(ctx, "UPDATE system_setting SET version=version+1 WHERE setting_key=?", keyRevision)
		if err != nil {
			return err
		}
		for k, v := range settings {
			res, err := e.Exec("UPDATE system_setting SET version=version+1, setting_value=? WHERE setting_key=?", v, k)
			if err != nil {
				return err
			}
			rows, _ := res.RowsAffected()
			if rows == 0 { // if no existing row, insert a new row
				if _, err = e.Insert(&Setting{SettingKey: k, SettingValue: v}); err != nil {
					return err
				}
			}
		}
		return recordSettings(ctx, before, settings)
	})
}

type dbConfigCachedGetter struct {
	mu sync.RWMutex

	cacheTime time.Time
	revision  int
	settings  map[string]string
	epoch     uint64 // bumped by InvalidateCache to discard the refreshes in flight
}

var _ config.DynKeyGetter = (*dbConfigCachedGetter)(nil)

func (d *dbConfigCachedGetter) GetValue(ctx context.Context, key string) (v string, has bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	v, has = d.settings[key]
	return v, has
}

// GetRevision never queries the database while holding d.mu: callers inside a transaction hold a pooled connection
// (and on SQLite the write lock) while they wait for d.mu, so a query under d.mu that waits for a connection
// deadlocks until the busy timeout. Two concurrent refreshes may store in either order; the next refresh, at most a
// second later, corrects an older result, the same window as the cache itself. The revision is compared with != as
// fixtures reset it.
func (d *dbConfigCachedGetter) GetRevision(ctx context.Context) int {
	d.mu.RLock()
	cachedDuration := time.Since(d.cacheTime)
	cachedRevision, epoch := d.revision, d.epoch
	d.mu.RUnlock()

	if cachedDuration < time.Second {
		return cachedRevision
	}

	rev := GetRevision(ctx)
	var settings map[string]string
	var err error
	if rev != cachedRevision {
		rev, settings, err = GetAllSettings(ctx)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if epoch != d.epoch {
		return d.revision
	}
	if err != nil {
		log.Error("Unable to get all settings: %v", err)
	} else if settings != nil {
		d.revision = rev
		d.settings = settings
	}
	d.cacheTime = time.Now()
	return d.revision
}

func (d *dbConfigCachedGetter) InvalidateCache() {
	d.mu.Lock()
	d.cacheTime = time.Time{}
	d.epoch++
	d.mu.Unlock()
}

func NewDatabaseDynKeyGetter() config.DynKeyGetter {
	return &dbConfigCachedGetter{}
}
