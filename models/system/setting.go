// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package system

import (
	"context"
	"math"
	"sync"
	"time"

	"forgejo.org/models/db"
	"forgejo.org/modules/log"
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
		err = db.Insert(ctx, &Setting{SettingKey: keyRevision, Version: 1})
		if err != nil {
			return 0
		}
		return 1
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

func SetSettings(ctx context.Context, settings map[string]string) error {
	_ = GetRevision(ctx) // prepare the "revision" key ahead
	return db.WithTx(ctx, func(ctx context.Context) error {
		e := db.GetEngine(ctx)
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
		return nil
	})
}

type dbConfigCachedGetter struct {
	mu sync.RWMutex

	cacheTime time.Time
	revision  int
	settings  map[string]string
	fetches   uint64 // the number of fetches started
	stored    uint64 // the fetch whose result is cached
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
// deadlocks until the busy timeout.
func (d *dbConfigCachedGetter) GetRevision(ctx context.Context) int {
	d.mu.RLock()
	cachedDuration := time.Since(d.cacheTime)
	cachedRevision := d.revision
	d.mu.RUnlock()

	if cachedDuration < time.Second {
		return cachedRevision
	}

	d.mu.Lock()
	d.fetches++
	fetch := d.fetches
	cachedRevision = d.revision
	d.mu.Unlock()

	rev := GetRevision(ctx)
	var settings map[string]string
	if rev != cachedRevision {
		var err error
		if rev, settings, err = GetAllSettings(ctx); err != nil {
			log.Error("Unable to get all settings: %v", err)
			return cachedRevision
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	// Only a fetch that started after the cached one may replace it, so the cache never goes back in time.
	if fetch > d.stored && (settings != nil || rev == d.revision) {
		d.stored = fetch
		if settings != nil {
			d.revision = rev
			d.settings = settings
		}
		d.cacheTime = time.Now()
	}
	return d.revision
}

func (d *dbConfigCachedGetter) InvalidateCache() {
	d.mu.Lock()
	d.cacheTime = time.Time{}
	d.stored = d.fetches // discard the fetches in flight
	d.mu.Unlock()
}

func NewDatabaseDynKeyGetter() config.DynKeyGetter {
	return &dbConfigCachedGetter{}
}
