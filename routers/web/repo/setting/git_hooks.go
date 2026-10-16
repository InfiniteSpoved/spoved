// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"net/http"

	"forgejo.org/modules/git"
	"forgejo.org/services/context"
	repo_service "forgejo.org/services/repository"
)

// GitHooks hooks of a repository
func GitHooks(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("repo.settings.githooks")
	ctx.Data["PageIsSettingsGitHooks"] = true

	hooks, err := ctx.Repo.GitRepo.Hooks()
	if err != nil {
		ctx.ServerError("Hooks", err)
		return
	}
	ctx.Data["Hooks"] = hooks

	ctx.HTML(http.StatusOK, tplGithooks)
}

// GitHooksEdit render for editing a hook of repository page
func GitHooksEdit(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("repo.settings.githooks")
	ctx.Data["PageIsSettingsGitHooks"] = true

	name := ctx.Params(":name")
	hook, err := ctx.Repo.GitRepo.GetHook(name)
	if err != nil {
		if err == git.ErrNotValidHook {
			ctx.NotFound("GetHook", err)
		} else {
			ctx.ServerError("GetHook", err)
		}
		return
	}
	ctx.Data["Hook"] = hook
	ctx.HTML(http.StatusOK, tplGithookEdit)
}

// GitHooksEditPost response for editing a git hook of a repository
func GitHooksEditPost(ctx *context.Context) {
	name := ctx.Params(":name")
	hook, err := ctx.Repo.GitRepo.GetHook(name)
	if err != nil {
		if err == git.ErrNotValidHook {
			ctx.NotFound("GetHook", err)
		} else {
			ctx.ServerError("GetHook", err)
		}
		return
	}
	if err = repo_service.UpdateGitHook(ctx, ctx.Repo.Repository, hook, ctx.FormString("content")); err != nil {
		ctx.ServerError("UpdateGitHook", err)
		return
	}
	ctx.Redirect(ctx.Repo.RepoLink + "/settings/hooks/git")
}
