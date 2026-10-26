// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repo

import (
	"context"
	"slices"
	"strings"

	"forgejo.org/models/db"
	"forgejo.org/modules/json"
	"forgejo.org/modules/optional"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"

	"xorm.io/builder"
)

// Dependency is one dependency a manifest of the repository's default branch declares. The rows of a repository
// are replaced as a whole by each scan (ReplaceRepoDependencies).
type Dependency struct {
	ID           int64  `xorm:"pk autoincr"`
	RepoID       int64  `xorm:"INDEX NOT NULL"`
	Ecosystem    string `xorm:"VARCHAR(16) INDEX(ecosystem_name) NOT NULL"` // an OSV ecosystem name
	Name         string `xorm:"VARCHAR(512) INDEX(ecosystem_name) NOT NULL"`
	Version      string `xorm:"VARCHAR(256) NOT NULL DEFAULT ''"`
	ManifestPath string `xorm:"VARCHAR(1024) NOT NULL DEFAULT ''"` // the git path of the manifest, never a file system path
	Direct       bool   `xorm:"NOT NULL DEFAULT false"`
	License      string `xorm:"VARCHAR(256) NOT NULL DEFAULT ''"`
}

// Scan statuses and error codes. The codes are the only values of DependencyScan.Error and of a skipped
// manifest's code; pages show them through a fixed list of phrases, never a stored text.
const (
	DependencyScanOK     = "ok"
	DependencyScanFailed = "failed"

	DependencyErrTimeout      = "timeout"
	DependencyErrTreeTooLarge = "tree_too_large"
	DependencyErrGit          = "git_error"
	DependencyErrInternal     = "internal_error"

	DependencySkipTooLarge           = "manifest_too_large"
	DependencySkipTooMany            = "too_many_manifests"
	DependencySkipTooManyDeps        = "too_many_dependencies"
	DependencySkipTooManyEntries     = "too_many_entries"
	DependencySkipParse              = "parse_error"
	DependencySkipInvalidPath        = "invalid_path"
	DependencySkipRead               = "read_error"
	maxSkippedManifestsStored    int = 100
)

// DependencyScanErrors are the values of DependencyScan.Error.
var DependencyScanErrors = []string{DependencyErrTimeout, DependencyErrTreeTooLarge, DependencyErrGit, DependencyErrInternal}

// DependencySkipCodes are the values of SkippedManifest.Code.
var DependencySkipCodes = []string{
	DependencySkipTooLarge, DependencySkipTooMany, DependencySkipTooManyDeps, DependencySkipTooManyEntries, DependencySkipParse,
	DependencySkipInvalidPath, DependencySkipRead,
}

// SkippedManifest is a manifest the scan didn't read, with the reason.
type SkippedManifest struct {
	Path string `json:"path"`
	Code string `json:"code"`
}

// DependencyScan is the result of the last scan of a repository: the commit the rows belong to, and when the
// last scan failed, its error code (the rows of the last good scan stay).
type DependencyScan struct {
	ID              int64              `xorm:"pk autoincr"`
	RepoID          int64              `xorm:"UNIQUE NOT NULL"`
	CommitSHA       string             `xorm:"VARCHAR(64) NOT NULL DEFAULT ''"` // "" until a scan succeeded
	ScannedUnix     timeutil.TimeStamp `xorm:"NOT NULL DEFAULT 0"`
	Status          string             `xorm:"VARCHAR(16) NOT NULL DEFAULT ''"`
	Error           string             `xorm:"VARCHAR(32) NOT NULL DEFAULT ''"`
	ManifestCount   int                `xorm:"NOT NULL DEFAULT 0"`
	DependencyCount int                `xorm:"NOT NULL DEFAULT 0"`
	Skipped         string             `xorm:"TEXT"` // JSON list of SkippedManifest
}

// TableName is repo_dependency.
func (Dependency) TableName() string { return "repo_dependency" }

// TableName is repo_dependency_scan.
func (DependencyScan) TableName() string { return "repo_dependency_scan" }

func init() {
	db.RegisterModel(new(Dependency))
	db.RegisterModel(new(DependencyScan))
}

// SkippedManifests returns the stored list; codes outside the allowlist read as an internal error.
func (s *DependencyScan) SkippedManifests() []SkippedManifest {
	var l []SkippedManifest
	if s.Skipped == "" || json.Unmarshal([]byte(s.Skipped), &l) != nil {
		return nil
	}
	for i := range l {
		if !slices.Contains(DependencySkipCodes, l[i].Code) {
			l[i].Code = DependencyErrInternal
		}
	}
	return l
}

// maxSkippedBytes keeps the stored list below the size of a MySQL TEXT column.
const maxSkippedBytes = 60000

// SetSkippedManifests stores the list (at most 100 entries and maxSkippedBytes of JSON, so a repository full of bad
// manifests stays small).
func (s *DependencyScan) SetSkippedManifests(l []SkippedManifest) {
	if len(l) > maxSkippedManifestsStored {
		l = l[:maxSkippedManifestsStored]
	}
	s.Skipped = ""
	for len(l) > 0 {
		b, _ := json.Marshal(l)
		if len(b) <= maxSkippedBytes {
			s.Skipped = string(b)
			return
		}
		l = l[:len(l)-1]
	}
}

// GetRepoDependencyScan returns the scan row of the repository, or nil if it was never scanned.
func GetRepoDependencyScan(ctx context.Context, repoID int64) (*DependencyScan, error) {
	s := new(DependencyScan)
	if has, err := db.GetEngine(ctx).Where("repo_id = ?", repoID).Get(s); err != nil || !has {
		return nil, err
	}
	return s, nil
}

// ReplaceRepoDependenciesIf replaces the rows of the repository in one transaction and stores the scan, but only if
// the scan row is still the one the caller read (expect, nil if there was none; same commit, time, status and error,
// so a scan of the same commit that finished meanwhile counts as another row): another process that finished a
// scan meanwhile wins, on any number of servers. It reports whether it wrote. With a nil deps the old rows stay and
// only the status of the scan row changes (a failed scan).
func ReplaceRepoDependenciesIf(ctx context.Context, scan *DependencyScan, deps []*Dependency, expect *DependencyScan) (written bool, err error) {
	err = db.WithTx(ctx, func(ctx context.Context) error {
		e := db.GetEngine(ctx)
		cols := []string{"commit_sha", "scanned_unix", "status", "error", "manifest_count", "dependency_count", "skipped"}
		if deps == nil {
			cols = []string{"scanned_unix", "status", "error"}
		}
		if expect == nil {
			if _, err := e.Insert(scan); err != nil {
				return err // the unique key on repo_id refuses it if another process inserted first
			}
		} else {
			// the conditional update is also the row lock that serializes writers
			n, err := e.Where("id = ? AND repo_id = ? AND commit_sha = ? AND scanned_unix = ? AND status = ? AND error = ?",
				expect.ID, scan.RepoID, expect.CommitSHA, expect.ScannedUnix, expect.Status, expect.Error).Cols(cols...).Update(scan)
			if err != nil || n == 0 {
				return err
			}
		}
		if deps != nil {
			if _, err := e.Where("repo_id = ?", scan.RepoID).Delete(new(Dependency)); err != nil {
				return err
			}
			for chunk := range slices.Chunk(deps, 500) {
				if _, err := e.Insert(&chunk); err != nil {
					return err
				}
			}
		}
		written = true
		return nil
	})
	if err != nil && expect == nil {
		// the first scan of another process stored its row first: that is a lost write, not a failure
		if cur, gerr := GetRepoDependencyScan(ctx, scan.RepoID); gerr == nil && cur != nil {
			return false, nil
		}
	}
	return written, err
}

// DependencySearchOptions selects the dependencies of one repository.
type DependencySearchOptions struct {
	db.ListOptions
	RepoID    int64
	Q         string // a part of the name, at most 100 characters
	Ecosystem string
	Direct    optional.Option[bool]
	Manifest  string // the exact path of a manifest
}

// MaxDependencyQuery is the longest search text.
const MaxDependencyQuery = 100

func (o *DependencySearchOptions) ToConds() builder.Cond {
	cond := builder.NewCond().And(builder.Eq{"repo_id": o.RepoID})
	if o.Q != "" {
		cond = cond.And(containsFold("name", o.Q))
	}
	if o.Ecosystem != "" {
		cond = cond.And(builder.Eq{"ecosystem": o.Ecosystem})
	}
	if o.Direct.Has() {
		cond = cond.And(builder.Eq{"direct": o.Direct.ValueOrDefault(false)})
	}
	if o.Manifest != "" {
		cond = cond.And(builder.Eq{"manifest_path": o.Manifest})
	}
	return cond
}

func (o *DependencySearchOptions) ToOrders() string {
	return "LOWER(name) ASC, name ASC, ecosystem ASC, version ASC, manifest_path ASC, id ASC"
}

// containsFold is "col contains q, ignoring case", with % and _ in q taken literally ('!' is the escape character,
// the one way SQLite, MySQL and PostgreSQL all agree on).
func containsFold(col, q string) builder.Cond {
	if len(q) > MaxDependencyQuery {
		q = q[:MaxDependencyQuery]
	}
	q = strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(q)
	if setting.Database.Type.IsSQLite3() {
		q = util.ToUpperASCII(q) // SQLite's UPPER only changes ASCII
	} else {
		q = strings.ToUpper(q)
	}
	return builder.Expr("UPPER("+col+") LIKE ? ESCAPE '!'", "%"+q+"%")
}

// UnscannedDependencyRepoIDs returns up to limit repositories that need a scan: ready, not empty, not trashed, and
// either without a scan row or with a scan of another commit than the default branch's head (as the branch table
// has it), except when the last scan failed after retryBefore.
func UnscannedDependencyRepoIDs(ctx context.Context, retryBefore timeutil.TimeStamp, limit int) ([]int64, error) {
	var ids []int64
	err := db.GetEngine(ctx).Table("repository").Select("repository.id").
		Join("LEFT", "repo_dependency_scan", "repo_dependency_scan.repo_id = repository.id").
		Join("LEFT", "branch", "branch.repo_id = repository.id AND branch.name = repository.default_branch AND branch.is_deleted = ?", false).
		Where(builder.Eq{"repository.is_empty": false, "repository.status": RepositoryReady, "repository.trashed_unix": 0}).
		And(builder.Or(
			builder.IsNull{"repo_dependency_scan.id"},
			builder.And(
				builder.Neq{"branch.commit_id": ""},
				builder.NotNull{"branch.commit_id"},
				builder.Expr("repo_dependency_scan.commit_sha <> branch.commit_id"),
				builder.Or(builder.Eq{"repo_dependency_scan.error": ""}, builder.Lt{"repo_dependency_scan.scanned_unix": int64(retryBefore)}),
			),
		)).
		OrderBy("repository.id").Limit(limit).
		Find(&ids)
	return ids, err
}

// DependencyFacet is one value of a filter with the number of dependencies that have it.
type DependencyFacet struct {
	Value string `xorm:"value"`
	Count int    `xorm:"cnt"`
}

// DependencyFacets are the counts behind the filters of the Dependencies page.
type DependencyFacets struct {
	Ecosystems []DependencyFacet
	Manifests  []DependencyFacet
	Direct     int
	Transitive int
}

// GetDependencyFacets counts the dependencies of the repository by ecosystem, by manifest and by relationship:
// one GROUP BY query for each list.
func GetDependencyFacets(ctx context.Context, repoID int64) (*DependencyFacets, error) {
	f := &DependencyFacets{}
	if err := db.GetEngine(ctx).Table("repo_dependency").Where("repo_id = ?", repoID).Select("ecosystem AS value, COUNT(*) AS cnt").GroupBy("ecosystem").OrderBy("ecosystem").Find(&f.Ecosystems); err != nil {
		return nil, err
	}
	// Ecosystem names mix cases (npm, RubyGems): list them alphabetically, as the table sorts them.
	slices.SortFunc(f.Ecosystems, func(a, b DependencyFacet) int {
		return strings.Compare(strings.ToLower(a.Value), strings.ToLower(b.Value))
	})
	if err := db.GetEngine(ctx).Table("repo_dependency").Where("repo_id = ?", repoID).Select("manifest_path AS value, COUNT(*) AS cnt").GroupBy("manifest_path").OrderBy("manifest_path").Find(&f.Manifests); err != nil {
		return nil, err
	}
	var rel []struct {
		Direct bool `xorm:"direct"`
		Count  int  `xorm:"cnt"`
	}
	if err := db.GetEngine(ctx).Table("repo_dependency").Where("repo_id = ?", repoID).Select("direct, COUNT(*) AS cnt").GroupBy("direct").Find(&rel); err != nil {
		return nil, err
	}
	for _, r := range rel {
		if r.Direct {
			f.Direct = r.Count
		} else {
			f.Transitive = r.Count
		}
	}
	return f, nil
}
