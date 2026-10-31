// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// Package secretscan stores what the secret scanner (services/secretscan) found: one state row per repository and
// kind (the repository or its wiki), one alert per secret and the places it was found at. No column holds a secret
// or any part of it: an alert has the rule, a keyed fingerprint (modules/secretdetection) and the places.
package secretscan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"

	"xorm.io/builder"
)

// The two git repositories of a repository.
const (
	KindRepo = "repo"
	KindWiki = "wiki"
)

// Scan statuses. Incomplete means a budget (time, bytes, files, alerts) ended the scan: its tips are stored, so the
// next scan reads only what is new, and the cron task doesn't retry it. Failed keeps the earlier tips.
const (
	StatusOK         = "ok"
	StatusIncomplete = "incomplete"
	StatusFailed     = "failed"
)

// Alert states.
const (
	StateOpen         = "open"
	StateRevoked      = "revoked"
	StateFalsePos     = "false_positive"
	StateUsedInTests  = "used_in_tests"
	CurrentKeyVersion = 1 // the version of the fingerprint key (modules/keying)
)

// Caps of stored text (bytes).
const (
	MaxPathLen   = 4096
	maxAuthorLen = 255
)

// Scan is the state of the scanner for a repository's git repository (Kind) or its wiki.
type Scan struct {
	ID     int64  `xorm:"pk autoincr"`
	RepoID int64  `xorm:"UNIQUE(repo_kind) NOT NULL"`
	Kind   string `xorm:"VARCHAR(8) UNIQUE(repo_kind) NOT NULL"`
	// ScannedTips is a JSON list of object IDs whose whole reachable set is scanned (see TipList)
	ScannedTips string `xorm:"LONGTEXT"`
	Status      string `xorm:"VARCHAR(16) NOT NULL DEFAULT ''"`
	Error       string `xorm:"VARCHAR(32) NOT NULL DEFAULT ''"` // a code, never text from git
	// Blobs, Bytes and SkippedLarge count the last scan: the files read, their size and the files over MAX_BLOB_SIZE
	Blobs        int64              `xorm:"NOT NULL DEFAULT 0"`
	Bytes        int64              `xorm:"NOT NULL DEFAULT 0"`
	SkippedLarge int64              `xorm:"NOT NULL DEFAULT 0"`
	Version      int64              `xorm:"NOT NULL DEFAULT 0"` // compare-and-swap counter of the row
	UpdatedUnix  timeutil.TimeStamp `xorm:"INDEX updated"`
}

func (*Scan) TableName() string { return "secret_scan" }

// Alert is one secret (by rule and fingerprint) found in a repository.
type Alert struct {
	ID            int64              `xorm:"pk autoincr"`
	RepoID        int64              `xorm:"UNIQUE(repo_rule_fingerprint) UNIQUE(repo_index) INDEX(repo_state) NOT NULL"`
	Index         int64              `xorm:"UNIQUE(repo_index) NOT NULL"`
	RuleID        string             `xorm:"VARCHAR(64) UNIQUE(repo_rule_fingerprint) NOT NULL"`
	SecretType    string             `xorm:"VARCHAR(32) NOT NULL DEFAULT ''"`
	Fingerprint   string             `xorm:"VARCHAR(64) UNIQUE(repo_rule_fingerprint) NOT NULL"`
	KeyVersion    int                `xorm:"SMALLINT NOT NULL DEFAULT 1"`
	State         string             `xorm:"VARCHAR(16) INDEX(repo_state) NOT NULL DEFAULT 'open'"`
	Reason        string             `xorm:"VARCHAR(16) NOT NULL DEFAULT ''"` // set when the alert is closed (written by the API and the pages)
	Comment       string             `xorm:"TEXT"`
	ClosedByID    int64              `xorm:"NOT NULL DEFAULT 0"`
	ClosedUnix    timeutil.TimeStamp `xorm:"NOT NULL DEFAULT 0"`
	LocationCount int                `xorm:"NOT NULL DEFAULT 0"` // every place seen, also those over the cap that have no row
	CreatedUnix   timeutil.TimeStamp `xorm:"created"`
	UpdatedUnix   timeutil.TimeStamp `xorm:"updated"`
}

func (*Alert) TableName() string { return "secret_scanning_alert" }

// Location is a place an alert's secret was found: a file at a line, in the commit that brought it.
type Location struct {
	ID        int64  `xorm:"pk autoincr"`
	AlertID   int64  `xorm:"UNIQUE(alert_location) NOT NULL"`
	RepoID    int64  `xorm:"INDEX NOT NULL"`
	Kind      string `xorm:"VARCHAR(8) UNIQUE(alert_location) NOT NULL"`
	BlobID    string `xorm:"VARCHAR(64) UNIQUE(alert_location) NOT NULL"`
	Path      string `xorm:"TEXT"` // text from git: stripped of control characters, capped at MaxPathLen
	PathHash  string `xorm:"VARCHAR(64) UNIQUE(alert_location) NOT NULL"`
	StartLine int    `xorm:"UNIQUE(alert_location) NOT NULL DEFAULT 0"`
	EndLine   int    `xorm:"NOT NULL DEFAULT 0"`
	// CommitID is empty for a blob that only a ref reaches (a tag of a file), then Path is the name rev-list gave
	CommitID    string `xorm:"VARCHAR(64) NOT NULL DEFAULT ''"`
	AuthorName  string `xorm:"VARCHAR(255) NOT NULL DEFAULT ''"` // the commit's text, for display only: spoofable
	AuthorEmail string `xorm:"VARCHAR(255) NOT NULL DEFAULT ''"`
	CommitUnix  int64  `xorm:"NOT NULL DEFAULT 0"`
}

func (*Location) TableName() string { return "secret_scanning_location" }

// SecretScanningAlertIndex is the per-repository alert number (group_id is the repository), like issue_index.
type SecretScanningAlertIndex db.ResourceIndex

func init() {
	db.RegisterModel(new(Scan))
	db.RegisterModel(new(Alert))
	db.RegisterModel(new(Location))
	db.RegisterModel(new(SecretScanningAlertIndex))
}

// The organization's switch, as user settings of the organization: OrgKey is "false" while the owners turned it
// off (no row = on), OrgLockedKey is "true" while repositories can't change their own switch.
const (
	OrgKey       = "secret_scanning.enabled"
	OrgLockedKey = "secret_scanning.locked"
)

// PathHash is the hash that stands for a path in the unique key of a location.
func PathHash(path string) string {
	h := sha256.Sum256([]byte(path))
	return hex.EncodeToString(h[:])
}

// CleanText makes text of a commit or a path safe to store and show: no control or format characters, valid
// UTF-8, at most max bytes.
func CleanText(s string, max int) string {
	s = util.StripFormatting(util.StripControl(strings.ToValidUTF8(s, "?")))
	if len(s) > max {
		s = strings.ToValidUTF8(s[:max], "")
	}
	return s
}

// NewLocation fills a location from the text git gave; the caller sets the alert.
func NewLocation(repoID int64, kind, blobID, path string, startLine, endLine int, commitID, authorName, authorEmail string, commitUnix int64) *Location {
	return &Location{
		RepoID: repoID, Kind: kind, BlobID: blobID, Path: CleanText(path, MaxPathLen), PathHash: PathHash(path),
		StartLine: startLine, EndLine: endLine, CommitID: commitID,
		AuthorName: CleanText(authorName, maxAuthorLen), AuthorEmail: CleanText(authorEmail, maxAuthorLen), CommitUnix: commitUnix,
	}
}

// GetScan returns the state row, or nil when there is none.
func GetScan(ctx context.Context, repoID int64, kind string) (*Scan, error) {
	s := &Scan{}
	has, err := db.GetEngine(ctx).Where("repo_id = ? AND kind = ?", repoID, kind).Get(s)
	if err != nil || !has {
		return nil, err
	}
	return s, nil
}

// ErrScanMoved is a state row that another process changed since it was read.
var ErrScanMoved = errors.New("the scan state changed meanwhile")

// Save writes the state row as the scan that read old (nil = no row) leaves it, and returns ErrScanMoved when
// another process wrote it first (a second row of the same repository and kind, or a changed version).
func Save(ctx context.Context, old, s *Scan) error {
	if old == nil {
		// the unique key decides who inserts first; no row inserted is the lost race
		insert := "INSERT INTO secret_scan (repo_id, kind, scanned_tips, status, error, blobs, bytes, skipped_large, version, updated_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?) ON CONFLICT (repo_id, kind) DO NOTHING"
		if setting.Database.Type.IsMySQL() {
			insert = strings.Replace(insert, "INSERT", "INSERT IGNORE", 1)
			insert = strings.Replace(insert, " ON CONFLICT (repo_id, kind) DO NOTHING", "", 1)
		}
		res, err := db.Exec(ctx, insert, s.RepoID, s.Kind, s.ScannedTips, s.Status, s.Error, s.Blobs, s.Bytes, s.SkippedLarge, timeutil.TimeStampNow())
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return util.Iif(err != nil, err, ErrScanMoved)
		}
		return nil
	}
	n, err := db.GetEngine(ctx).Where("id = ? AND version = ?", old.ID, old.Version).
		Cols("scanned_tips", "status", "error", "blobs", "bytes", "skipped_large").Incr("version").Update(s)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrScanMoved
	}
	return nil
}

// GetAlertByKey returns the alert of a secret in a repository, or nil.
func GetAlertByKey(ctx context.Context, repoID int64, ruleID, fingerprint string) (*Alert, error) {
	a := &Alert{}
	has, err := db.GetEngine(ctx).Where("repo_id = ? AND rule_id = ? AND fingerprint = ?", repoID, ruleID, fingerprint).Get(a)
	if err != nil || !has {
		return nil, err
	}
	return a, nil
}

// CountAlerts is the number of alerts of a repository.
func CountAlerts(ctx context.Context, repoID int64) (int64, error) {
	return db.GetEngine(ctx).Where("repo_id = ?", repoID).Count(new(Alert))
}

// InsertAlert stores a new alert and gives it the next number of its repository; call it in a transaction.
func InsertAlert(ctx context.Context, a *Alert) error {
	idx, err := db.GetNextResourceIndex(ctx, "secret_scanning_alert_index", a.RepoID)
	if err != nil {
		return err
	}
	a.Index = idx
	if a.State == "" {
		a.State = StateOpen
	}
	a.KeyVersion = CurrentKeyVersion
	_, err = db.GetEngine(ctx).Insert(a)
	return err
}

// AddLocationCount adds to the number of places seen of an alert.
func AddLocationCount(ctx context.Context, alertID int64, n int) error {
	_, err := db.GetEngine(ctx).Exec("UPDATE secret_scanning_alert SET location_count = location_count + ?, updated_unix = ? WHERE id = ?", n, timeutil.TimeStampNow(), alertID)
	return err
}

// HasLocation tells whether an alert has a place at this file and line (the blob may differ: a file edited around
// the secret is the same place).
func HasLocation(ctx context.Context, alertID int64, kind, pathHash string, startLine int) (bool, error) {
	return db.GetEngine(ctx).Where("alert_id = ? AND kind = ? AND path_hash = ? AND start_line = ?", alertID, kind, pathHash, startLine).Exist(new(Location))
}

// CountLocations is the number of stored places of an alert.
func CountLocations(ctx context.Context, alertID int64) (int64, error) {
	return db.GetEngine(ctx).Where("alert_id = ?", alertID).Count(new(Location))
}

// UnscannedRepoIDs returns up to limit repositories the cron task should scan: ready, not empty, not trashed, secret
// scanning not turned off below the site, and without a state row for the repository, or whose last scan failed
// before retryBefore. An incomplete scan is left alone.
func UnscannedRepoIDs(ctx context.Context, retryBefore timeutil.TimeStamp, scope builder.Cond, limit int) ([]int64, error) {
	var ids []int64
	if scope == nil {
		scope = builder.NewCond()
	}
	err := db.GetEngine(ctx).Table("repository").Select("repository.id").
		Join("LEFT", "secret_scan", "secret_scan.repo_id = repository.id AND secret_scan.kind = ?", KindRepo).
		Where(builder.Eq{"repository.is_empty": false, "repository.status": repo_model.RepositoryReady, "repository.trashed_unix": 0}).
		And(builder.Not{OffCond()}).
		And(scope).
		And(builder.Or(
			builder.IsNull{"secret_scan.id"},
			builder.And(builder.Eq{"secret_scan.status": StatusFailed}, builder.Lt{"secret_scan.updated_unix": int64(retryBefore)}),
		)).
		OrderBy("repository.id").Limit(limit).
		Find(&ids)
	return ids, err
}

// OffCond is, on the repository table, the repositories whose organization or own setting turns secret scanning off:
// the SQL form of services/secretscan.Enabled without the site switch (the caller checks that). Only organizations
// store the settings, so for a personal repository only its own flag counts.
func OffCond() builder.Cond {
	return builder.Or(
		orgSettingCond(OrgKey, "false"),
		builder.And(builder.Eq{"repository.secret_scanning_disabled": true}, builder.Not{orgSettingCond(OrgLockedKey, "true")}),
	)
}

// orgSettingCond is an EXISTS on the user setting key = value of the repository's owner.
func orgSettingCond(key, value string) builder.Cond {
	return builder.Exists(builder.Select("1").From("user_setting").Where(builder.And(
		builder.Expr("user_setting.user_id = repository.owner_id"),
		builder.Eq{"user_setting.setting_key": key, "user_setting.setting_value": value},
	)))
}
