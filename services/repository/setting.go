// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"
	"slices"

	"forgejo.org/models/db"
	"forgejo.org/models/organization"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"forgejo.org/modules/log"
	actions_service "forgejo.org/services/actions"
)

// UpdateRepositoryUnits updates a repository's units
func UpdateRepositoryUnits(ctx context.Context, repo *repo_model.Repository, units []repo_model.RepoUnit, deleteUnitTypes []unit.Type) (err error) {
	ctx, committer, err := db.TxContext(ctx)
	if err != nil {
		return err
	}
	defer committer.Close()

	// Delete existing settings of units before adding again
	for _, u := range units {
		deleteUnitTypes = append(deleteUnitTypes, u.Type)
	}

	if slices.Contains(deleteUnitTypes, unit.TypeActions) {
		if err := actions_service.CleanRepoScheduleTasks(ctx, repo, true); err != nil {
			log.Error("CleanRepoScheduleTasks: %v", err)
		}
	}

	for _, u := range units {
		if u.Type == unit.TypeActions {
			if err := actions_service.DetectAndHandleSchedules(ctx, repo); err != nil {
				log.Error("DetectAndHandleSchedules: %v", err)
			}
			break
		}
	}

	var stored []unit.Type
	if err = db.GetEngine(ctx).Table("repo_unit").Where("repo_id = ?", repo.ID).Cols("type").Find(&stored); err != nil {
		return err
	}
	after := slices.DeleteFunc(slices.Clone(stored), func(t unit.Type) bool { return slices.Contains(deleteUnitTypes, t) })
	for _, u := range units {
		after = append(after, u.Type)
	}
	if b, a := unitNames(stored), unitNames(after); !slices.Equal(b, a) {
		if err = repo_model.RecordRepo(ctx, "repo.units.changed", repo, map[string]any{
			"before": map[string]any{"units": b}, "after": map[string]any{"units": a},
		}); err != nil {
			return err
		}
	}

	if _, err = db.GetEngine(ctx).Where("repo_id = ?", repo.ID).In("type", deleteUnitTypes).Delete(new(repo_model.RepoUnit)); err != nil {
		return err
	}

	if len(units) > 0 {
		if err = db.Insert(ctx, units); err != nil {
			return err
		}
	}

	return committer.Commit()
}

// SetArchiveRepoState archives or unarchives repo, and records the change.
func SetArchiveRepoState(ctx context.Context, repo *repo_model.Repository, isArchived bool) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		if repo.IsArchived != isArchived {
			action := "repo.repository.unarchived"
			if isArchived {
				action = "repo.repository.archived"
			}
			if err := repo_model.RecordRepo(ctx, action, repo, nil); err != nil {
				return err
			}
		}
		return repo_model.SetArchiveRepoState(ctx, repo, isArchived)
	})
}

// unitNames are the sorted audit names of the unit types, without duplicates.
func unitNames(types []unit.Type) []string {
	names := make([]string, 0, len(types))
	for _, t := range types {
		names = append(names, organization.UnitName(t))
	}
	slices.Sort(names)
	return slices.Compact(names)
}
