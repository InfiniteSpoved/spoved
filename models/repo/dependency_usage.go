// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repo

import (
	"context"

	"forgejo.org/models/db"
	"forgejo.org/modules/optional"
	"forgejo.org/modules/vulnerability"

	"xorm.io/builder"
)

// DependencyUsageOptions selects the dependency rows of many repositories (the repositories that use a package).
type DependencyUsageOptions struct {
	// RepoCond is the visibility rule on the repository table; the caller builds it (and so reads the dynamic settings)
	// before the session, and it is always ANDed.
	RepoCond  builder.Cond
	Ecosystem string
	Name      string // compared as vulnerability.NormalizeName makes it; needs Ecosystem
	Version   string // exact
	Direct    optional.Option[bool]
}

func (o *DependencyUsageOptions) cond() builder.Cond {
	cond := o.RepoCond
	if o.Ecosystem != "" {
		cond = cond.And(builder.Eq{"`repo_dependency`.ecosystem": o.Ecosystem})
	}
	if o.Name != "" {
		// the lowercased and PyPI-normalized forms are expressions, so the (ecosystem, name) index serves their first
		// column only, as in the match of repositories against advisories (RepoIDsUsing)
		cond = cond.And(builder.Expr(vulnerability.SQLNormalizedName(o.Ecosystem, "`repo_dependency`.name")+" = ?", vulnerability.NormalizeName(o.Ecosystem, o.Name)))
	}
	if o.Version != "" {
		cond = cond.And(builder.Eq{"`repo_dependency`.version": o.Version})
	}
	if o.Direct.Has() {
		cond = cond.And(builder.Eq{"`repo_dependency`.direct": o.Direct.ValueOrDefault(false)})
	}
	return cond
}

// dependencyUsageOrder is the order of the search: repository (RepoNameOrder), manifest path, name, version.
const dependencyUsageOrder = RepoNameOrder + ", `repo_dependency`.manifest_path, `repo_dependency`.name, `repo_dependency`.version, `repo_dependency`.id"

// RepoNameOrder sorts joined rows by owner, then project or name, then name: a root repository sorts by its own name
// where a repository in a project sorts by the project's (no concatenation in SQL, which differs per database).
const RepoNameOrder = "LOWER(`repository`.owner_name), CASE WHEN `repository`.group_name = '' THEN `repository`.lower_name ELSE LOWER(`repository`.group_name) END, `repository`.lower_name"

// CountDependencyUsage counts the rows.
func CountDependencyUsage(ctx context.Context, o *DependencyUsageOptions) (int64, error) {
	return db.GetEngine(ctx).Table("repo_dependency").Join("INNER", "repository", "`repository`.id = `repo_dependency`.repo_id").Where(o.cond()).Count()
}

// FindDependencyUsageCandidates is FindDependencyUsage for a range search, which reads up to limit rows only to
// decide whether each is inside the range: only id, repo_id, version and requirement are loaded.
func FindDependencyUsageCandidates(ctx context.Context, o *DependencyUsageOptions, limit int) ([]*Dependency, error) {
	var rows []*Dependency
	return rows, db.GetEngine(ctx).Select("`repo_dependency`.id, `repo_dependency`.repo_id, `repo_dependency`.version, `repo_dependency`.requirement").
		Join("INNER", "repository", "`repository`.id = `repo_dependency`.repo_id").Where(o.cond()).
		OrderBy(dependencyUsageOrder).Limit(limit).Find(&rows)
}

// GetDependenciesByIDs loads full rows by ID.
func GetDependenciesByIDs(ctx context.Context, ids []int64) (map[int64]*Dependency, error) {
	rows := make(map[int64]*Dependency, len(ids))
	if len(ids) == 0 {
		return rows, nil
	}
	return rows, db.GetEngine(ctx).In("id", ids).Find(&rows)
}

// FindDependencyUsage returns the rows ordered by repository full name, manifest path, name, version, skipping skip
// and returning at most limit (all if 0).
func FindDependencyUsage(ctx context.Context, o *DependencyUsageOptions, skip, limit int) ([]*Dependency, error) {
	sess := db.GetEngine(ctx).Select("`repo_dependency`.*").Join("INNER", "repository", "`repository`.id = `repo_dependency`.repo_id").Where(o.cond()).
		OrderBy(dependencyUsageOrder)
	if limit > 0 {
		sess = sess.Limit(limit, skip)
	}
	var rows []*Dependency
	return rows, sess.Find(&rows)
}

// FindDependencyUsageIDs returns the IDs of the rows in the order of FindDependencyUsage, at most limit.
func FindDependencyUsageIDs(ctx context.Context, o *DependencyUsageOptions, limit int) ([]int64, error) {
	var ids []int64
	return ids, db.GetEngine(ctx).Table("repo_dependency").Select("`repo_dependency`.id").Join("INNER", "repository", "`repository`.id = `repo_dependency`.repo_id").Where(o.cond()).
		OrderBy(dependencyUsageOrder).Limit(limit).Find(&ids)
}
