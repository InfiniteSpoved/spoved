// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repository

import (
	"context"
	"fmt"
	"os"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/log"
	"forgejo.org/modules/util"
)

// RenameRepoGroup renames a project and moves its directory. Like a repository rename, the directory moves
// before the database changes and back if they fail; the working-copy locks of all its repositories (trashed
// ones too) are held meanwhile, so no rename, transfer or trash restore of one of them runs in this process.
// Pushes don't take these locks: a push running during the rename fails (its path disappears), as it does
// during a repository rename.
func RenameRepoGroup(ctx context.Context, owner *user_model.User, group *repo_model.RepoGroup, newName string) (err error) {
	if group.OwnerID != owner.ID {
		return repo_model.ErrRepoGroupNotExist
	}
	if err := repo_model.IsUsableRepoName(newName); err != nil { // before the name reaches a path
		return err
	}
	var repoIDs []int64
	if err := db.GetEngine(ctx).Table("repository").Where("owner_id = ? AND group_id = ?", owner.ID, group.ID).
		OrderBy("id").Cols("id").Find(&repoIDs); err != nil {
		return err
	}
	for _, id := range repoIDs {
		defer repoWorkingPool.Lock(fmt.Sprint(id))()
	}

	oldPath, newPath := repo_model.GroupPath(owner.Name, group.Name), repo_model.GroupPath(owner.Name, newName)
	moved := false
	if oldPath != newPath {
		if isExist, err := util.IsExist(newPath); err != nil {
			return err
		} else if isExist {
			return repo_model.ErrRepoFilesAlreadyExist{Uname: owner.Name, Name: newName}
		}
		if isExist, err := util.IsExist(oldPath); err != nil {
			return err
		} else if isExist {
			if err := util.Rename(oldPath, newPath); err != nil {
				return fmt.Errorf("rename project directory: %w", err)
			}
			moved = true
		}
	}
	if err = repo_model.RenameRepoGroup(ctx, owner, group, newName); err != nil && moved {
		moveDirsBack(newPath, oldPath)
	}
	return err
}

// DeleteRepoGroup deletes an empty project and its directory.
func DeleteRepoGroup(ctx context.Context, owner *user_model.User, group *repo_model.RepoGroup) error {
	if group.OwnerID != owner.ID {
		return repo_model.ErrRepoGroupNotExist
	}
	if err := repo_model.DeleteRepoGroup(ctx, group); err != nil {
		return err
	}
	if err := os.Remove(repo_model.GroupPath(owner.Name, group.Name)); err != nil && !os.IsNotExist(err) {
		log.Error("Unable to remove the directory of project %s/%s: %v", owner.Name, group.Name, err)
	}
	return nil
}
