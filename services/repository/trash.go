// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repository

import (
	"context"
	"fmt"

	"forgejo.org/models"
	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/db"
	"forgejo.org/models/organization"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	code_indexer "forgejo.org/modules/indexer/code"
	issue_indexer "forgejo.org/modules/indexer/issues"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
	actions_service "forgejo.org/services/actions"
)

// TrashRepository moves repo to the trash: it keeps all data but is hidden from everyone but site admins until
// it is restored or purged (see services/trash). The row, its ID and its name stay, so the name stays reserved.
func TrashRepository(ctx context.Context, doer *user_model.User, repo *repo_model.Repository) error {
	// Stop what is running while the repository can still be loaded by the Actions code.
	runs, err := db.Find[actions_model.ActionRun](ctx, actions_model.FindRunOptions{RepoID: repo.ID, Status: actions_model.PendingStatuses()})
	if err != nil {
		return fmt.Errorf("find pending runs: %w", err)
	}
	for _, run := range runs {
		if err := actions_service.CancelRun(ctx, run); err != nil {
			return fmt.Errorf("cancel run %d: %w", run.ID, err)
		}
	}

	if err := db.WithTx(ctx, func(ctx context.Context) error {
		n, err := db.GetEngine(ctx).Where("id = ? AND trashed_unix = 0", repo.ID).
			Cols("trashed_unix", "trashed_by_id", "trashed_by_name", "trashed_owner_name", "trashed_name").
			Update(&repo_model.Repository{
				TrashedUnix:      timeutil.TimeStampNow(),
				TrashedByID:      doer.ID,
				TrashedByName:    doer.Name,
				TrashedOwnerName: repo.OwnerName,
				TrashedName:      repo.Name,
			})
		if err != nil {
			return err
		} else if n == 0 {
			return repo_model.ErrRepoNotExist{ID: repo.ID}
		}
		if _, err := db.Exec(ctx, "UPDATE `user` SET num_repos=num_repos-1 WHERE id=?", repo.OwnerID); err != nil {
			return err
		}
		if err := organization.AddTeamRepoCounts(ctx, repo.ID, -1); err != nil {
			return err
		}
		if err := models.DeleteRepositoryTransfer(ctx, repo.ID); err != nil {
			return err
		}
		// forget what was indexed, so that a restore indexes the code again
		if _, err := db.GetEngine(ctx).Where("repo_id = ? AND indexer_type = ?", repo.ID, repo_model.RepoIndexerTypeCode).Delete(new(repo_model.RepoIndexerStatus)); err != nil {
			return err
		}
		return repo_model.DeleteIndexerBranches(ctx, repo.ID)
	}); err != nil {
		return err
	}

	issue_indexer.DeleteRepoIssueIndexer(ctx, repo.ID)
	if setting.Indexer.RepoIndexerEnabled {
		code_indexer.UpdateRepoIndexer(repo) // the indexer drops the documents of a repository it can't load
	}
	return nil
}
