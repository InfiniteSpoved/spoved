// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package setting

import (
	"net/http"

	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"forgejo.org/modules/setting"
	"forgejo.org/services/context"
)

// ActionsGeneral shows the general Actions settings of a repository.
func ActionsGeneral(ctx *context.Context) {
	actionsUnit, err := ctx.Repo.Repository.GetUnit(ctx, unit.TypeActions)
	if err != nil {
		ctx.ServerError("GetUnit", err)
		return
	}
	ctx.Data["Title"] = ctx.Tr("actions.actions")
	ctx.Data["PageType"] = "general"
	ctx.Data["PageIsActionsSettingsGeneral"] = true
	ctx.Data["DefaultTokenPermissions"] = actionsUnit.ActionsConfig().DefaultTokenPermissions
	ctx.Data["InstanceDefaultTokenPermissions"] = setting.Actions.DefaultTokenPermissions
	ctx.HTML(http.StatusOK, tplRepoSecrets)
}

// ActionsGeneralPost saves the general Actions settings of a repository.
func ActionsGeneralPost(ctx *context.Context) {
	permissions := ctx.FormString("default_token_permissions")
	if permissions != "" && permissions != "read" && permissions != "write" {
		ctx.Error(http.StatusBadRequest, "invalid default_token_permissions")
		return
	}
	actionsUnit, err := ctx.Repo.Repository.GetUnit(ctx, unit.TypeActions)
	if err != nil {
		ctx.ServerError("GetUnit", err)
		return
	}
	actionsUnit.ActionsConfig().DefaultTokenPermissions = permissions
	if err := repo_model.UpdateRepoUnit(ctx, actionsUnit); err != nil {
		ctx.ServerError("UpdateRepoUnit", err)
		return
	}
	ctx.Flash.Success(ctx.Tr("repo.settings.update_settings_success"))
	ctx.Redirect(ctx.Repo.RepoLink + "/settings/actions/general")
}
