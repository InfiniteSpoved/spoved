// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// Package trash restores and permanently deletes repositories that were moved to the trash by
// repo_service.TrashRepository. Only site admins reach it.
package trash

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"forgejo.org/models/db"
	"forgejo.org/models/organization"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	code_indexer "forgejo.org/modules/indexer/code"
	issue_indexer "forgejo.org/modules/indexer/issues"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
	repo_service "forgejo.org/services/repository"

	"xorm.io/builder"
)

// ErrTrashConflict is returned when the repository is not in the trash (any more): it was restored or
// purged, or another purge is in progress.
var ErrTrashConflict = errors.New("the repository is not in the trash, or is being restored or purged already")

// purgeRetryAfter is how long a purge claim is left alone before the cron job retries it (a crashed purge).
const purgeRetryAfter = 6 * time.Hour

// Item is a trashed repository as listed to site admins.
type Item struct {
	Repo          *repo_model.Repository
	DaysRemaining int
	Purging       bool
}

// ListOptions filters List.
type ListOptions struct {
	db.ListOptions
	Keyword string
}

func (o ListOptions) cond() builder.Cond {
	cond := builder.Neq{"trashed_unix": 0}
	if o.Keyword == "" {
		return cond
	}
	kw := strings.ToLower(o.Keyword)
	return builder.And(cond, builder.Or(
		builder.Like{"lower(owner_name)", kw},
		builder.Like{"lower_name", kw},
	))
}

// List returns trashed repositories, newest first, with the number of matches and their total size.
func List(ctx context.Context, opts ListOptions) (items []*Item, count, totalSize int64, err error) {
	repos := make([]*repo_model.Repository, 0, opts.PageSize)
	sess := db.GetEngine(ctx).Where(opts.cond())
	if count, err = db.SetSessionPagination(sess, &opts).OrderBy("ABS(trashed_unix) DESC").FindAndCount(&repos); err != nil {
		return nil, 0, 0, err
	}
	size, err := db.GetEngine(ctx).Where(opts.cond()).Sum(new(repo_model.Repository), "size")
	if err != nil {
		return nil, 0, 0, err
	}
	totalSize = int64(size)
	for _, repo := range repos {
		items = append(items, &Item{Repo: repo, DaysRemaining: daysRemaining(repo), Purging: repo.TrashedUnix < 0})
	}
	return items, count, totalSize, nil
}

// purgeAt is when the repository is deleted automatically, counted from the day it was trashed.
func purgeAt(repo *repo_model.Repository) time.Time {
	return repo.TrashedUnix.AsTime().Add(time.Duration(setting.Trash.RetentionDays) * 24 * time.Hour)
}

func daysRemaining(repo *repo_model.Repository) int {
	left := time.Until(purgeAt(repo))
	if left <= 0 {
		return 0
	}
	return int((left + 24*time.Hour - 1) / (24 * time.Hour))
}

// Restore takes the repository out of the trash: same ID, owner, name and data.
func Restore(ctx context.Context, doer *user_model.User, id int64) error {
	var repo *repo_model.Repository
	if err := db.WithTx(ctx, func(ctx context.Context) error {
		res, err := db.Exec(ctx, "UPDATE `repository` SET trashed_unix=0 WHERE id=? AND trashed_unix>0", id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrTrashConflict
		}
		if repo, err = repo_model.GetRepositoryByID(ctx, id); err != nil {
			return err
		}
		if _, err = db.Exec(ctx, "UPDATE `user` SET num_repos=num_repos+1 WHERE id=?", repo.OwnerID); err != nil {
			return err
		}
		return organization.AddTeamRepoCounts(ctx, repo.ID, 1)
	}); err != nil {
		return err
	}

	issue_indexer.UpdateRepoIndexer(ctx, repo.ID)
	if setting.Indexer.RepoIndexerEnabled {
		code_indexer.UpdateRepoIndexer(repo)
	}
	return nil
}

// Purge deletes a trashed repository for good.
func Purge(ctx context.Context, doer *user_model.User, id int64) error {
	if err := claimForPurge(ctx, id, builder.Gt{"trashed_unix": 0}); err != nil {
		return err
	}
	return deleteClaimed(ctx, doer, id)
}

// deleteClaimed deletes the repository claimed by claimForPurge, closing the pull requests from its branches
// into other repositories like any permanent deletion.
func deleteClaimed(ctx context.Context, doer *user_model.User, id int64) error {
	repo, err := repo_model.GetRepositoryByIDIncludingTrashed(ctx, id)
	if err != nil {
		return err
	}
	return repo_service.DeleteRepositoryPermanently(ctx, doer, repo, false) // its webhooks fired when it was trashed
}

// claimForPurge marks the repository as being purged, minus the time of the claim, so that a second
// restore or purge finds nothing to claim. It stays hidden meanwhile.
func claimForPurge(ctx context.Context, id int64, cond builder.Cond) error {
	n, err := db.GetEngine(ctx).Where(builder.And(builder.Eq{"id": id}, cond)).
		Cols("trashed_unix").Update(&repo_model.Repository{TrashedUnix: -timeutil.TimeStampNow()})
	if err != nil {
		return err
	} else if n == 0 {
		return ErrTrashConflict
	}
	return nil
}

// PurgeExpired deletes the repositories whose retention is over, and retries purges that were claimed but
// never finished.
func PurgeExpired(ctx context.Context, doer *user_model.User) error {
	now := timeutil.TimeStampNow()
	expired := now.Add(-int64(setting.Trash.RetentionDays) * 86400)
	stale := now.Add(-int64(purgeRetryAfter / time.Second))

	var repos []*repo_model.Repository
	if err := db.GetEngine(ctx).Where(builder.Or(
		builder.And(builder.Gt{"trashed_unix": 0}, builder.Lte{"trashed_unix": expired}),
		builder.And(builder.Lt{"trashed_unix": 0}, builder.Gte{"trashed_unix": -stale}),
	)).Cols("id", "trashed_unix").Find(&repos); err != nil {
		return fmt.Errorf("find expired trash: %w", err)
	}
	for _, repo := range repos {
		// the claim only succeeds for whoever sees the same trashed_unix, so two pods never purge the same one
		if err := claimForPurge(ctx, repo.ID, builder.Eq{"trashed_unix": repo.TrashedUnix}); err != nil {
			if errors.Is(err, ErrTrashConflict) {
				continue
			}
			return err
		}
		if err := deleteClaimed(ctx, doer, repo.ID); err != nil {
			log.Error("purge trashed repository %d: %v", repo.ID, err)
		}
	}
	return nil
}
