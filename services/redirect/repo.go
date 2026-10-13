// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT
package redirect

import (
	"context"
	"errors"

	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
)

// LookupRepoRedirect returns the repository ID if there's a redirect registered for
// the ownerID repository name pair. It checks if the doer has permission to view
// the new repository.
func LookupRepoRedirect(ctx context.Context, doer *user_model.User, ownerID int64, repoName string) (int64, error) {
	redirectID, err := repo_model.GetRedirect(ctx, ownerID, 0, repoName)
	if err != nil {
		return 0, err
	}

	redirectRepo, err := repo_model.GetRepositoryByID(ctx, redirectID)
	if repo_model.IsErrRepoNotExist(err) { // e.g. in the trash
		return 0, repo_model.ErrRedirectNotExist{OwnerID: ownerID, RepoName: repoName}
	} else if err != nil {
		return 0, err
	}

	perm, err := access_model.GetUserRepoPermission(ctx, redirectRepo, doer)
	if err != nil {
		return 0, err
	}

	if !perm.HasAccess() {
		return 0, repo_model.ErrRedirectNotExist{OwnerID: ownerID, RepoName: repoName, MissingPermission: true}
	}

	return redirectID, nil
}

// LookupGroupRepoRedirect returns the repository that `owner/groupName/repoName` leads to now: the project may have
// been renamed (a project redirect), the repository renamed or moved away (a redirect in the project), or both. Like
// LookupRepoRedirect, a repository the doer can't see is ErrRedirectNotExist.
func LookupGroupRepoRedirect(ctx context.Context, doer, owner *user_model.User, groupName, repoName string) (*repo_model.Repository, error) {
	notExist := repo_model.ErrRedirectNotExist{OwnerID: owner.ID, RepoName: repoName}
	group, err := repo_model.GetRepoGroupByName(ctx, owner.ID, groupName)
	renamed := errors.Is(err, repo_model.ErrRepoGroupNotExist)
	if renamed {
		var groupID int64
		if groupID, err = repo_model.GetRepoGroupRedirect(ctx, owner.ID, groupName); err == nil {
			group, err = repo_model.GetRepoGroupByID(ctx, owner.ID, groupID)
		}
	}
	if errors.Is(err, repo_model.ErrRepoGroupNotExist) {
		return nil, notExist
	} else if err != nil {
		return nil, err
	}

	var repo *repo_model.Repository
	if renamed { // the repository may still be there
		repo, err = repo_model.GetRepositoryByPath(ctx, owner.ID, group.Name, repoName)
	}
	if !renamed || repo_model.IsErrRepoNotExist(err) {
		var repoID int64
		if repoID, err = repo_model.GetRedirect(ctx, owner.ID, group.ID, repoName); err == nil {
			repo, err = repo_model.GetRepositoryByID(ctx, repoID)
		}
	}
	if repo_model.IsErrRedirectNotExist(err) || repo_model.IsErrRepoNotExist(err) { // e.g. in the trash
		return nil, notExist
	} else if err != nil {
		return nil, err
	}

	perm, err := access_model.GetUserRepoPermission(ctx, repo, doer)
	if err != nil {
		return nil, err
	}
	if !perm.HasAccess() {
		return nil, notExist
	}
	return repo, nil
}
