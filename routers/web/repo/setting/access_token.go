// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package setting

import (
	"net/http"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/routers/web/shared/user"
	"forgejo.org/services/context"
)

func repoTokenLevel(ctx *context.Context) user.OwnedTokenLevel {
	repo := ctx.Repo.Repository
	return user.OwnedTokenLevel{
		Opts:     auth_model.FindOwnedAccessTokensOptions{OwnerType: auth_model.AccessTokenOwnerRepo, OwnerID: repo.ID},
		Link:     ctx.Repo.RepoLink + "/settings/keys",
		CloneURL: repo.CloneLink().HTTPS,
	}
}

// loadRepoAccessTokens sets the access tokens of the repository and the organization and project tokens covering it.
func loadRepoAccessTokens(ctx *context.Context) bool {
	return user.LoadOwnedTokens(ctx, repoTokenLevel(ctx)) && user.LoadCoveringTokens(ctx, ctx.Repo.Repository)
}

// AccessTokenNew creates an access token of the repository and shows its value once
func AccessTokenNew(ctx *context.Context) {
	if user.CreateOwnedToken(ctx, repoTokenLevel(ctx), user.ReadOwnedTokenForm(ctx), true, nil); ctx.Written() {
		return
	}
	if loadKeysPage(ctx) {
		ctx.HTML(http.StatusOK, tplDeployKeys)
	}
}

// AccessTokenDelete revokes an access token of the repository
func AccessTokenDelete(ctx *context.Context) {
	user.DeleteOwnedToken(ctx, repoTokenLevel(ctx))
}
