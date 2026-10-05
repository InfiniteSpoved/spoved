// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repo

import (
	"context"
	"errors"
	"math"
	"strings"

	"forgejo.org/models/db"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/optional"

	"github.com/dustin/go-humanize"
	"xorm.io/builder"
)

const storageStatsSelect = "COUNT(*) AS num_repos, COALESCE(SUM(git_size), 0) AS git_size, COALESCE(SUM(lfs_size), 0) AS lfs_size"

// StorageStats sums the stored sizes of a set of repositories.
type StorageStats struct {
	NumRepos int64 `xorm:"num_repos"`
	GitSize  int64 `xorm:"git_size"`
	LFSSize  int64 `xorm:"lfs_size"`
}

// Size returns the git and LFS size combined.
func (s StorageStats) Size() int64 {
	return s.GitSize + s.LFSSize
}

// Percent returns the share of total, in percent, that s uses.
func (s StorageStats) Percent(total StorageStats) float64 {
	if total.Size() == 0 {
		return 0
	}
	return float64(s.Size()) * 100 / float64(total.Size())
}

// GetStorageTotals returns the storage used by all repositories of the instance.
func GetStorageTotals(ctx context.Context) (StorageStats, error) {
	var stats StorageStats
	_, err := db.GetEngine(ctx).Table("repository").Select(storageStatsSelect).Get(&stats)
	return stats, err
}

// GetOwnerStorageTotals returns the storage used by the repositories of one owner.
func GetOwnerStorageTotals(ctx context.Context, ownerID int64) (StorageStats, error) {
	var stats StorageStats
	_, err := db.GetEngine(ctx).Table("repository").Where("owner_id = ?", ownerID).Select(storageStatsSelect).Get(&stats)
	return stats, err
}

// GetMedianRepoSize returns the median git and LFS size of all repositories, rounded down to whole bytes.
func GetMedianRepoSize(ctx context.Context, numRepos int64) (int64, error) {
	if numRepos == 0 {
		return 0, nil
	}
	offset, limit := (numRepos-1)/2, 1
	if numRepos%2 == 0 {
		offset, limit = numRepos/2-1, 2
	}
	var sizes []int64
	if err := db.GetEngine(ctx).Table("repository").Select("git_size + lfs_size AS total_size").
		OrderBy("git_size + lfs_size, id").Limit(limit, int(offset)).Find(&sizes); err != nil {
		return 0, err
	}
	var sum int64
	for _, size := range sizes {
		sum += size
	}
	return sum / int64(max(len(sizes), 1)), nil
}

// sizeFilterColumns maps the fields of a SizeFilter to their SQL expression.
var sizeFilterColumns = map[string]string{
	"git":   "git_size",
	"lfs":   "lfs_size",
	"total": "git_size + lfs_size",
}

// SizeFilter limits repositories to a size range of one field, in bytes. A zero Field filters nothing.
type SizeFilter struct {
	Field    string // a key of sizeFilterColumns
	Min, Max optional.Option[int64]
}

// ParseSizeFilter parses a field ("git", "lfs" or "total") and inclusive bounds such as "10 GB"; empty bounds are open.
func ParseSizeFilter(field, minSize, maxSize string) (SizeFilter, error) {
	if field == "" && minSize == "" && maxSize == "" {
		return SizeFilter{}, nil
	}
	if _, ok := sizeFilterColumns[field]; !ok {
		return SizeFilter{}, errors.New("unknown size field")
	}
	parse := func(s string) (optional.Option[int64], error) {
		if s == "" {
			return optional.None[int64](), nil
		}
		n, err := humanize.ParseBytes(s)
		if err != nil {
			return optional.None[int64](), err
		}
		if n > math.MaxInt64 {
			return optional.None[int64](), errors.New("size too large")
		}
		return optional.Some(int64(n)), nil
	}
	f := SizeFilter{Field: field}
	var err error
	if f.Min, err = parse(minSize); err != nil {
		return SizeFilter{}, err
	}
	if f.Max, err = parse(maxSize); err != nil {
		return SizeFilter{}, err
	}
	if f.Min.Has() && f.Max.Has() && f.Min.ValueOrDefault(0) > f.Max.ValueOrDefault(0) {
		return SizeFilter{}, errors.New("minimum size is above the maximum")
	}
	return f, nil
}

// Cond returns the condition of the filter.
func (f SizeFilter) Cond() builder.Cond {
	cond := builder.NewCond()
	column, ok := sizeFilterColumns[f.Field]
	if !ok {
		return cond
	}
	if has, v := f.Min.Get(); has {
		cond = cond.And(builder.Expr(column+" >= ?", v))
	}
	if has, v := f.Max.Get(); has {
		cond = cond.And(builder.Expr(column+" <= ?", v))
	}
	return cond
}

// OwnerStorage is the storage used by the repositories of one owner.
type OwnerStorage struct {
	OwnerID      int64 `xorm:"owner_id"`
	StorageStats `xorm:"extends"`
	Owner        *user_model.User `xorm:"-"`
}

// OwnerStorageOrderBy maps the sort keys of the storage report to their ORDER BY clause.
var OwnerStorageOrderBy = map[string]string{
	"size":           "SUM(git_size) + SUM(lfs_size) ASC",
	"reversesize":    "SUM(git_size) + SUM(lfs_size) DESC",
	"gitsize":        "SUM(git_size) ASC",
	"reversegitsize": "SUM(git_size) DESC",
	"lfssize":        "SUM(lfs_size) ASC",
	"reverselfssize": "SUM(lfs_size) DESC",
	"fewestrepos":    "COUNT(*) ASC",
	"mostrepos":      "COUNT(*) DESC",
}

// FindOwnerStorageOptions filters and sorts FindOwnerStorage.
type FindOwnerStorageOptions struct {
	db.ListOptions // PageSize 0 returns all owners
	Keyword        string
	OwnerType      optional.Option[user_model.UserType]
	OrderBy        string // a key of OwnerStorageOrderBy, anything else sorts by "reversesize"
}

// FindOwnerStorage returns the storage used per repository owner, and the number of owners.
func FindOwnerStorage(ctx context.Context, opts FindOwnerStorageOptions) ([]*OwnerStorage, int64, error) {
	cond := builder.NewCond()
	if opts.Keyword != "" || opts.OwnerType.Has() {
		userCond := builder.NewCond()
		if opts.Keyword != "" {
			userCond = userCond.And(builder.Like{"lower_name", strings.ToLower(opts.Keyword)})
		}
		if has, ownerType := opts.OwnerType.Get(); has {
			userCond = userCond.And(builder.Eq{"type": ownerType})
		}
		cond = builder.In("owner_id", builder.Select("id").From("`user`").Where(userCond))
	}

	var count int64
	if _, err := db.GetEngine(ctx).Table("repository").Where(cond).Select("COUNT(DISTINCT owner_id)").Get(&count); err != nil {
		return nil, 0, err
	}

	orderBy, ok := OwnerStorageOrderBy[opts.OrderBy]
	if !ok {
		orderBy = OwnerStorageOrderBy["reversesize"]
	}
	sess := db.GetEngine(ctx).Table("repository").Where(cond).
		Select("owner_id, " + storageStatsSelect).
		GroupBy("owner_id").OrderBy(orderBy + ", owner_id ASC")
	if opts.PageSize > 0 {
		sess = db.SetSessionPagination(sess, &opts.ListOptions)
	}
	owners := make([]*OwnerStorage, 0, opts.PageSize)
	if err := sess.Find(&owners); err != nil {
		return nil, 0, err
	}

	ids := make([]int64, len(owners))
	for i, o := range owners {
		ids[i] = o.OwnerID
	}
	users := make(map[int64]*user_model.User, len(ids))
	if err := db.GetEngine(ctx).In("id", ids).Find(&users); err != nil {
		return nil, 0, err
	}
	for _, o := range owners {
		o.Owner = users[o.OwnerID]
		if o.Owner == nil {
			o.Owner = user_model.NewGhostUser()
		}
	}
	return owners, count, nil
}
