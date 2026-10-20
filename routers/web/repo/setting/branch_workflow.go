// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package setting

import (
	"errors"
	"net/http"
	"strings"

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

	model, err := branchworkflow.GetEffectiveModel(ctx, ctx.Repo.Repository)
	if err != nil {
		ctx.ServerError("GetEffectiveModel", err)
		return
	}
	shownModel := model.Model
	if !model.Locked && model.Configured != nil {
		shownModel = *model.Configured
	}
	ctx.Data["ModelInherit"] = model.Configured == nil
	ctx.Data["ModelLocked"] = model.Locked
	if model.Source == branchworkflow.SourceOrg {
		ctx.Data["ModelSetBy"] = ctx.Locale.TrString("repo.pulls.delete_after_merge.set_by_org", model.SetBy)
	}
	if model.Org != nil {
		ctx.Data["ModelInheritDesc"] = ctx.Locale.TrString("repo.settings.branching_model.inherit_desc_org", ctx.Repo.Owner.Name)
	}
	ctx.Data["DevelopmentBranch"] = shownModel.DevelopmentBranch
	ctx.Data["ReleasePrefixes"] = strings.Join(shownModel.ReleasePrefixes, ", ")
	chain, err := branchworkflow.GetChain(ctx, ctx.Repo.Repository, model)
	if err != nil {
		ctx.ServerError("GetChain", err)
		return
	}
	ctx.Data["HasModel"] = len(model.Model.ReleasePrefixes) > 0
	if len(chain.Ignored) > maxIgnoredShown {
		ctx.Data["IgnoredMore"] = len(chain.Ignored) - maxIgnoredShown
		chain.Ignored = chain.Ignored[:maxIgnoredShown]
	}
	ctx.Data["Chain"] = chain
	ctx.HTML(http.StatusOK, tplBranchWorkflow)
}

// maxIgnoredShown caps the ignored branches the chain preview lists.
const maxIgnoredShown = 20

// BranchingModelPost saves the repository's branching model.
func BranchingModelPost(ctx *context.Context) {
	form := web.GetForm(ctx).(*forms.BranchingModelForm)
	var model *repo_model.BranchingModel
	if !form.Inherit || !ctx.Repo.Owner.IsOrganization() {
		model = &repo_model.BranchingModel{DevelopmentBranch: form.DevelopmentBranch, ReleasePrefixes: strings.Split(form.ReleasePrefixes, ",")}
	}
	err := branchworkflow.SetRepoModel(ctx, ctx.Repo.Repository, model)
	if value, ok := branchworkflow.IsInvalidBranchingModel(err); ok {
		ctx.Flash.Error(ctx.Tr("repo.settings.branching_model.invalid", value))
	} else {
		switch {
		case errors.Is(err, branchworkflow.ErrTooManyReleasePrefixes):
			ctx.Flash.Error(ctx.Tr("repo.settings.branching_model.too_many_prefixes"))
		case errors.Is(err, branchworkflow.ErrLocked):
			ctx.Error(http.StatusForbidden, ctx.Locale.TrString("repo.settings.branching_model.locked"))
			return
		case errors.Is(err, branchworkflow.ErrNoPullRequests):
			ctx.Flash.Error(ctx.Tr("repo.settings.branch_workflow.pulls_disabled"))
		case err != nil:
			ctx.ServerError("SetRepoModel", err)
			return
		default:
			ctx.Flash.Success(ctx.Tr("repo.settings.update_settings_success"))
		}
	}
	ctx.Redirect(ctx.Repo.RepoLink + "/settings/branch_workflow")
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
