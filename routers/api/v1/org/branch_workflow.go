// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package org

import (
	"errors"
	"net/http"

	repo_model "forgejo.org/models/repo"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/web"
	"forgejo.org/services/branchworkflow"
	"forgejo.org/services/context"
)

// GetBranchWorkflow returns the branch workflow settings of an organization
func GetBranchWorkflow(ctx *context.APIContext) {
	// swagger:operation GET /orgs/{org}/branch-workflow organization orgGetBranchWorkflow
	// ---
	// summary: Get the branch workflow settings of an organization
	// produces:
	// - application/json
	// parameters:
	// - name: org
	//   in: path
	//   description: name of the organization
	//   type: string
	//   required: true
	// responses:
	//   "200":
	//     "$ref": "#/responses/OrgBranchWorkflow"
	//   "403":
	//     "$ref": "#/responses/forbidden"
	//   "404":
	//     "$ref": "#/responses/notFound"

	mode, locked, err := branchworkflow.GetOrg(ctx, ctx.Org().Organization.AsUser())
	if err != nil {
		ctx.ServerError("GetOrg", err)
		return
	}
	ctx.JSON(http.StatusOK, &api.OrgBranchWorkflow{DeleteBranchAfterMerge: mode.String(), EnforceDeleteBranchAfterMerge: locked})
}

// EditBranchWorkflow changes the branch workflow settings of an organization
func EditBranchWorkflow(ctx *context.APIContext) {
	// swagger:operation PUT /orgs/{org}/branch-workflow organization orgEditBranchWorkflow
	// ---
	// summary: Change the branch workflow settings of an organization
	// consumes:
	// - application/json
	// produces:
	// - application/json
	// parameters:
	// - name: org
	//   in: path
	//   description: name of the organization
	//   type: string
	//   required: true
	// - name: body
	//   in: body
	//   required: true
	//   schema:
	//     "$ref": "#/definitions/OrgBranchWorkflow"
	// responses:
	//   "200":
	//     "$ref": "#/responses/OrgBranchWorkflow"
	//   "403":
	//     "$ref": "#/responses/forbidden"
	//   "404":
	//     "$ref": "#/responses/notFound"
	//   "422":
	//     "$ref": "#/responses/validationError"

	form := web.GetForm(ctx).(*api.OrgBranchWorkflow)
	mode, ok := repo_model.ParseDeleteBranchMode(form.DeleteBranchAfterMerge)
	if !ok {
		ctx.Error(http.StatusUnprocessableEntity, "DeleteBranchAfterMerge", "must be one of inherit, off, default-on, always")
		return
	}
	if err := branchworkflow.SetOrg(ctx, ctx.Org().Organization.AsUser(), mode, form.EnforceDeleteBranchAfterMerge); err != nil {
		if errors.Is(err, branchworkflow.ErrLockWithoutValue) {
			ctx.Error(http.StatusUnprocessableEntity, "EnforceDeleteBranchAfterMerge", err)
		} else {
			ctx.ServerError("SetOrg", err)
		}
		return
	}
	ctx.JSON(http.StatusOK, form)
}
