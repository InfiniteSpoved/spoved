// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package admin

import (
	"errors"
	"net/http"

	repo_model "forgejo.org/models/repo"
	api "forgejo.org/modules/structs"
	"forgejo.org/routers/api/v1/utils"
	"forgejo.org/services/context"
	trash_service "forgejo.org/services/trash"
)

// ListTrash lists the repositories in the trash
func ListTrash(ctx *context.APIContext) {
	// swagger:operation GET /admin/trash admin adminListTrash
	// ---
	// summary: List the deleted repositories in the trash
	// produces:
	// - application/json
	// parameters:
	// - name: q
	//   in: query
	//   description: keyword to match in the owner or repository name
	//   type: string
	// - name: page
	//   in: query
	//   description: page number of results to return (1-based)
	//   type: integer
	// - name: limit
	//   in: query
	//   description: page size of results
	//   type: integer
	// responses:
	//   "200":
	//     "$ref": "#/responses/TrashItemList"
	//   "403":
	//     "$ref": "#/responses/forbidden"
	items, count, _, err := trash_service.List(ctx, trash_service.ListOptions{
		ListOptions: utils.GetListOptions(ctx),
		Keyword:     ctx.FormTrim("q"),
	})
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "List", err)
		return
	}
	res := make([]*api.TrashItem, len(items))
	for i, item := range items {
		res[i] = &api.TrashItem{
			Type:          "repo",
			ID:            item.Repo.ID,
			FullName:      item.Repo.TrashedOwnerName + "/" + item.Repo.TrashedName,
			DeletedBy:     item.Repo.TrashedByName,
			Deleted:       item.Repo.TrashedUnix.AsTime(),
			DaysRemaining: item.DaysRemaining,
			Size:          item.Repo.Size,
			Purging:       item.Purging,
		}
	}
	ctx.SetTotalCountHeader(count)
	ctx.JSON(http.StatusOK, res)
}

// trashedRepo checks the path and returns the repository in the trash, or writes the error response.
func trashedRepo(ctx *context.APIContext) *repo_model.Repository {
	if ctx.Params(":type") != "repo" {
		ctx.Error(http.StatusBadRequest, "type", "only repositories are in the trash")
		return nil
	}
	repo, err := repo_model.GetRepositoryByIDIncludingTrashed(ctx, ctx.ParamsInt64(":id"))
	if err != nil && !repo_model.IsErrRepoNotExist(err) {
		ctx.Error(http.StatusInternalServerError, "GetRepositoryByIDIncludingTrashed", err)
		return nil
	}
	if err != nil || !repo.IsTrashed() {
		ctx.NotFound()
		return nil
	}
	return repo
}

func trashError(ctx *context.APIContext, err error) {
	if errors.Is(err, trash_service.ErrTrashConflict) {
		ctx.Error(http.StatusConflict, "ErrTrashConflict", err)
		return
	}
	ctx.Error(http.StatusInternalServerError, "trash", err)
}

// RestoreTrash restores a repository from the trash
func RestoreTrash(ctx *context.APIContext) {
	// swagger:operation POST /admin/trash/{type}/{id}/restore admin adminRestoreTrash
	// ---
	// summary: Restore a deleted repository from the trash
	// parameters:
	// - name: type
	//   in: path
	//   description: the kind of item, "repo"
	//   type: string
	//   required: true
	// - name: id
	//   in: path
	//   description: id of the item
	//   type: integer
	//   format: int64
	//   required: true
	// responses:
	//   "204":
	//     "$ref": "#/responses/empty"
	//   "400":
	//     "$ref": "#/responses/error"
	//   "403":
	//     "$ref": "#/responses/forbidden"
	//   "404":
	//     "$ref": "#/responses/notFound"
	//   "409":
	//     "$ref": "#/responses/error"
	repo := trashedRepo(ctx)
	if repo == nil {
		return
	}
	if err := trash_service.Restore(ctx, ctx.Doer(), repo.ID); err != nil {
		trashError(ctx, err)
		return
	}
	ctx.Status(http.StatusNoContent)
}

// PurgeTrash deletes a repository in the trash for good
func PurgeTrash(ctx *context.APIContext) {
	// swagger:operation DELETE /admin/trash/{type}/{id} admin adminPurgeTrash
	// ---
	// summary: Delete a repository in the trash permanently
	// parameters:
	// - name: type
	//   in: path
	//   description: the kind of item, "repo"
	//   type: string
	//   required: true
	// - name: id
	//   in: path
	//   description: id of the item
	//   type: integer
	//   format: int64
	//   required: true
	// responses:
	//   "204":
	//     "$ref": "#/responses/empty"
	//   "400":
	//     "$ref": "#/responses/error"
	//   "403":
	//     "$ref": "#/responses/forbidden"
	//   "404":
	//     "$ref": "#/responses/notFound"
	//   "409":
	//     "$ref": "#/responses/error"
	repo := trashedRepo(ctx)
	if repo == nil {
		return
	}
	if err := trash_service.Purge(ctx, ctx.Doer(), repo.ID); err != nil {
		trashError(ctx, err)
		return
	}
	ctx.Status(http.StatusNoContent)
}
