// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/db"
	actions_module "forgejo.org/modules/actions"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/storage"
	"forgejo.org/modules/timeutil"

	"xorm.io/builder"
)

// Cleanup removes expired actions runs, logs, data, artifacts and used ephemeral runners
func Cleanup(ctx context.Context) error {
	// clean up old runs first, so that the artifacts they leave pending deletion are removed below
	if err := CleanupRuns(ctx); err != nil {
		return fmt.Errorf("failed to clean up runs: %w", err)
	}

	// clean up expired artifacts
	if err := CleanupArtifacts(ctx); err != nil {
		return fmt.Errorf("failed to clean up artifacts: %w", err)
	}

	// clean up old logs
	if err := CleanupLogs(ctx); err != nil {
		return fmt.Errorf("failed to clean up logs: %w", err)
	}

	// clean up old ephemeral runners
	if err := CleanupEphemeralRunners(ctx); err != nil {
		return fmt.Errorf("failed to clean up old ephemeral runners: %w", err)
	}

	return nil
}

// CleanupArtifacts removes expired add need-deleted artifacts and set records expired status
func CleanupArtifacts(taskCtx context.Context) error {
	if err := cleanExpiredArtifacts(taskCtx); err != nil {
		return err
	}
	return cleanNeedDeleteArtifacts(taskCtx)
}

func cleanExpiredArtifacts(taskCtx context.Context) error {
	artifacts, err := actions_model.ListNeedExpiredArtifacts(taskCtx)
	if err != nil {
		return err
	}
	log.Info("Found %d expired artifacts", len(artifacts))
	for _, artifact := range artifacts {
		if err := actions_model.SetArtifactExpired(taskCtx, artifact.ID); err != nil {
			log.Error("Cannot set artifact %d expired: %v", artifact.ID, err)
			continue
		}
		if err := storage.ActionsArtifacts.Delete(artifact.StoragePath); err != nil {
			log.Error("Cannot delete artifact %d: %v", artifact.ID, err)
			continue
		}
		log.Info("Artifact %d set expired", artifact.ID)
	}
	return nil
}

// deleteArtifactBatchSize is the batch size of deleting artifacts
const deleteArtifactBatchSize = 100

func cleanNeedDeleteArtifacts(taskCtx context.Context) error {
	for {
		artifacts, err := actions_model.ListPendingDeleteArtifacts(taskCtx, deleteArtifactBatchSize)
		if err != nil {
			return err
		}
		log.Info("Found %d artifacts pending deletion", len(artifacts))
		for _, artifact := range artifacts {
			if err := actions_model.SetArtifactDeleted(taskCtx, artifact.ID); err != nil {
				log.Error("Cannot set artifact %d deleted: %v", artifact.ID, err)
				continue
			}
			if err := storage.ActionsArtifacts.Delete(artifact.StoragePath); err != nil {
				log.Error("Cannot delete artifact %d: %v", artifact.ID, err)
				continue
			}
			log.Info("Artifact %d set deleted", artifact.ID)
		}
		if len(artifacts) < deleteArtifactBatchSize {
			log.Debug("No more artifacts pending deletion")
			break
		}
	}
	return nil
}

const deleteRunBatchSize = 100

// CleanupRuns removes completed runs which are older than the effective retention time of their repository
// (actions_model.EffectiveRunRetentionDays), except kept runs and the latest run of each workflow on the default branch.
func CleanupRuns(ctx context.Context) error {
	repoDays, orgDays, err := actions_model.FindRunRetentionSettings(ctx)
	if err != nil {
		return fmt.Errorf("could not retrieve retention settings: %w", err)
	}
	// one scan for every repository: it reaches back as far as the shortest retention of any of them
	shortest := actions_model.EffectiveRunRetentionDays(minValue(repoDays), minValue(orgDays))
	if shortest == 0 {
		return nil
	}
	olderThan := actions_model.RetentionCutoff(shortest)

	keep, err := actions_model.GetLatestDefaultBranchRunIDs(ctx)
	if err != nil {
		return fmt.Errorf("could not retrieve latest runs: %w", err)
	}

	count := 0
	var afterID int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		runs, err := actions_model.FindOldDoneRuns(ctx, olderThan, afterID, deleteRunBatchSize)
		if err != nil {
			return fmt.Errorf("could not retrieve runs to delete: %w", err)
		}
		for _, run := range runs {
			afterID = run.ID
			days := actions_model.EffectiveRunRetentionDays(repoDays[run.RepoID], orgDays[run.OwnerID])
			if days == 0 || run.Created >= actions_model.RetentionCutoff(days) || keep.Contains(run.ID) {
				continue
			}
			if err := DeleteRunUnlessKept(ctx, run.ID); err != nil {
				if errors.Is(err, ErrRunKept) {
					continue
				}
				log.Error("Failed to delete run %d: %v", run.ID, err)
				// do not return error here, continue to next run
				continue
			}
			count++
		}
		if len(runs) < deleteRunBatchSize {
			break
		}
	}

	log.Info("Removed %d runs", count)
	return nil
}

// minValue returns the smallest value of m, or 0 if it is empty.
func minValue(m map[int64]int64) int64 {
	var smallest int64
	for _, v := range m {
		if smallest == 0 || v < smallest {
			smallest = v
		}
	}
	return smallest
}

const deleteLogBatchSize = 100

// CleanupLogs removes logs which are older than the configured retention time
func CleanupLogs(ctx context.Context) error {
	olderThan := timeutil.TimeStampNow().AddDuration(-time.Duration(setting.Actions.LogRetentionDays) * 24 * time.Hour)

	count := 0
	for {
		tasks, err := actions_model.FindOldTasksToExpire(ctx, olderThan, deleteLogBatchSize)
		if err != nil {
			return fmt.Errorf("could not retrieve tasks to expire: %w", err)
		}
		for _, task := range tasks {
			if task.HasLogs() {
				err = actions_module.RemoveLogs(ctx, task.LogInStorage, task.LogFilename)
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					log.Error("Failed to remove log %s (in storage %v) of task %v: %v",
						task.LogFilename, task.LogInStorage, task.ID, err)

					// do not return error here, continue to next task
					continue
				}
				log.Trace("Removed log %s of task %v", task.LogFilename, task.ID)
			}

			task.LogIndexes = nil // clear log indexes since it's a heavy field
			task.LogExpired = true
			if err := actions_model.UpdateTask(ctx, task, "log_indexes", "log_expired"); err != nil {
				log.Error("Failed to update task %v: %v", task.ID, err)
				// do not return error here, continue to next task
				continue
			}
			count++
		}
		if len(tasks) < deleteLogBatchSize {
			break
		}
	}

	log.Info("Removed %d logs", count)
	return nil
}

// CleanupEphemeralRunners removes used ephemeral runners which are no longer able to process jobs
func CleanupEphemeralRunners(ctx context.Context) error {
	var ids []int
	err := db.GetEngine(ctx).
		Table("`action_runner`").
		Select("DISTINCT `action_runner`.id").
		Join("INNER", "`action_task`", "`action_task`.`runner_id` = `action_runner`.`id`").
		Where(builder.Eq{"`action_runner`.`ephemeral`": true}).
		And(builder.In("`action_task`.`status`", actions_model.DoneStatuses())).
		Find(&ids)
	if err != nil {
		return fmt.Errorf("failed to find ephemeral runners: %w", err)
	}

	res, err := db.GetEngine(ctx).
		In("id", ids).
		Delete(&actions_model.ActionRunner{})
	if err != nil {
		return fmt.Errorf("failed to delete ephemeral runners: %w", err)
	}

	log.Info("Removed %d ephemeral runners", res)
	return nil
}

// CleanupEphemeralRunnersByPickedTaskOfRepo removes all ephemeral runners that have active/finished tasks on the given repository
func CleanupEphemeralRunnersByPickedTaskOfRepo(ctx context.Context, repoID int64) error {
	subQuery := builder.Select("`action_runner`.id").
		From(builder.Select("*").From("`action_runner`"), "`action_runner`"). // mysql needs this redundant subquery
		Join("INNER", "`action_task`", "`action_task`.`runner_id` = `action_runner`.`id`").
		Where(builder.And(builder.Eq{"`action_runner`.`ephemeral`": true}, builder.Eq{"`action_task`.`repo_id`": repoID}))
	b := builder.Delete(builder.In("id", subQuery)).From("`action_runner`")
	res, err := db.GetEngine(ctx).Exec(b)
	if err != nil {
		return fmt.Errorf("find runners: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	log.Info("Removed %d runners", affected)
	return nil
}

// CleanupOfflineRunners removes offline runners
func CleanupOfflineRunners(ctx context.Context, duration time.Duration, globalOnly bool) error {
	olderThan := timeutil.TimeStampNow().AddDuration(-duration)
	return actions_model.DeleteOfflineRunners(ctx, olderThan, globalOnly)
}
