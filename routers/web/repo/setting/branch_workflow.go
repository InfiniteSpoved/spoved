// Copyright 2026 The spoved contributors. All rights reserved.
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
	repo_service "forgejo.org/services/repository"
)

const tplBranchWorkflow base.TplName = "repo/settings/branch_workflow"

// BranchWorkflow renders the repository's branch workflow settings.
func BranchWorkflow(ctx *context.Context) {
	if !prepareBranchWorkflow(ctx) || !PrepareMergedBranches(ctx, repo_service.MergedBranchScope{Repo: ctx.Repo.Repository}, ctx.Repo.RepoLink+"/settings/branch_workflow", false) {
		return
	}
	ctx.HTML(http.StatusOK, tplBranchWorkflow)
}

// BranchWorkflowDeleteMerged renders the branch workflow settings with the merged branches that would be deleted.
func BranchWorkflowDeleteMerged(ctx *context.Context) {
	if !prepareBranchWorkflow(ctx) || !PrepareMergedBranches(ctx, repo_service.MergedBranchScope{Repo: ctx.Repo.Repository}, ctx.Repo.RepoLink+"/settings/branch_workflow", true) {
		return
	}
	ctx.HTML(http.StatusOK, tplBranchWorkflow)
}

// BranchWorkflowDeleteMergedPost deletes the repository's merged branches.
func BranchWorkflowDeleteMergedPost(ctx *context.Context) {
	DeleteMergedBranchesPost(ctx, repo_service.MergedBranchScope{Repo: ctx.Repo.Repository}, ctx.Repo.RepoLink+"/settings/branch_workflow")
}

// PrepareMergedBranches sets the data of the "delete merged branches" section of the settings page at back, and the
// branches it would delete when preview is set. It deletes nothing.
func PrepareMergedBranches(ctx *context.Context, scope repo_service.MergedBranchScope, back string, preview bool) bool {
	ctx.Data["MergedLink"] = back + "/delete_merged"
	ctx.Data["MergedBackLink"] = back
	ctx.Data["MergedShowRepos"] = scope.Repo == nil
	if !preview {
		return true
	}
	found, err := repo_service.FindMergedBranches(ctx, scope)
	if err != nil {
		ctx.ServerError("FindMergedBranches", err)
		return false
	}
	count := 0
	for _, f := range found {
		count += len(f.Branches)
	}
	ctx.Data["MergedPreview"] = true
	ctx.Data["MergedBranches"] = found
	ctx.Data["MergedCount"] = count
	ctx.Data["MaxDeleteMerged"] = repo_service.MaxDeleteMergedBranches
	return true
}

// DeleteMergedBranchesPost deletes the merged branches of the scope, at most MaxDeleteMergedBranches at once, and
// redirects to the settings page at back.
func DeleteMergedBranchesPost(ctx *context.Context, scope repo_service.MergedBranchScope, back string) {
	deleted, failed, more, err := repo_service.DeleteMergedBranches(ctx, ctx.Doer, scope)
	if err != nil {
		ctx.ServerError("DeleteMergedBranches", err)
		return
	}
	if more {
		ctx.Flash.Success(ctx.Tr("repo.settings.delete_merged.done_more", deleted))
	} else {
		ctx.Flash.Success(ctx.Tr("repo.settings.delete_merged.done", deleted))
	}
	if failed > 0 {
		ctx.Flash.Warning(ctx.Tr("repo.settings.delete_merged.failed", failed))
	}
	ctx.Redirect(back)
}

func prepareBranchWorkflow(ctx *context.Context) bool {
	ctx.Data["Title"] = ctx.Tr("repo.settings.branch_workflow")
	ctx.Data["PageIsSettingsBranchWorkflow"] = true
	eff, err := branchworkflow.GetEffective(ctx, ctx.Repo.Repository)
	if err != nil {
		ctx.ServerError("GetEffective", err)
		return false
	}
	shown := eff.Configured
	if eff.Locked {
		shown = eff.Mode
		ctx.Data["SetBy"] = branchworkflow.SetByText(ctx.Locale, eff)
	}
	ctx.Data["DeleteAfterMerge"] = shown.String()
	ctx.Data["Locked"] = eff.Locked
	ctx.Data["OwnerIsOrg"] = ctx.Repo.Owner.IsOrganization()
	return true
}

// BranchWorkflowPost saves the repository's branch workflow settings.
func BranchWorkflowPost(ctx *context.Context) {
	form := web.GetForm(ctx).(*forms.BranchWorkflowForm)
	mode, ok := repo_model.ParseDeleteBranchMode(form.DeleteAfterMerge)
	if !ok {
		ctx.Flash.Error(ctx.Tr("repo.settings.branch_workflow.invalid_value"))
		ctx.Redirect(ctx.Repo.RepoLink + "/settings/branch_workflow")
		return
	}
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
