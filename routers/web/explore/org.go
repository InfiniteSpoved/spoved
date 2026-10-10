// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package explore

import (
	"slices"
	"strconv"

	"forgejo.org/models/db"
	org_model "forgejo.org/models/organization"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/container"
	"forgejo.org/modules/optional"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/structs"
	"forgejo.org/services/context"
)

// Organizations render explore organizations page
func Organizations(ctx *context.Context) {
	if setting.Service.Explore.DisableOrganizationsPage {
		ctx.Redirect(setting.AppSubURL + "/explore")
		return
	}

	ctx.Data["UsersPageIsDisabled"] = setting.Service.Explore.DisableUsersPage
	ctx.Data["CodePageIsDisabled"] = setting.Service.Explore.DisableCodePage
	ctx.Data["Title"] = ctx.Tr("explore")
	ctx.Data["PageIsExplore"] = true
	ctx.Data["PageIsExploreOrganizations"] = true
	ctx.Data["IsRepoIndexerEnabled"] = setting.Indexer.RepoIndexerEnabled

	visibleTypes := []structs.VisibleType{structs.VisibleTypePublic}
	if ctx.Doer != nil {
		visibleTypes = append(visibleTypes, structs.VisibleTypeLimited, structs.VisibleTypePrivate)
	}

	supportedSortOrders := container.SetOf(
		"newest",
		"oldest",
		"alphabetically",
		"reversealphabetically",
	)
	sortOrder := ctx.FormString("sort")
	if sortOrder == "" {
		if supportedSortOrders.Contains(setting.UI.ExploreDefaultSort) {
			sortOrder = setting.UI.ExploreDefaultSort
		} else {
			sortOrder = "newest"
		}
		ctx.SetFormString("sort", sortOrder)
	}

	showArchived := ctx.FormBool("archived")
	ctx.Data["ShowArchived"] = showArchived
	opts := &user_model.SearchUserOptions{
		Actor:       ctx.Doer,
		Type:        user_model.UserTypeOrganization,
		ListOptions: db.ListOptions{PageSize: setting.UI.ExplorePagingNum},
		Visible:     visibleTypes,

		SupportedSortOrders: supportedSortOrders,
	}
	if !showArchived {
		opts.IsArchived = optional.Some(false)
	}
	opts.ExtraParamStrings = map[string]string{}
	if showArchived {
		opts.ExtraParamStrings["archived"] = "true"
	}

	// with no categories the page stays as it was and the category parameter is ignored
	cats, err := org_model.FindOrgCategories(ctx)
	if err != nil {
		ctx.ServerError("FindOrgCategories", err)
		return
	}
	if len(cats) > 0 {
		ctx.Data["OrgCategories"] = cats
		filter := ctx.FormString("category")
		switch filter {
		case "":
			opts.GroupByCategory = true
			byID := make(map[int64]*org_model.OrgCategory, len(cats))
			for _, c := range cats {
				byID[c.ID] = c
			}
			ctx.Data["GroupByCategory"] = true
			ctx.Data["OrgCategoryByID"] = byID
		case "other":
			opts.CategoryID = optional.Some[int64](0)
		default:
			id, _ := strconv.ParseInt(filter, 10, 64)
			if !slices.ContainsFunc(cats, func(c *org_model.OrgCategory) bool { return c.ID == id }) {
				ctx.NotFound("unknown category", nil)
				return
			}
			opts.CategoryID = optional.Some(id)
		}
		ctx.Data["CategoryFilter"] = filter
		if filter != "" {
			opts.ExtraParamStrings["category"] = filter
		}
	}
	RenderUserSearch(ctx, opts, tplExploreUsers)
}
