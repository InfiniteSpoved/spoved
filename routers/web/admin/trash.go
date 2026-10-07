// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package admin

import (
	"errors"
	"html/template"
	"net/http"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/base"
	"forgejo.org/modules/setting"
	"forgejo.org/services/context"
	trash_service "forgejo.org/services/trash"
)

const tplTrash base.TplName = "admin/trash"

// Trash lists the repositories in the trash.
func Trash(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("admin.trash")
	ctx.Data["PageIsAdminTrash"] = true
	ctx.Data["RetentionDays"] = setting.Trash.RetentionDays

	keyword := ctx.FormTrim("q")
	page := max(ctx.FormInt("page"), 1)
	items, count, totalSize, err := trash_service.List(ctx, trash_service.ListOptions{
		ListOptions: db.ListOptions{Page: page, PageSize: setting.UI.Admin.RepoPagingNum},
		Keyword:     keyword,
	})
	if err != nil {
		ctx.ServerError("trash.List", err)
		return
	}
	ctx.Data["Items"] = items
	ctx.Data["Keyword"] = keyword
	ctx.Data["Total"] = count
	ctx.Data["TotalSize"] = totalSize

	pager := context.NewPagination(int(count), setting.UI.Admin.RepoPagingNum, page, 5)
	pager.AddParam(ctx, "q", "Keyword")
	ctx.Data["Page"] = pager

	ctx.HTML(http.StatusOK, tplTrash)
}

// trashedRepo returns the repository in the trash named by the :id path parameter, or writes the response.
func trashedRepo(ctx *context.Context) *repo_model.Repository {
	repo, err := repo_model.GetRepositoryByIDIncludingTrashed(ctx, ctx.ParamsInt64(":id"))
	if err != nil && !repo_model.IsErrRepoNotExist(err) {
		ctx.ServerError("GetRepositoryByIDIncludingTrashed", err)
		return nil
	}
	if err != nil || !repo.IsTrashed() {
		ctx.NotFound("trashedRepo", nil)
		return nil
	}
	return repo
}

func trashDone(ctx *context.Context, err error, success template.HTML) {
	switch {
	case errors.Is(err, trash_service.ErrTrashConflict):
		ctx.Flash.Error(ctx.Tr("admin.trash.conflict"))
	case err != nil:
		ctx.ServerError("trash", err)
		return
	default:
		ctx.Flash.Success(success)
	}
	ctx.Redirect(setting.AppSubURL + "/admin/trash")
}

// RestoreTrash takes a repository out of the trash.
func RestoreTrash(ctx *context.Context) {
	repo := trashedRepo(ctx)
	if repo == nil {
		return
	}
	trashDone(ctx, trash_service.Restore(ctx, ctx.Doer, repo.ID), ctx.Tr("admin.trash.restored", repo.TrashedOwnerName+"/"+repo.TrashedName))
}

// PurgeTrash deletes a repository in the trash for good, after its name was typed.
func PurgeTrash(ctx *context.Context) {
	repo := trashedRepo(ctx)
	if repo == nil {
		return
	}
	if ctx.FormString("confirm_name") != repo.TrashedOwnerName+"/"+repo.TrashedName {
		ctx.Flash.Error(ctx.Tr("admin.trash.confirm_mismatch"))
		ctx.Redirect(setting.AppSubURL + "/admin/trash")
		return
	}
	trashDone(ctx, trash_service.Purge(ctx, ctx.Doer, repo.ID), ctx.Tr("admin.trash.purged", repo.TrashedOwnerName+"/"+repo.TrashedName))
}
