// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package admin

import (
	"errors"
	"fmt"
	"net/http"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	org_model "forgejo.org/models/organization"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/base"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/web"
	"forgejo.org/services/context"
	"forgejo.org/services/forms"
	usergroup_service "forgejo.org/services/usergroup"
)

const (
	tplUserGroups    base.TplName = "admin/user_group/list"
	tplUserGroupNew  base.TplName = "admin/user_group/new"
	tplUserGroupView base.TplName = "admin/user_group/view"
)

func userGroupLink(g *org_model.UserGroup) string {
	return fmt.Sprintf("%s/admin/groups/%d", setting.AppSubURL, g.ID)
}

// UserGroups shows all user groups
func UserGroups(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("admin.user_groups")
	ctx.Data["PageIsAdminUserGroups"] = true

	page := max(ctx.FormInt("page"), 1)
	opts := &org_model.SearchUserGroupOptions{
		ListOptions: db.ListOptions{Page: page, PageSize: setting.UI.Admin.OrgPagingNum},
		Keyword:     ctx.FormTrim("q"),
	}
	groups, count, err := org_model.SearchUserGroups(ctx, opts)
	if err != nil {
		ctx.ServerError("SearchUserGroups", err)
		return
	}
	for _, g := range groups {
		if err := g.LoadCounts(ctx); err != nil {
			ctx.ServerError("LoadCounts", err)
			return
		}
	}

	ctx.Data["Groups"] = groups
	ctx.Data["Total"] = count
	ctx.Data["Keyword"] = opts.Keyword

	pager := context.NewPagination(int(count), opts.PageSize, opts.Page, 5)
	pager.SetDefaultParams(ctx)
	ctx.Data["Page"] = pager

	ctx.HTML(http.StatusOK, tplUserGroups)
}

// NewUserGroup renders the page to create a user group
func NewUserGroup(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("admin.user_groups.new")
	ctx.Data["PageIsAdminUserGroups"] = true
	ctx.HTML(http.StatusOK, tplUserGroupNew)
}

// NewUserGroupPost creates a user group
func NewUserGroupPost(ctx *context.Context) {
	form := web.GetForm(ctx).(*forms.UserGroupForm)
	ctx.Data["Title"] = ctx.Tr("admin.user_groups.new")
	ctx.Data["PageIsAdminUserGroups"] = true

	if ctx.HasError() {
		ctx.HTML(http.StatusOK, tplUserGroupNew)
		return
	}

	group := &org_model.UserGroup{Name: form.Name, Description: form.Description}
	if err := org_model.InsertUserGroup(ctx, group); err != nil {
		if org_model.IsErrUserGroupAlreadyExist(err) {
			ctx.Data["Err_Name"] = true
			ctx.RenderWithErr(ctx.Tr("admin.user_groups.name_taken"), tplUserGroupNew, form)
			return
		}
		ctx.ServerError("InsertUserGroup", err)
		return
	}

	ctx.Flash.Success(ctx.Tr("admin.user_groups.created", group.Name))
	ctx.Redirect(userGroupLink(group))
}

func getUserGroup(ctx *context.Context) *org_model.UserGroup {
	group, err := org_model.GetUserGroupByID(ctx, ctx.ParamsInt64(":id"))
	if err != nil {
		ctx.NotFoundOrServerError("GetUserGroupByID", org_model.IsErrUserGroupNotExist, err)
		return nil
	}
	return group
}

func prepareUserGroupView(ctx *context.Context, group *org_model.UserGroup) {
	ctx.Data["Title"] = group.Name
	ctx.Data["PageIsAdminUserGroups"] = true
	ctx.Data["Group"] = group
	ctx.Data["GroupLink"] = userGroupLink(group)

	members, err := org_model.GetUserGroupMembers(ctx, group.ID, db.ListOptions{})
	if err != nil {
		ctx.ServerError("GetUserGroupMembers", err)
		return
	}
	ctx.Data["Members"] = members

	memberSourceIDs, err := org_model.GetUserGroupMemberLoginSources(ctx, group.ID)
	if err != nil {
		ctx.ServerError("GetUserGroupMemberLoginSources", err)
		return
	}
	sourceNames := make(map[int64]string)
	memberSources := make(map[int64]string, len(memberSourceIDs))
	for uid, sourceID := range memberSourceIDs {
		name, ok := sourceNames[sourceID]
		if !ok {
			source, err := auth_model.GetSourceByID(ctx, sourceID)
			if err != nil && !auth_model.IsErrSourceNotExist(err) {
				ctx.ServerError("GetSourceByID", err)
				return
			} else if err == nil {
				name = source.Name
			}
			sourceNames[sourceID] = name
		}
		if name != "" {
			memberSources[uid] = name
		}
	}
	ctx.Data["MemberSources"] = memberSources

	teams, err := org_model.GetUserGroupTeams(ctx, group.ID)
	if err != nil {
		ctx.ServerError("GetUserGroupTeams", err)
		return
	}
	ctx.Data["LinkedTeams"] = teams
}

// ViewUserGroup shows a user group with its members and linked teams
func ViewUserGroup(ctx *context.Context) {
	group := getUserGroup(ctx)
	if ctx.Written() {
		return
	}
	prepareUserGroupView(ctx, group)
	if ctx.Written() {
		return
	}
	ctx.Data["name"] = group.Name
	ctx.Data["description"] = group.Description
	ctx.HTML(http.StatusOK, tplUserGroupView)
}

// EditUserGroupPost updates the name and description of a user group
func EditUserGroupPost(ctx *context.Context) {
	form := web.GetForm(ctx).(*forms.UserGroupForm)
	group := getUserGroup(ctx)
	if ctx.Written() {
		return
	}
	prepareUserGroupView(ctx, group)
	if ctx.Written() {
		return
	}

	if ctx.HasError() {
		ctx.HTML(http.StatusOK, tplUserGroupView)
		return
	}

	group.Name = form.Name
	group.Description = form.Description
	if err := org_model.UpdateUserGroup(ctx, group); err != nil {
		if org_model.IsErrUserGroupAlreadyExist(err) {
			ctx.Data["Err_Name"] = true
			ctx.RenderWithErr(ctx.Tr("admin.user_groups.name_taken"), tplUserGroupView, form)
			return
		}
		ctx.ServerError("UpdateUserGroup", err)
		return
	}

	ctx.Flash.Success(ctx.Tr("admin.user_groups.updated"))
	ctx.Redirect(userGroupLink(group))
}

// DeleteUserGroup deletes a user group
func DeleteUserGroup(ctx *context.Context) {
	group := getUserGroup(ctx)
	if ctx.Written() {
		return
	}

	if err := usergroup_service.DeleteGroup(ctx, group); err != nil {
		if org_model.IsErrLastOrgOwner(err) {
			ctx.Flash.Error(ctx.Tr("admin.user_groups.last_org_owner"))
			ctx.JSONRedirect(userGroupLink(group))
			return
		}
		ctx.ServerError("DeleteGroup", err)
		return
	}

	ctx.Flash.Success(ctx.Tr("admin.user_groups.deleted", group.Name))
	ctx.JSONRedirect(setting.AppSubURL + "/admin/groups")
}

// AddUserGroupMember adds a user to a user group
func AddUserGroupMember(ctx *context.Context) {
	group := getUserGroup(ctx)
	if ctx.Written() {
		return
	}

	user, err := user_model.GetUserByName(ctx, ctx.FormTrim("uname"))
	if err != nil {
		if user_model.IsErrUserNotExist(err) {
			ctx.Flash.Error(ctx.Tr("form.user_not_exist"))
			ctx.Redirect(userGroupLink(group))
			return
		}
		ctx.ServerError("GetUserByName", err)
		return
	}

	switch err := usergroup_service.AddMember(ctx, group, user); {
	case err == nil:
		ctx.Flash.Success(ctx.Tr("admin.user_groups.member_added", user.Name))
	case errors.Is(err, usergroup_service.ErrOrganizationCannotBeMember):
		ctx.Flash.Error(ctx.Tr("admin.user_groups.cannot_add_org"))
	case errors.Is(err, usergroup_service.ErrAlreadyMember):
		ctx.Flash.Error(ctx.Tr("admin.user_groups.already_member", user.Name))
	default:
		ctx.ServerError("AddMember", err)
		return
	}
	ctx.Redirect(userGroupLink(group))
}

// RemoveUserGroupMember removes a user from a user group
func RemoveUserGroupMember(ctx *context.Context) {
	group := getUserGroup(ctx)
	if ctx.Written() {
		return
	}

	if err := usergroup_service.RemoveMember(ctx, group, ctx.FormInt64("uid")); err != nil {
		if org_model.IsErrLastOrgOwner(err) {
			ctx.Flash.Error(ctx.Tr("admin.user_groups.last_org_owner"))
			ctx.Redirect(userGroupLink(group))
			return
		}
		ctx.ServerError("RemoveMember", err)
		return
	}
	ctx.Flash.Success(ctx.Tr("admin.user_groups.member_removed"))
	ctx.Redirect(userGroupLink(group))
}
