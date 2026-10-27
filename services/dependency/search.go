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
	TokenRepos builder.Cond // what the credential may read (Reducer().RepoReadAccessFilter()), nil for none
	Project    string       // org only: a project of the organization
	Owner      string       // instance only: an owner name
}

// RepoCond is the one visibility rule of the org and instance searches, on the repository table: the runs list's
// rule (not trashed, a Code unit the policy and the organization allow, readable by the doer, not hidden by a
// two-factor requirement) and the dependency graph being on. empty is true when a project or owner filter names
// nothing, so the answer is an empty list (cond is then not to be used). It reads dynamic settings: call it before building a session.
func (s SearchScope) RepoCond(ctx context.Context) (cond builder.Cond, empty bool, err error) {
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

// ErrTooManyMatches is a range search with more candidates than MaxUsageCandidates.
var ErrTooManyMatches = util.NewInvalidArgumentErrorf("too many matches, add an owner or project filter")

// SearchUsage lists the dependency rows of the repositories in scope that match f, and how many there are. The
// errors that wrap util.ErrInvalidArgument are the caller's mistakes (a 422). With a range, the candidates (by
// package) are read up to MaxUsageCandidates, filtered by version in Go, and only then counted and paged.
func SearchUsage(ctx context.Context, scope SearchScope, f UsageFilter, lo db.ListOptions) ([]*UsageRow, int64, error) {
	if err := ValidateSearchPackage(f.Ecosystem, f.Name, f.Version); err != nil {
		return nil, 0, err
	}
	if f.Version != "" && f.Range != "" {
		return nil, 0, util.NewInvalidArgumentErrorf("version and range exclude each other")
	}
	var filter *vulnerability.RangeFilter
	if f.Range != "" {
		if f.Name == "" {
			return nil, 0, util.NewInvalidArgumentErrorf("range needs an ecosystem and a name")
		}
		var ok bool
		if filter, ok = vulnerability.ParseRangeFilter(f.Ecosystem, f.Range); !ok {
			return nil, 0, util.NewInvalidArgumentErrorf("range is not a requirement of the ecosystem")
		}
	}
	repoCond, empty, err := scope.RepoCond(ctx)
	if err != nil || empty {
		return nil, 0, err
	}
	opts := &repo_model.DependencyUsageOptions{RepoCond: repoCond, Ecosystem: f.Ecosystem, Name: f.Name, Version: f.Version, Direct: f.Direct}
	lo.SetDefaultValues()
	skip, take := lo.GetSkipTake()

	var rows []*repo_model.Dependency
	var uncertain map[int64]bool
	var total int64
	if filter == nil {
		if total, err = repo_model.CountDependencyUsage(ctx, opts); err != nil || total == 0 {
			return nil, 0, err
		}
		if rows, err = repo_model.FindDependencyUsage(ctx, opts, skip, take); err != nil {
			return nil, 0, err
		}
	} else {
		candidates, err := repo_model.FindDependencyUsage(ctx, opts, 0, MaxUsageCandidates+1)
		if err != nil {
			return nil, 0, err
		}
		if len(candidates) > MaxUsageCandidates {
			return nil, 0, ErrTooManyMatches
		}
		uncertain = map[int64]bool{}
		kept := candidates[:0]
		for _, d := range candidates {
			if in, unsure := filter.Match(d.Version, d.Requirement); in {
				kept = append(kept, d)
				uncertain[d.ID] = unsure
			}
		}
		total = int64(len(kept))
		rows = kept[min(skip, len(kept)):]
		if take > 0 && len(rows) > take {
			rows = rows[:take]
		}
	}
	ids := make([]int64, 0, len(rows))
	for _, d := range rows {
		if !slices.Contains(ids, d.RepoID) {
			ids = append(ids, d.RepoID)
		}
	}
	repos, err := repo_model.GetRepositoriesMapByIDs(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	out := make([]*UsageRow, 0, len(rows))
	for _, d := range rows {
		if repo := repos[d.RepoID]; repo != nil { // gone since the query: dropped
			out = append(out, &UsageRow{Dependency: d, Repo: repo, Uncertain: uncertain[d.ID]})
		}
	}
	return out, total, nil
}
