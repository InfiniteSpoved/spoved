// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package setting

import (
	"errors"
	"net/http"

	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/base"
	"forgejo.org/modules/web"
	"forgejo.org/services/branchworkflow"
	"forgejo.org/services/context"
	"forgejo.org/services/forms"
)

const tplBranchWorkflow base.TplName = "repo/settings/branch_workflow"

// BranchWorkflow renders the repository's branch workflow settings.
func BranchWorkflow(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("repo.settings.branch_workflow")
	ctx.Data["PageIsSettingsBranchWorkflow"] = true
	eff, err := branchworkflow.GetEffective(ctx, ctx.Repo.Repository)
	if err != nil {
		ctx.ServerError("GetEffective", err)
		return
	}
	shown := eff.Configured
	if eff.Locked {
		shown = eff.Mode
		ctx.Data["SetBy"] = branchworkflow.SetByText(ctx.Locale, eff)
	}
	ctx.Data["DeleteAfterMerge"] = shown.String()
	ctx.Data["Locked"] = eff.Locked
	ctx.Data["OwnerIsOrg"] = ctx.Repo.Owner.IsOrganization()
	ctx.HTML(http.StatusOK, tplBranchWorkflow)
}

// BranchWorkflowPost saves the repository's branch workflow settings.
func BranchWorkflowPost(ctx *context.Context) {
	form := web.GetForm(ctx).(*forms.BranchWorkflowForm)
	mode, _ := repo_model.ParseDeleteBranchMode(form.DeleteAfterMerge)
	switch err := branchworkflow.SetRepo(ctx, ctx.Repo.Repository, mode); {
	case errors.Is(err, branchworkflow.ErrLocked):
		ctx.Error(http.StatusForbidden, ctx.Locale.TrString("repo.settings.branch_workflow.locked"))
		return
	case errors.Is(err, branchworkflow.ErrNoPullRequests):
		ctx.Flash.Error(ctx.Tr("repo.settings.branch_workflow.pulls_disabled"))
	case err != nil:
		ctx.ServerError("SetRepo", err)
		return
	default:
		ctx.Flash.Success(ctx.Tr("repo.settings.update_settings_success"))
	}
	ctx.Redirect(ctx.Repo.RepoLink + "/settings/branch_workflow")
}
