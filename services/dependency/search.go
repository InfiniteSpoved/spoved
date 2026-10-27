// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import (
	"context"
	"errors"
	"slices"

	"forgejo.org/models/db"
	"forgejo.org/models/organization"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/dependency"
	"forgejo.org/modules/optional"
	"forgejo.org/modules/util"
	"forgejo.org/modules/vulnerability"

	"xorm.io/builder"
)

// MaxUsageCandidates is how many rows a range search reads before it answers "too many" (a variable for tests).
var MaxUsageCandidates = 20000

// SearchScope says whose repositories a search covers and who asks.
type SearchScope struct {
	Org        *organization.Organization // nil: every owner (the instance routes)
	Doer       *user_model.User
	Admin      bool         // doer acts as a site admin or auditor: sees every repository that is not hidden by a rule of the instance
	TokenRepos builder.Cond // what the credential may read (Reducer().RepoReadAccessFilter()); defense in depth, the routes already refuse restricted tokens
	Project    string       // org only: a project of the organization
	Owner      string       // instance only: an owner name
}

// RepoCond is the one visibility rule of the org and instance searches, on the repository table: the runs list's
// rule (not trashed, a Code unit the policy and the organization allow, readable by the doer, not hidden by a
// two-factor requirement) and the dependency graph being on. empty is true when a project or owner filter names
// nothing, so the answer is an empty list (cond is then not to be used). It reads dynamic settings: call it before building a session.
func (s SearchScope) RepoCond(ctx context.Context) (cond builder.Cond, empty bool, err error) {
	if s.Org == nil && !s.Admin {
		return builder.NewCond(), false, errors.New("a search of every owner is for site admins only")
	}
	if !SiteEnabled(ctx) {
		return builder.NewCond(), true, nil
	}
	var ownerID int64
	if s.Org != nil {
		ownerID = s.Org.ID
	}
	cond = repo_model.OrgUnitRepoCond(ctx, ownerID, s.Doer, s.Admin, unit.TypeCode).And(builder.Not{repo_model.DependencyGraphOffCond()})
	if s.TokenRepos != nil {
		cond = cond.And(s.TokenRepos)
	}
	if s.Project != "" && s.Org != nil {
		group, err := repo_model.GetRepoGroupByName(ctx, s.Org.ID, s.Project)
		if errors.Is(err, repo_model.ErrRepoGroupNotExist) {
			return builder.NewCond(), true, nil
		} else if err != nil {
			return nil, false, err
		}
		cond = cond.And(builder.Eq{"`repository`.group_id": group.ID})
	}
	if s.Owner != "" && s.Org == nil {
		owner, err := user_model.GetUserByName(ctx, s.Owner)
		if user_model.IsErrUserNotExist(err) {
			return builder.NewCond(), true, nil
		} else if err != nil {
			return nil, false, err
		}
		cond = cond.And(builder.Eq{"`repository`.owner_id": owner.ID})
	}
	return cond, false, nil
}

// ValidateSearchPackage checks the package filters shared by the usage and the alert search.
func ValidateSearchPackage(ecosystem, name, version string) error {
	switch {
	case ecosystem != "" && !dependency.IsEcosystem(ecosystem):
		return util.NewInvalidArgumentErrorf("unknown ecosystem")
	case name != "" && ecosystem == "":
		return util.NewInvalidArgumentErrorf("name needs an ecosystem")
	case version != "" && name == "":
		return util.NewInvalidArgumentErrorf("version needs an ecosystem and a name")
	case len(name) > vulnerability.MaxNameLen:
		return util.NewInvalidArgumentErrorf("name is too long")
	case len(version) > dependency.MaxVersionLen:
		return util.NewInvalidArgumentErrorf("version is too long")
	}
	return nil
}

// UsageFilter narrows the dependency search. Ecosystem and Name are exact; Version or Range (not both) narrow the
// version.
type UsageFilter struct {
	Ecosystem, Name, Version, Range string
	Direct                          optional.Option[bool]
}

// UsageRow is one dependency of one repository in a search result.
type UsageRow struct {
	*repo_model.Dependency
	Repo      *repo_model.Repository
	Uncertain bool // the row has no version and its requirement overlaps the range
}

// TooManyMatches is a search or export with too many rows; it names the filter the route has, unless it is given.
func TooManyMatches(scope SearchScope) error {
	switch {
	case scope.Org != nil && scope.Project == "":
		return util.NewInvalidArgumentErrorf("too many matches, add a project filter, or narrow by version or range")
	case scope.Org == nil && scope.Owner == "":
		return util.NewInvalidArgumentErrorf("too many matches, add an owner filter, or narrow by version or range")
	}
	return util.NewInvalidArgumentErrorf("too many matches, narrow by version or range")
}

// usagePlan is a validated dependency search: the options, and for a range search the rows inside the range.
type usagePlan struct {
	opts      *repo_model.DependencyUsageOptions
	kept      []*repo_model.Dependency // range search: the candidates inside the range, in order
	uncertain map[int64]bool
	ranged    bool
	total     int64
}

// planUsage validates f and counts the rows. With a range, the candidates (by package) are read up to
// MaxUsageCandidates and filtered by version in Go.
func planUsage(ctx context.Context, scope SearchScope, f UsageFilter) (*usagePlan, error) {
	if err := ValidateSearchPackage(f.Ecosystem, f.Name, f.Version); err != nil {
		return nil, err
	}
	if f.Ecosystem == "" || f.Name == "" {
		return nil, util.NewInvalidArgumentErrorf("ecosystem and name are required")
	}
	if f.Version != "" && f.Range != "" {
		return nil, util.NewInvalidArgumentErrorf("version and range exclude each other")
	}
	var filter *vulnerability.RangeFilter
	if f.Range != "" {
		var ok bool
		if filter, ok = vulnerability.ParseRangeFilter(f.Ecosystem, f.Range); !ok {
			return nil, util.NewInvalidArgumentErrorf("range is not a requirement of the ecosystem")
		}
	}
	repoCond, empty, err := scope.RepoCond(ctx)
	if err != nil || empty {
		return &usagePlan{}, err
	}
	p := &usagePlan{opts: &repo_model.DependencyUsageOptions{RepoCond: repoCond, Ecosystem: f.Ecosystem, Name: f.Name, Version: f.Version, Direct: f.Direct}}
	if filter == nil {
		p.total, err = repo_model.CountDependencyUsage(ctx, p.opts)
		return p, err
	}
	candidates, err := repo_model.FindDependencyUsageCandidates(ctx, p.opts, MaxUsageCandidates+1)
	if err != nil {
		return nil, err
	}
	if len(candidates) > MaxUsageCandidates {
		return nil, TooManyMatches(scope)
	}
	p.ranged, p.uncertain = true, map[int64]bool{}
	for _, d := range candidates {
		if in, unsure := filter.Match(d.Version, d.Requirement); in {
			p.kept = append(p.kept, d)
			p.uncertain[d.ID] = unsure
		}
	}
	p.total = int64(len(p.kept))
	return p, nil
}

// page loads the rows from skip on, at most take (all if 0), with their repositories.
func (p *usagePlan) page(ctx context.Context, skip, take int) ([]*UsageRow, error) {
	if p.total == 0 {
		return nil, nil
	}
	var rows []*repo_model.Dependency
	if !p.ranged {
		var err error
		if rows, err = repo_model.FindDependencyUsage(ctx, p.opts, skip, take); err != nil {
			return nil, err
		}
	} else {
		kept := p.kept[min(skip, len(p.kept)):]
		if take > 0 && len(kept) > take {
			kept = kept[:take]
		}
		pageIDs := make([]int64, len(kept))
		for i, d := range kept {
			pageIDs[i] = d.ID
		}
		var err error
		if rows, err = loadDependencies(ctx, pageIDs); err != nil {
			return nil, err
		}
	}
	return p.withRepos(ctx, rows)
}

// loadDependencies loads the rows by ID in that order; a row deleted by a scan since the query is dropped.
func loadDependencies(ctx context.Context, ids []int64) ([]*repo_model.Dependency, error) {
	full, err := repo_model.GetDependenciesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	rows := make([]*repo_model.Dependency, 0, len(ids))
	for _, id := range ids {
		if row := full[id]; row != nil {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// withRepos pairs the rows with their repositories, dropping those whose repository is gone.
func (p *usagePlan) withRepos(ctx context.Context, rows []*repo_model.Dependency) ([]*UsageRow, error) {
	ids := make([]int64, 0, len(rows))
	for _, d := range rows {
		if !slices.Contains(ids, d.RepoID) {
			ids = append(ids, d.RepoID)
		}
	}
	repos, err := repo_model.GetRepositoriesMapByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]*UsageRow, 0, len(rows))
	for _, d := range rows {
		if repo := repos[d.RepoID]; repo != nil { // gone since the query: dropped
			out = append(out, &UsageRow{Dependency: d, Repo: repo, Uncertain: p.uncertain[d.ID]})
		}
	}
	return out, nil
}

// SearchUsage lists the dependency rows of the repositories in scope that match f, and how many there are. The
// errors that wrap util.ErrInvalidArgument are the caller's mistakes (a 422). With a range, the rows are filtered
// by version in Go before they are counted and paged.
func SearchUsage(ctx context.Context, scope SearchScope, f UsageFilter, lo db.ListOptions) ([]*UsageRow, int64, error) {
	p, err := planUsage(ctx, scope, f)
	if err != nil || p.total == 0 {
		return nil, 0, err
	}
	lo.SetDefaultValues()
	skip, take := lo.GetSkipTake()
	rows, err := p.page(ctx, skip, take)
	return rows, p.total, err
}

// MaxExportRows is the most rows an export holds (a variable for tests); above it the export is refused.
var MaxExportRows = 100_000

// ExportBatch is how many rows an export loads at a time (a variable for tests).
var ExportBatch = 500

// afterExportBatch is called after each batch of an export (a hook for tests).
var afterExportBatch func()

// ExportUsage lists every row SearchUsage would, in the same order. It reads the ordered IDs once, so the cap and
// the count come from one snapshot, and loads the rows batch by batch by ID: a row deleted meanwhile is dropped,
// no other row is skipped or repeated. start is called once with the count before the first emit (not at all if
// the count is above MaxExportRows or the filter is wrong, which are errors wrapping util.ErrInvalidArgument); a
// start error is returned as it is.
func ExportUsage(ctx context.Context, scope SearchScope, f UsageFilter, start func(total int64) error, emit func([]*UsageRow) error) error {
	p, err := planUsage(ctx, scope, f)
	if err != nil {
		return err
	}
	var ids []int64
	switch {
	case p.total == 0:
	case p.ranged:
		for _, d := range p.kept {
			ids = append(ids, d.ID)
		}
	default:
		if ids, err = repo_model.FindDependencyUsageIDs(ctx, p.opts, MaxExportRows+1); err != nil {
			return err
		}
	}
	if len(ids) > MaxExportRows {
		return TooManyMatches(scope)
	}
	if err := start(int64(len(ids))); err != nil {
		return err
	}
	for batch := range slices.Chunk(ids, ExportBatch) {
		deps, err := loadDependencies(ctx, batch)
		if err != nil {
			return err
		}
		rows, err := p.withRepos(ctx, deps)
		if err != nil {
			return err
		}
		if err := emit(rows); err != nil {
			return err
		}
		if afterExportBatch != nil {
			afterExportBatch()
		}
	}
	return nil
}
