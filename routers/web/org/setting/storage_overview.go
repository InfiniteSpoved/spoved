// Copyright 2024 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package setting

import (
	"net/http"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/base"
	"forgejo.org/modules/setting"
	"forgejo.org/routers/web/shared"
	"forgejo.org/services/context"

	"xorm.io/builder"
)

const (
	tplSettingsStorageOverview base.TplName = "org/settings/storage_overview"
)

// repoStorage is one row of the per-repository table
type repoStorage struct {
	Repo *repo_model.Repository
	repo_model.StorageStats
}

// StorageOverview render a size overview of the organization and its repositories, as well as relevant
// quota limits of the instance.
func StorageOverview(ctx *context.Context) {
	org := ctx.Org.Organization
	totals, err := repo_model.GetOwnerStorageTotals(ctx, org.ID)
	if err != nil {
		ctx.ServerError("GetOwnerStorageTotals", err)
		return
	}

	sortType := ctx.FormString("sort")
	switch sortType {
	case "size", "gitsize", "reversegitsize", "lfssize", "reverselfssize":
	default:
		sortType = "reversesize"
	}
	page := max(ctx.FormInt("page"), 1)
	opts := &repo_model.SearchRepoOptions{
		ListOptions: db.ListOptions{Page: page, PageSize: setting.UI.User.RepoPagingNum},
		OrderBy:     repo_model.OrderByFlatMap[sortType] + ", id ASC",
	}
	repos, count, err := repo_model.SearchRepositoryByCondition(ctx, opts, builder.Eq{"owner_id": org.ID}, false)
	if err != nil {
		ctx.ServerError("SearchRepositoryByCondition", err)
		return
	}
	rows := make([]repoStorage, len(repos))
	for i, repo := range repos {
		rows[i] = repoStorage{Repo: repo, StorageStats: repo_model.StorageStats{GitSize: repo.GitSize, LFSSize: repo.LFSSize}}
	}

	ctx.Data["OrgStorage"] = totals
	ctx.Data["RepoStorage"] = rows
	ctx.Data["SortType"] = sortType
	pager := context.NewPagination(int(count), opts.PageSize, page, 5)
	pager.SetDefaultParams(ctx)
	ctx.Data["Page"] = pager

	if !setting.Quota.Enabled { // without quotas the page shows only the repositories
		ctx.Data["Title"] = ctx.Tr("settings.storage_overview")
		ctx.Data["PageIsStorageOverview"] = true
		ctx.HTML(http.StatusOK, tplSettingsStorageOverview)
		return
	}
	shared.StorageOverview(ctx, org.ID, tplSettingsStorageOverview)
}
