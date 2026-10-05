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

const tplSettingsBranchWorkflow base.TplName = "org/settings/branch_workflow"

// BranchWorkflow renders the organization's branch workflow settings.
func BranchWorkflow(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("org.settings.branch_workflow")
	ctx.Data["PageIsOrgSettings"] = true
	ctx.Data["PageIsOrgSettingsBranchWorkflow"] = true
	mode, locked, err := branchworkflow.GetOrg(ctx, ctx.Org.Organization.AsUser())
	if err != nil {
		ctx.ServerError("GetOrg", err)
		return
	}
	ctx.Data["DeleteAfterMerge"] = mode.String()
	ctx.Data["Enforce"] = locked
	ctx.HTML(http.StatusOK, tplSettingsBranchWorkflow)
}

// BranchWorkflowPost saves the organization's branch workflow settings.
func BranchWorkflowPost(ctx *context.Context) {
	form := web.GetForm(ctx).(*forms.BranchWorkflowForm)
	mode, _ := repo_model.ParseDeleteBranchMode(form.DeleteAfterMerge)
	switch err := branchworkflow.SetOrg(ctx, ctx.Org.Organization.AsUser(), mode, form.Enforce); {
	case errors.Is(err, branchworkflow.ErrLockWithoutValue):
		ctx.Flash.Error(ctx.Tr("org.settings.branch_workflow.enforce_needs_value"))
	case err != nil:
		ctx.ServerError("SetOrg", err)
		return
	default:
		ctx.Flash.Success(ctx.Tr("org.settings.update_setting_success"))
	}
	ctx.Redirect(ctx.Org.OrgLink + "/settings/branch_workflow")
}
