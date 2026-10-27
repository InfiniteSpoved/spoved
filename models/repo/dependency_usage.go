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

// RepoNameOrder sorts joined rows by the full name of the repository: owner, then the project or, for a root
// repository, its own name, then its name (the order of owner/name and owner/project/name as text, without
// concatenating in SQL, which differs per database).
const RepoNameOrder = "LOWER(`repository`.owner_name), CASE WHEN `repository`.group_name = '' THEN `repository`.lower_name ELSE LOWER(`repository`.group_name) END, `repository`.lower_name"

// CountDependencyUsage counts the rows.
func CountDependencyUsage(ctx context.Context, o *DependencyUsageOptions) (int64, error) {
	return db.GetEngine(ctx).Table("repo_dependency").Join("INNER", "repository", "`repository`.id = `repo_dependency`.repo_id").Where(o.cond()).Count()
}

// FindDependencyUsage returns the rows ordered by repository full name, manifest path, name, version, skipping skip
// and returning at most limit (all if 0).
func FindDependencyUsage(ctx context.Context, o *DependencyUsageOptions, skip, limit int) ([]*Dependency, error) {
	sess := db.GetEngine(ctx).Select("`repo_dependency`.*").Join("INNER", "repository", "`repository`.id = `repo_dependency`.repo_id").Where(o.cond()).
		OrderBy(RepoNameOrder + ", `repo_dependency`.manifest_path, `repo_dependency`.name, `repo_dependency`.version, `repo_dependency`.id")
	if limit > 0 {
		sess = sess.Limit(limit, skip)
	}
	var rows []*Dependency
	return rows, sess.Find(&rows)
}
