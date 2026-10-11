// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repo

import (
	"errors"
	"net/http"

	repo_model "forgejo.org/models/repo"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/web"
	"forgejo.org/services/branchworkflow"
	"forgejo.org/services/context"
)

func renderBranchWorkflow(ctx *context.APIContext) {
	eff, err := branchworkflow.GetEffective(ctx, ctx.Repo().Repository)
	if err != nil {
		ctx.ServerError("GetEffective", err)
		return
	}
	res := &api.RepoBranchWorkflow{
		DeleteBranchAfterMerge:          eff.Configured.String(),
		EffectiveDeleteBranchAfterMerge: eff.Mode.String(),
		Source:                          eff.Source,
		Locked:                          eff.Locked,
	}
	if eff.Locked {
		res.LockedBy = eff.SetBy
	}
	ctx.JSON(http.StatusOK, res)
}

// GetBranchWorkflow returns the branch workflow settings of a repository
func GetBranchWorkflow(ctx *context.APIContext) {
	// swagger:operation GET /repos/{owner}/{repo}/branch-workflow repository repoGetBranchWorkflow
	// ---
	// summary: Get the branch workflow settings of a repository, with the effective value and where it comes from
	// produces:
	// - application/json
	// parameters:
	// - name: owner
	//   in: path
	//   description: owner of the repo
	//   type: string
	//   required: true
	// - name: repo
	//   in: path
	//   description: name of the repo
	//   type: string
	//   required: true
	// responses:
	//   "200":
	//     "$ref": "#/responses/RepoBranchWorkflow"
	//   "404":
	//     "$ref": "#/responses/notFound"

	renderBranchWorkflow(ctx)
}

// EditBranchWorkflow changes the branch workflow settings of a repository
func EditBranchWorkflow(ctx *context.APIContext) {
	// swagger:operation PUT /repos/{owner}/{repo}/branch-workflow repository repoEditBranchWorkflow
	// ---
	// summary: Change the branch workflow settings of a repository
	// consumes:
	// - application/json
	// produces:
	// - application/json
	// parameters:
	// - name: owner
	//   in: path
	//   description: owner of the repo
	//   type: string
	//   required: true
	// - name: repo
	//   in: path
	//   description: name of the repo
	//   type: string
	//   required: true
	// - name: body
	//   in: body
	//   required: true
	//   schema:
	//     "$ref": "#/definitions/EditRepoBranchWorkflowOption"
	// responses:
	//   "200":
	//     "$ref": "#/responses/RepoBranchWorkflow"
	//   "403":
	//     "$ref": "#/responses/forbidden"
	//   "404":
	//     "$ref": "#/responses/notFound"
	//   "422":
	//     "$ref": "#/responses/validationError"

	form := web.GetForm(ctx).(*api.EditRepoBranchWorkflowOption)
	mode, ok := repo_model.ParseDeleteBranchMode(form.DeleteBranchAfterMerge)
	if !ok {
		ctx.Error(http.StatusUnprocessableEntity, "DeleteBranchAfterMerge", "must be one of inherit, off, default-on, always")
		return
	}
	switch err := branchworkflow.SetRepo(ctx, ctx.Repo().Repository, mode); {
	case errors.Is(err, branchworkflow.ErrLocked):
		ctx.Error(http.StatusForbidden, "SetRepo", err)
	case errors.Is(err, branchworkflow.ErrNoPullRequests):
		ctx.Error(http.StatusUnprocessableEntity, "SetRepo", err)
	case err != nil:
		ctx.ServerError("SetRepo", err)
	default:
		renderBranchWorkflow(ctx)
	}
}
