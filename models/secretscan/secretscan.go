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

// The git repositories a scan covers, each with its own state row: the refs other than refs/pull/*, the wiki, and
// refs/pull/* (which any reader can write).
const (
	KindRepo = "repo"
	KindWiki = "wiki"
	KindPull = "pull"
)

// Scan statuses. Incomplete: the scan is under way (a slice ended at a limit) or has holes, parts that were not
// scanned (Scan.Holes); holes stay until the state is reset, and a trigger with nothing new changes nothing.
const (
	StatusOK         = "ok"
	StatusIncomplete = "incomplete"
	StatusFailed     = "failed"
)

// Alert states.
const (
	StateOpen        = "open"
	StateRevoked     = "revoked"
	StateFalsePos    = "false_positive"
	StateUsedInTests = "used_in_tests"
	StateWontFix     = "wont_fix"
)

// CurrentKeyVersion is the version of the fingerprint key (modules/keying).
const CurrentKeyVersion = 1

// Caps of stored text (bytes).
const (
	maxPathLen   = 4096
	maxAuthorLen = 255
)

// Scan is the state of the scanner for a repository's git repository (Kind) or its wiki.
type Scan struct {
	ID     int64  `xorm:"pk autoincr"`
	RepoID int64  `xorm:"UNIQUE(repo_kind) NOT NULL"`
	Kind   string `xorm:"VARCHAR(8) UNIQUE(repo_kind) NOT NULL"`
	// ScannedTips is a JSON list of commit IDs whose whole history is scanned. It holds ref tips (those of refs/pull/*
	// in the pull row), so it is never shown: no API or page returns it (only status, counts and the error code).
	ScannedTips string `xorm:"LONGTEXT"`
	Status      string `xorm:"VARCHAR(16) NOT NULL DEFAULT ''"`
	Error       string `xorm:"VARCHAR(32) NOT NULL DEFAULT ''"` // a code, never text from git
	// Blobs, Bytes and SkippedLarge count the last scan: the files read, their size and the files over MAX_BLOB_SIZE
	Blobs        int64 `xorm:"NOT NULL DEFAULT 0"`
	Bytes        int64 `xorm:"NOT NULL DEFAULT 0"`
	SkippedLarge int64 `xorm:"NOT NULL DEFAULT 0"`
	Version      int64 `xorm:"NOT NULL DEFAULT 0"` // compare-and-swap counter of the row
	// FailCount counts the failed or limit-stopped slices in a row without progress; RetryUnix is when the cron task
	// and the next trigger may try again (0 = nothing to retry; while a scan is under way it is a safety net).
	FailCount int                `xorm:"NOT NULL DEFAULT 0"`
	RetryUnix timeutil.TimeStamp `xorm:"INDEX NOT NULL DEFAULT 0"`
	// Holes counts what was marked done without being scanned: files of a commit that alone was over the slice limits,
	// files the detector could not scan whole, and secrets left out for the alert cap. Error says why for the last.
	Holes       int64              `xorm:"NOT NULL DEFAULT 0"`
	UpdatedUnix timeutil.TimeStamp `xorm:"INDEX updated"`
}

func (*Scan) TableName() string { return "secret_scan" }

// Alert is one secret (by rule and fingerprint) found in a repository.
type Alert struct {
	ID            int64              `xorm:"pk autoincr"`
	RepoID        int64              `xorm:"UNIQUE(repo_rule_fingerprint) UNIQUE(repo_index) INDEX(repo_state) INDEX(repo_fingerprint) NOT NULL"`
	Index         int64              `xorm:"UNIQUE(repo_index) NOT NULL"`
	RuleID        string             `xorm:"VARCHAR(64) UNIQUE(repo_rule_fingerprint) NOT NULL"`
	SecretType    string             `xorm:"VARCHAR(32) NOT NULL DEFAULT ''"`
	Fingerprint   string             `xorm:"VARCHAR(64) UNIQUE(repo_rule_fingerprint) INDEX(repo_fingerprint) NOT NULL"`
	KeyVersion    int                `xorm:"SMALLINT NOT NULL DEFAULT 1"`
	State         string             `xorm:"VARCHAR(16) INDEX(repo_state) NOT NULL DEFAULT 'open'"`
	Reason        string             `xorm:"VARCHAR(16) NOT NULL DEFAULT ''"` // set when the alert is closed (written by the API and the pages)
	Comment       string             `xorm:"TEXT"`
	ClosedByID    int64              `xorm:"NOT NULL DEFAULT 0"`
	ClosedUnix    timeutil.TimeStamp `xorm:"NOT NULL DEFAULT 0"`
	LocationCount int                `xorm:"NOT NULL DEFAULT 0"` // every place seen, also those over the cap that have no row
	// ViaPull is true while only refs/pull/* (which any reader can write) reach the secret: such alerts have their
	// own share of the alert cap
	ViaPull     bool               `xorm:"NOT NULL DEFAULT false"`
	CreatedUnix timeutil.TimeStamp `xorm:"created"`
	UpdatedUnix timeutil.TimeStamp `xorm:"updated"`
}

func (*Alert) TableName() string { return "secret_scanning_alert" }

// Location is a place an alert's secret was found: a file at a line, in the commit that brought it.
type Location struct {
	ID        int64  `xorm:"pk autoincr"`
	AlertID   int64  `xorm:"UNIQUE(alert_location) NOT NULL"`
	RepoID    int64  `xorm:"INDEX NOT NULL"`
	Kind      string `xorm:"VARCHAR(8) UNIQUE(alert_location) NOT NULL"`
	BlobID    string `xorm:"VARCHAR(64) UNIQUE(alert_location) NOT NULL"`
	Path      string `xorm:"TEXT"` // text from git: stripped of control characters, capped at maxPathLen
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

// cleanText makes text of a commit or a path safe to store and show: no control or format characters, valid
// UTF-8, at most max bytes.
func cleanText(s string, max int) string {
	s = util.StripFormatting(util.StripControl(strings.ToValidUTF8(s, "?")))
	if len(s) > max {
		s = strings.ToValidUTF8(s[:max], "")
	}
	return s
}

// NewLocation fills a location from the text git gave; the caller sets the alert.
func NewLocation(repoID int64, kind, blobID, path string, startLine, endLine int, commitID, authorName, authorEmail string, commitUnix int64) *Location {
	return &Location{
		RepoID: repoID, Kind: kind, BlobID: blobID, Path: cleanText(path, maxPathLen), PathHash: PathHash(path),
		StartLine: startLine, EndLine: endLine, CommitID: commitID,
		AuthorName: cleanText(authorName, maxAuthorLen), AuthorEmail: cleanText(authorEmail, maxAuthorLen), CommitUnix: commitUnix,
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

// FindScans returns the state rows of a repository: its code, its wiki and its pull refs.
func FindScans(ctx context.Context, repoID int64) (scans []*Scan, err error) {
	return scans, db.GetEngine(ctx).Where("repo_id = ?", repoID).OrderBy("kind").Find(&scans)
}

// ErrScanMoved is a state row that another process changed since it was read.
var ErrScanMoved = errors.New("the scan state changed meanwhile")

// Save writes the state row as the scan that read old (nil = no row) leaves it, and returns ErrScanMoved when
// another process wrote it first (a second row of the same repository and kind, or a changed version).
func Save(ctx context.Context, old, s *Scan) error {
	if old == nil {
		// the unique key decides who inserts first; no row inserted is the lost race
		insert := "INSERT INTO secret_scan (repo_id, kind, scanned_tips, status, error, blobs, bytes, skipped_large, fail_count, retry_unix, holes, version, updated_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?) ON CONFLICT (repo_id, kind) DO NOTHING"
		if setting.Database.Type.IsMySQL() {
			insert = "INSERT IGNORE INTO secret_scan (repo_id, kind, scanned_tips, status, error, blobs, bytes, skipped_large, fail_count, retry_unix, holes, version, updated_unix) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)"
		}
		res, err := db.Exec(ctx, insert, s.RepoID, s.Kind, s.ScannedTips, s.Status, s.Error, s.Blobs, s.Bytes, s.SkippedLarge, s.FailCount, s.RetryUnix, s.Holes, timeutil.TimeStampNow())
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return util.Iif(err != nil, err, ErrScanMoved)
		}
		return nil
	}
	n, err := db.GetEngine(ctx).Where("id = ? AND version = ?", old.ID, old.Version).
		Cols("scanned_tips", "status", "error", "blobs", "bytes", "skipped_large", "fail_count", "retry_unix", "holes").Incr("version").Update(s)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrScanMoved
	}
	return nil
}

// GetAlertsByFingerprints returns the alerts of a repository with one of the fingerprints, in one query (the
// caller matches the rule: the same fingerprint under another rule is another alert).
func GetAlertsByFingerprints(ctx context.Context, repoID int64, fingerprints []string) ([]*Alert, error) {
	var alerts []*Alert
	if len(fingerprints) == 0 {
		return alerts, nil
	}
	return alerts, db.GetEngine(ctx).Where("repo_id = ?", repoID).In("fingerprint", fingerprints).Find(&alerts)
}

// CountOpenAlerts counts the open alerts of a repository, in one query: all of them, and those that only
// refs/pull/* reach. Closed alerts don't count towards the caps.
func CountOpenAlerts(ctx context.Context, repoID int64) (total, viaPull int64, err error) {
	var rows []struct {
		ViaPull bool  `xorm:"via_pull"`
		N       int64 `xorm:"n"`
	}
	if err := db.GetEngine(ctx).Table("secret_scanning_alert").Select("via_pull, COUNT(*) AS n").
		Where("repo_id = ? AND state = ?", repoID, StateOpen).GroupBy("via_pull").Find(&rows); err != nil {
		return 0, 0, err
	}
	for _, r := range rows {
		total += r.N
		if r.ViaPull {
			viaPull += r.N
		}
	}
	return total, viaPull, nil
}

// ClearViaPull marks alerts as reached by something other than refs/pull/*.
func ClearViaPull(ctx context.Context, alertIDs []int64) error {
	if len(alertIDs) == 0 {
		return nil
	}
	_, err := db.GetEngine(ctx).In("id", alertIDs).Cols("via_pull").Update(&Alert{ViaPull: false})
	return err
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

// PlaceKey is what makes a place of an alert: its kind, file path and line (the blob may differ: a file edited
// around the secret is the same place).
type PlaceKey struct {
	Kind, PathHash string
	StartLine      int
}

// StoredPlaces returns, for each of the alerts, the places that have a row, in one query. The number of rows is the
// number of stored places.
func StoredPlaces(ctx context.Context, alertIDs []int64) (map[int64]map[PlaceKey]bool, error) {
	places := map[int64]map[PlaceKey]bool{}
	if len(alertIDs) == 0 {
		return places, nil
	}
	var rows []*Location
	if err := db.GetEngine(ctx).Cols("alert_id", "kind", "path_hash", "start_line").In("alert_id", alertIDs).Find(&rows); err != nil {
		return nil, err
	}
	for _, r := range rows {
		if places[r.AlertID] == nil {
			places[r.AlertID] = map[PlaceKey]bool{}
		}
		places[r.AlertID][PlaceKey{r.Kind, r.PathHash, r.StartLine}] = true
	}
	return places, nil
}

// InsertLocations stores places in batches of a few rows per statement.
func InsertLocations(ctx context.Context, locs []*Location) error {
	const batch = 50 // 14 columns each: under SQLite's limit of 999 variables
	for len(locs) > 0 {
		n := min(batch, len(locs))
		if _, err := db.GetEngine(ctx).Insert(locs[:n]); err != nil {
			return err
		}
		locs = locs[n:]
	}
	return nil
}

// UnscannedRepoIDs returns up to limit repositories the cron task should scan: ready, not trashed, secret scanning
// not turned off below the site, and without a state row for the repository (an empty one included: its wiki may
// hold a secret, and the scan stores a row for it), or with a state row of either kind (a wiki too) whose retry time
// (a failed or budget-stopped scan) has come.
func UnscannedRepoIDs(ctx context.Context, now timeutil.TimeStamp, limit int) ([]int64, error) {
	var ids []int64
	err := db.GetEngine(ctx).Table("repository").Select("repository.id").
		Join("LEFT", "secret_scan", "secret_scan.repo_id = repository.id AND secret_scan.kind = ?", KindRepo).
		Where(builder.Eq{"repository.status": repo_model.RepositoryReady, "repository.trashed_unix": 0}).
		And(builder.Not{OffCond()}).
		And(builder.Or(
			builder.IsNull{"secret_scan.id"},
			builder.Exists(builder.Select("1").From("secret_scan", "due").Where(builder.And(
				builder.Expr("due.repo_id = repository.id"),
				builder.Gt{"due.retry_unix": 0},
				builder.Lte{"due.retry_unix": int64(now)},
			))),
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

// orgSettingCond is an EXISTS on the user setting key = value of the repository's owner, when that owner is an
// organization (as services/secretscan.Enabled reads it: a personal owner's settings never count).
func orgSettingCond(key, value string) builder.Cond {
	return builder.Exists(builder.Select("1").From("user_setting").Where(builder.And(
		builder.Expr("user_setting.user_id = repository.owner_id"),
		builder.Eq{"user_setting.setting_key": key, "user_setting.setting_value": value},
		builder.In("user_setting.user_id", builder.Select("id").From("`user`").Where(builder.Eq{"type": 1})), // user_model.UserTypeOrganization (models/user can't be imported here)
	)))
}

// orgRepoCond is the repositories of an organization (the ones in projects too), the trash left out.
func orgRepoCond(orgID int64) builder.Cond {
	return builder.Eq{"repository.owner_id": orgID, "repository.trashed_unix": 0}
}

// CountOrgRepos counts the repositories of an organization and, of those, the ones whose admins turned secret
// scanning off (their own setting; it counts while the organization neither turns it off nor locks it).
func CountOrgRepos(ctx context.Context, orgID int64) (total, off int64, err error) {
	if total, err = db.GetEngine(ctx).Table("repository").Where(orgRepoCond(orgID)).Count(); err != nil {
		return 0, 0, err
	}
	off, err = db.GetEngine(ctx).Table("repository").Where(orgRepoCond(orgID)).And("secret_scanning_disabled = ?", true).Count()
	return total, off, err
}

// FindOrgOffRepos lists the repositories of an organization whose admins turned secret scanning off, with the total.
func FindOrgOffRepos(ctx context.Context, orgID int64, opts db.ListOptions) (repo_model.RepositoryList, int64, error) {
	repos := repo_model.RepositoryList{}
	cond := builder.And(orgRepoCond(orgID), builder.Eq{"repository.secret_scanning_disabled": true})
	total, err := db.GetEngine(ctx).Where(cond).OrderBy("repository.lower_name").Limit(opts.PageSize, (opts.Page-1)*opts.PageSize).FindAndCount(&repos)
	return repos, total, err
}

// ScannedRepoIDsAfter returns up to limit ids above after of ready repositories that have a scan state and secret
// scanning isn't off for below the site (all of them, or an organization's): the ones to scan again when a switch
// turns on, since pushes meanwhile were not scanned. Never-scanned repositories are the cron task's.
func ScannedRepoIDsAfter(ctx context.Context, orgID, after int64, limit int) ([]int64, error) {
	var ids []int64
	sess := db.GetEngine(ctx).Table("repository").Select("repository.id").
		Where(builder.Eq{"repository.status": repo_model.RepositoryReady, "repository.trashed_unix": 0}).
		And(builder.Gt{"repository.id": after}).
		And(builder.Not{OffCond()}).
		And(builder.Exists(builder.Select("1").From("secret_scan").Where(builder.Expr("secret_scan.repo_id = repository.id"))))
	if orgID != 0 {
		sess = sess.And(builder.Eq{"repository.owner_id": orgID})
	}
	return ids, sess.OrderBy("repository.id").Limit(limit).Find(&ids)
}
