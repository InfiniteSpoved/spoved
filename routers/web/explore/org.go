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

	cats, filter, categoryID, ok := OrgCategoryFilter(ctx)
	if !ok {
		return
	}
	opts.CategoryID = categoryID
	if len(cats) > 0 && filter == "" {
		opts.GroupByCategory = true
		ctx.Data["GroupByCategory"] = true
		ctx.Data["OrgCategoryByID"] = org_model.CategoriesByID(cats)
	}
	if filter != "" {
		opts.ExtraParamStrings["category"] = filter
	}
	RenderUserSearch(ctx, opts, tplExploreUsers)
}

// OrgCategoryFilter reads ?category= ("" = all, "other" = no category, or a category ID) and sets OrgCategories and
// CategoryFilter for the templates. With no categories the parameter is ignored, so pages stay as they were. An
// unknown category is a 404, and ok is false.
func OrgCategoryFilter(ctx *context.Context) (cats []*org_model.OrgCategory, filter string, id optional.Option[int64], ok bool) {
	cats, err := org_model.FindOrgCategories(ctx)
	if err != nil {
		ctx.ServerError("FindOrgCategories", err)
		return nil, "", id, false
	}
	if len(cats) == 0 {
		return nil, "", id, true
	}
	ctx.Data["OrgCategories"] = cats
	filter = ctx.FormString("category")
	switch filter {
	case "":
	case "other":
		id = optional.Some[int64](0)
	default:
		n, _ := strconv.ParseInt(filter, 10, 64)
		if !slices.ContainsFunc(cats, func(c *org_model.OrgCategory) bool { return c.ID == n }) {
			ctx.NotFound("unknown category", nil)
			return nil, "", id, false
		}
		id = optional.Some(n)
	}
	ctx.Data["CategoryFilter"] = filter
	return cats, filter, id, true
}
