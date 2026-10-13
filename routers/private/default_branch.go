// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package private

import (
	"fmt"
	"net/http"

	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/gitrepo"
	"forgejo.org/modules/private"
	app_context "forgejo.org/services/context"
)

// SetDefaultBranch updates the default branch
func SetDefaultBranch(ctx *app_context.PrivateContext) {
	branch := ctx.Params(":branch")

	ctx.Repo.Repository.DefaultBranch = branch
	if err := gitrepo.SetDefaultBranch(ctx, ctx.Repo.Repository, ctx.Repo.Repository.DefaultBranch); err != nil {
		ctx.JSON(http.StatusInternalServerError, private.Response{
			Err: fmt.Sprintf("Unable to set default branch on repository: %s Error: %v", ctx.Repo.Repository.FullName(), err),
		})
		return
	}

	if err := repo_model.UpdateDefaultBranch(ctx, ctx.Repo.Repository); err != nil {
		ctx.JSON(http.StatusInternalServerError, private.Response{
			Err: fmt.Sprintf("Unable to set default branch on repository: %s Error: %v", ctx.Repo.Repository.FullName(), err),
		})
		return
	}
	ctx.PlainText(http.StatusOK, "success")
}
