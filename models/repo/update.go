// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"context"
	"fmt"
	"time"

	"forgejo.org/models/db"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/util"
)

// UpdateRepositoryOwnerNames updates repository owner_names (this should only be used when the ownerName has changed case)
func UpdateRepositoryOwnerNames(ctx context.Context, ownerID int64, ownerName string) error {
	if ownerID == 0 {
		return nil
	}
	ctx, committer, err := db.TxContext(ctx)
	if err != nil {
		return err
	}
	defer committer.Close()

	if _, err := db.GetEngine(ctx).Where("owner_id = ?", ownerID).Cols("owner_name").Update(&Repository{
		OwnerName: ownerName,
	}); err != nil {
		return err
	}

	return committer.Commit()
}

// UpdateRepositoryUpdatedTime updates a repository's updated time
func UpdateRepositoryUpdatedTime(ctx context.Context, repoID int64, updateTime time.Time) error {
	_, err := db.GetEngine(ctx).Exec("UPDATE repository SET updated_unix = ? WHERE id = ?", updateTime.Unix(), repoID)
	return err
}

// UpdateRepositoryCols updates repository's columns
func UpdateRepositoryCols(ctx context.Context, repo *Repository, cols ...string) error {
	_, err := db.GetEngine(ctx).ID(repo.ID).Cols(cols...).Update(repo)
	return err
}

// ErrReachLimitOfRepo represents a "ReachLimitOfRepo" kind of error.
type ErrReachLimitOfRepo struct {
	Limit int
}

// IsErrReachLimitOfRepo checks if an error is a ErrReachLimitOfRepo.
func IsErrReachLimitOfRepo(err error) bool {
	_, ok := err.(ErrReachLimitOfRepo)
	return ok
}

func (err ErrReachLimitOfRepo) Error() string {
	return fmt.Sprintf("user has reached maximum limit of repositories [limit: %d]", err.Limit)
}

func (err ErrReachLimitOfRepo) Unwrap() error {
	return util.ErrPermissionDenied
}

// ErrRepoAlreadyExist represents a "RepoAlreadyExist" kind of error.
type ErrRepoAlreadyExist struct {
	Uname string
	Name  string
}

// IsErrRepoAlreadyExist checks if an error is a ErrRepoAlreadyExist.
func IsErrRepoAlreadyExist(err error) bool {
	_, ok := err.(ErrRepoAlreadyExist)
	return ok
}

func (err ErrRepoAlreadyExist) Error() string {
	return fmt.Sprintf("repository already exists [uname: %s, name: %s]", err.Uname, err.Name)
}

func (err ErrRepoAlreadyExist) Unwrap() error {
	return util.ErrAlreadyExist
}

// ErrRepoFilesAlreadyExist represents a "RepoFilesAlreadyExist" kind of error.
type ErrRepoFilesAlreadyExist struct {
	Uname string
	Name  string
}

// IsErrRepoFilesAlreadyExist checks if an error is a ErrRepoAlreadyExist.
func IsErrRepoFilesAlreadyExist(err error) bool {
	_, ok := err.(ErrRepoFilesAlreadyExist)
	return ok
}

func (err ErrRepoFilesAlreadyExist) Error() string {
	return fmt.Sprintf("repository files already exist [uname: %s, name: %s]", err.Uname, err.Name)
}

func (err ErrRepoFilesAlreadyExist) Unwrap() error {
	return util.ErrAlreadyExist
}

// CheckRepoCreationLimit returns ErrReachLimitOfRepo if u may not own another repository; repositories in
// the trash still count, so deleting and creating in a loop doesn't get around the limit.
func CheckRepoCreationLimit(ctx context.Context, doer, u *user_model.User) error {
	if doer.IsAdmin {
		return nil
	}
	trashed, err := db.GetEngine(ctx).Where("owner_id = ? AND trashed_unix <> 0", u.ID).Count(new(Repository))
	if err != nil {
		return err
	}
	if !u.CanCreateRepoWithTrashed(int(trashed)) {
		return ErrReachLimitOfRepo{u.MaxRepoCreation}
	}
	return nil
}

// CheckCreateRepository check if a repository can be created
func CheckCreateRepository(ctx context.Context, doer, u *user_model.User, name string) error {
	return CheckCreateRepositoryInGroup(ctx, doer, u, 0, name)
}

// CheckCreateRepositoryInGroup is CheckCreateRepository for a repository in the project groupID of u (0 = its
// root); a project that is not u's is ErrRepoGroupNotExist.
func CheckCreateRepositoryInGroup(ctx context.Context, doer, u *user_model.User, groupID int64, name string) error {
	groupName := ""
	if groupID != 0 {
		group, err := GetRepoGroupByID(ctx, u.ID, groupID)
		if err != nil {
			return err
		}
		groupName = group.Name
	}
	if err := CheckRepoCreationLimit(ctx, doer, u); err != nil {
		return err
	}

	if err := IsUsableRepoName(name); err != nil {
		return err
	}

	has, err := IsRepositoryModelExistInGroup(ctx, u.ID, groupID, name)
	if err != nil {
		return fmt.Errorf("IsRepositoryExist: %w", err)
	}
	isDir, err := util.IsDir(GroupRepoPath(u.Name, groupName, name))
	if err != nil {
		return fmt.Errorf("IsRepositoryExist: %w", err)
	} else if has || isDir {
		return ErrRepoAlreadyExist{u.Name, name}
	}

	return nil
}

// UpdateRepoSize stores the sizes of a repository; Size stays git + LFS
func UpdateRepoSize(ctx context.Context, repoID, gitSize, lfsSize, wikiSize int64) error {
	_, err := db.GetEngine(ctx).ID(repoID).Cols("size", "git_size", "lfs_size", "wiki_size").NoAutoTime().Update(&Repository{
		Size:     gitSize + lfsSize,
		GitSize:  gitSize,
		LFSSize:  lfsSize,
		WikiSize: wikiSize,
	})
	return err
}
