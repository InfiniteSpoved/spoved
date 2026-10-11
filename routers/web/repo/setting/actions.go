// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package setting

import (
	"net/http"
	"strings"

	actions_model "forgejo.org/models/actions"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"forgejo.org/modules/log"
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
	effective, err := actions_model.RepoRunRetentionDays(ctx, ctx.Repo.Repository)
	if err != nil {
		ctx.ServerError("RepoRunRetentionDays", err)
		return
	}
	ctx.Data["RunRetentionDays"] = actionsUnit.ActionsConfig().RunRetentionDays
	ctx.Data["EffectiveRunRetentionDays"] = effective
	ctx.Data["InstanceRunRetentionDays"] = setting.Actions.RunRetentionDays
	ctx.Data["InstanceRunRetentionMaxDays"] = setting.Actions.RunRetentionMaxDays
	ctx.Data["MaxRunRetentionDays"] = actions_model.MaxRunRetentionDays
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

// ActionsRetentionPost saves the number of days after which completed runs of a repository are deleted. It only
// changes this field of the Actions unit.
func ActionsRetentionPost(ctx *context.Context) {
	redirect := ctx.Repo.RepoLink + "/settings/actions/general"
	days, ok := actions_model.ParseRunRetentionDays(strings.TrimSpace(ctx.FormString("run_retention_days")))
	if !ok {
		ctx.Flash.Error(ctx.Tr("actions.settings.run_retention.invalid", actions_model.MaxRunRetentionDays))
		ctx.Redirect(redirect)
		return
	}
	actionsUnit, err := ctx.Repo.Repository.GetUnit(ctx, unit.TypeActions)
	if err != nil {
		ctx.ServerError("GetUnit", err)
		return
	}
	actionsUnit.ActionsConfig().RunRetentionDays = days
	if err := repo_model.UpdateRepoUnit(ctx, actionsUnit); err != nil {
		ctx.ServerError("UpdateRepoUnit", err)
		return
	}
	log.Info("User %d set the run retention of repository %d to %d days", ctx.Doer.ID, ctx.Repo.Repository.ID, days)
	ctx.Flash.Success(ctx.Tr("actions.settings.run_retention.saved"))
	ctx.Redirect(redirect)
}
