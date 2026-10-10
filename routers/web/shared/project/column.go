// Copyright 2024 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package project

import (
	"errors"

	project_model "forgejo.org/models/project"
	"forgejo.org/modules/json"
	"forgejo.org/modules/util"
	"forgejo.org/services/context"
	project_service "forgejo.org/services/project"
)

// MoveColumns moves or keeps columns in a project and sorts them inside that project
func MoveColumns(ctx *context.Context) {
	// the repository routes take only that repository's projects, the owner routes only the owner's
	var project *project_model.Project
	var err error
	if ctx.Repo.Repository != nil {
		project, err = project_service.GetProjectByIDForRepo(ctx, ctx.ParamsInt64(":id"), ctx.Repo.Repository.ID)
	} else {
		project, err = project_service.GetProjectByIDForOwner(ctx, ctx.ParamsInt64(":id"), ctx.ContextUser.ID)
	}
	if err != nil {
		ctx.NotFoundOrServerError("GetProjectByID", func(err error) bool {
			return errors.Is(err, util.ErrInvalidArgument) || project_model.IsErrProjectNotExist(err)
		}, err)
		return
	}

	type movedColumnsForm struct {
		Columns []struct {
			ColumnID int64 `json:"columnID"`
			Sorting  int64 `json:"sorting"`
		} `json:"columns"`
	}

	form := &movedColumnsForm{}
	if err = json.NewDecoder(ctx.Req.Body).Decode(&form); err != nil {
		ctx.ServerError("DecodeMovedColumnsForm", err)
		return
	}

	sortedColumnIDs := make(map[int64]int64)
	for _, column := range form.Columns {
		sortedColumnIDs[column.Sorting] = column.ColumnID
	}

	if err = project_model.MoveColumnsOnProject(ctx, project.ID, sortedColumnIDs); err != nil {
		ctx.ServerError("MoveColumnsOnProject", err)
		return
	}

	ctx.JSONOK()
}
