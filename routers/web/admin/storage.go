// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package admin

import (
	"encoding/csv"
	"net/http"
	"strconv"

	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/base"
	"forgejo.org/modules/optional"
	"forgejo.org/modules/setting"
	"forgejo.org/services/context"
)

const tplStorage base.TplName = "admin/storage"

// Storage shows the storage used per repository owner, or exports it as CSV
func Storage(ctx *context.Context) {
	totals, err := repo_model.GetStorageTotals(ctx)
	if err != nil {
		ctx.ServerError("GetStorageTotals", err)
		return
	}

	opts := repo_model.FindOwnerStorageOptions{
		Keyword: ctx.FormTrim("q"),
		OrderBy: ctx.FormString("sort"),
	}
	if _, ok := repo_model.OwnerStorageOrderBy[opts.OrderBy]; !ok {
		opts.OrderBy = "reversesize"
	}
	ownerType := ctx.FormString("type")
	switch ownerType {
	case "user":
		opts.OwnerType = optional.Some(user_model.UserTypeIndividual)
	case "org":
		opts.OwnerType = optional.Some(user_model.UserTypeOrganization)
	default:
		ownerType = ""
	}

	if ctx.FormString("format") == "csv" {
		owners, _, err := repo_model.FindOwnerStorage(ctx, opts)
		if err != nil {
			ctx.ServerError("FindOwnerStorage", err)
			return
		}
		ctx.SetServeHeaders(&context.ServeHeaderOptions{ContentType: "text/csv", ContentTypeCharset: "utf-8", Filename: "storage.csv"})
		w := csv.NewWriter(ctx.Resp)
		_ = w.Write([]string{"owner", "type", "repos", "git_size", "lfs_size", "total_size", "percent"})
		for _, o := range owners {
			typ := "user"
			if o.Owner.IsOrganization() {
				typ = "org"
			}
			_ = w.Write([]string{
				o.Owner.Name, typ, strconv.FormatInt(o.NumRepos, 10),
				strconv.FormatInt(o.GitSize, 10), strconv.FormatInt(o.LFSSize, 10), strconv.FormatInt(o.Size(), 10),
				strconv.FormatFloat(o.Percent(totals), 'f', 2, 64),
			})
		}
		w.Flush()
		return
	}

	page := max(ctx.FormInt("page"), 1)
	opts.Page = page
	opts.PageSize = setting.UI.Admin.RepoPagingNum
	owners, count, err := repo_model.FindOwnerStorage(ctx, opts)
	if err != nil {
		ctx.ServerError("FindOwnerStorage", err)
		return
	}

	ctx.Data["Title"] = ctx.Tr("admin.storage")
	ctx.Data["PageIsAdminStorage"] = true
	ctx.Data["Totals"] = totals
	ctx.Data["AverageSize"] = totals.Size() / max(totals.NumRepos, 1)
	ctx.Data["Total"] = count
	ctx.Data["Owners"] = owners
	ctx.Data["Keyword"] = opts.Keyword
	ctx.Data["SortType"] = opts.OrderBy
	ctx.Data["OwnerType"] = ownerType

	pager := context.NewPagination(int(count), opts.PageSize, page, 5)
	pager.SetDefaultParams(ctx)
	pager.AddParam(ctx, "type", "OwnerType")
	ctx.Data["Page"] = pager

	ctx.HTML(http.StatusOK, tplStorage)
}
