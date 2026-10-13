// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package private

import (
	"context"
	"fmt"
	"net/http"

	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/gitrepo"
	"forgejo.org/modules/log"
	"forgejo.org/modules/private"
	app_context "forgejo.org/services/context"
)

// This file contains common functions relating to setting the Repository for the internal routes

// RepoAssignment assigns the repository and gitrepository to the private context
func RepoAssignment(ctx *app_context.PrivateContext) context.CancelFunc {
	repo := loadRepository(ctx)
	if ctx.Written() {
		// Error handled in loadRepository
		return nil
	}

	gitRepo, err := gitrepo.OpenRepository(ctx, repo)
	if err != nil {
		log.Error("Failed to open repository: %s Error: %v", repo.FullName(), err)
		ctx.JSON(http.StatusInternalServerError, private.Response{
			Err: fmt.Sprintf("Failed to open repository: %s Error: %v", repo.FullName(), err),
		})
		return nil
	}

	ctx.Repo = &app_context.Repository{
		Repository: repo,
		GitRepo:    gitRepo,
	}

	// We opened it, we should close it
	cancel := func() {
		// If it's been set to nil then assume someone else has closed it.
		if ctx.Repo.GitRepo != nil {
			ctx.Repo.GitRepo.Close()
		}
	}

	return cancel
}

// loadRepository loads the repository of a hook by its ID, never by name: a repository in a project shares its
// owner and name with others.
func loadRepository(ctx *app_context.PrivateContext) *repo_model.Repository {
	repoID := ctx.ParamsInt64(":repoid")
	repo, err := repo_model.GetRepositoryByID(ctx, repoID)
	if err == nil {
		err = repo.LoadOwner(ctx)
	}
	if repo_model.IsErrRepoNotExist(err) {
		ctx.JSON(http.StatusNotFound, private.Response{UserMsg: "Repository not found"})
		return nil
	} else if err != nil {
		log.Error("Failed to get repository: %d Error: %v", repoID, err)
		ctx.JSON(http.StatusInternalServerError, private.Response{
			Err: fmt.Sprintf("Failed to get repository: %d Error: %v", repoID, err),
		})
		return nil
	}
	return repo
}
