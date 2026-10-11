// Copyright 2017 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package org

import (
	"net/http"
	"net/url"

	"forgejo.org/models"
	"forgejo.org/models/organization"
	"forgejo.org/modules/setting"
	api "forgejo.org/modules/structs"
	"forgejo.org/routers/api/v1/user"
	"forgejo.org/routers/api/v1/utils"
	"forgejo.org/services/auth"
	"forgejo.org/services/context"
	"forgejo.org/services/convert"
)

// listMembers list an organization's members
func listMembers(ctx *context.APIContext, isMember, twoFactorDisabled bool) {
	opts := &organization.FindOrgMembersOpts{
		TwoFactorDisabled: twoFactorDisabled,
		Doer:              ctx.Doer(),
		IsDoerMember:      isMember,
		OrgID:             ctx.Org().Organization.ID,
		ListOptions:       utils.GetListOptions(ctx),
	}

	count, err := organization.CountOrgMembers(ctx, opts)
	if err != nil {
		ctx.InternalServerError(err)
		return
	}

	members, _, err := organization.FindOrgMembers(ctx, opts)
	if err != nil {
		ctx.InternalServerError(err)
		return
	}

	apiMembers := make([]*api.User, len(members))
	for i, member := range members {
		apiMembers[i] = convert.ToUser(ctx, member, ctx.Doer())
	}

	ctx.SetTotalCountHeader(count)
	ctx.JSON(http.StatusOK, apiMembers)
}

// ListMembers list an organization's members
func ListMembers(ctx *context.APIContext) {
	// swagger:operation GET /orgs/{org}/members organization orgListMembers
	// ---
	// summary: List an organization's members
	// produces:
	// - application/json
	// parameters:
	// - name: org
	//   in: path
	//   description: name of the organization
	//   type: string
	//   required: true
	// - name: page
	//   in: query
	//   description: page number of results to return (1-based)
	//   type: integer
	// - name: limit
	//   in: query
	//   description: page size of results
	//   type: integer
	// - name: filter
	//   in: query
	//   description: '`2fa_disabled` lists only the members without two-factor authentication (organization owners and site administrators only)'
	//   type: string
	//   enum: [2fa_disabled]
	// responses:
	//   "200":
	//     "$ref": "#/responses/UserList"
	//   "403":
	//     "$ref": "#/responses/forbidden"
	//   "404":
	//     "$ref": "#/responses/notFound"
	//   "422":
	//     "$ref": "#/responses/validationError"

	var (
		isMember bool
		err      error
	)

	// who may ask is decided before the value is looked at, so the parameter is no oracle
	twoFactorDisabled := false
	if ctx.Req.URL.Query().Has("filter") {
		allowed := ctx.Doer().IsAdmin
		if !allowed {
			allowed, err = ctx.Org().Organization.IsActingOwner(ctx, ctx.Doer())
			if err != nil {
				ctx.Error(http.StatusInternalServerError, "IsActingOwner", err)
				return
			}
		}
		if !allowed {
			ctx.Error(http.StatusForbidden, "", "Must be an organization owner")
			return
		}
		if ctx.FormString("filter") != "2fa_disabled" {
			ctx.Error(http.StatusUnprocessableEntity, "", "unknown filter")
			return
		}
		twoFactorDisabled = true
	}

	if ctx.Doer() != nil {
		isMember, err = ctx.Org().Organization.IsOrgMember(ctx, ctx.Doer().ID)
		if err != nil {
			ctx.Error(http.StatusInternalServerError, "IsOrgMember", err)
			return
		}
	}
	// the auditor role lists concealed members too
	listMembers(ctx, isMember || (ctx.Doer() != nil && ctx.Doer().IsAuditor && auth.IsHeldByUser(ctx.Authentication())), twoFactorDisabled)
}

// ListPublicMembers list an organization's public members
func ListPublicMembers(ctx *context.APIContext) {
	// swagger:operation GET /orgs/{org}/public_members organization orgListPublicMembers
	// ---
	// summary: List an organization's public members
	// parameters:
	// - name: org
	//   in: path
	//   description: name of the organization
	//   type: string
	//   required: true
	// - name: page
	//   in: query
	//   description: page number of results to return (1-based)
	//   type: integer
	// - name: limit
	//   in: query
	//   description: page size of results
	//   type: integer
	// produces:
	// - application/json
	// responses:
	//   "200":
	//     "$ref": "#/responses/UserList"
	//   "404":
	//     "$ref": "#/responses/notFound"

	listMembers(ctx, false, false)
}

// IsMember check if a user is a member of an organization
func IsMember(ctx *context.APIContext) {
	// swagger:operation GET /orgs/{org}/members/{username} organization orgIsMember
	// ---
	// summary: Check if a user is a member of an organization
	// parameters:
	// - name: org
	//   in: path
	//   description: name of the organization
	//   type: string
	//   required: true
	// - name: username
	//   in: path
	//   description: username of the user
	//   type: string
	//   required: true
	// responses:
	//   "204":
	//     description: user is a member
	//   "303":
	//     description: redirection to /orgs/{org}/public_members/{username}
	//   "404":
	//     description: user is not a member

	userToCheck := user.GetUserByParams(ctx)
	if ctx.Written() {
		return
	}
	if ctx.Doer() != nil {
		userIsMember, err := ctx.Org().Organization.IsOrgMember(ctx, ctx.Doer().ID)
		if err != nil {
			ctx.Error(http.StatusInternalServerError, "IsOrgMember", err)
			return
		} else if userIsMember || ctx.IsUserSiteAdmin() {
			userToCheckIsMember, err := ctx.Org().Organization.IsOrgMember(ctx, userToCheck.ID)
			if err != nil {
				ctx.Error(http.StatusInternalServerError, "IsOrgMember", err)
			} else if userToCheckIsMember {
				ctx.Status(http.StatusNoContent)
			} else {
				ctx.NotFound()
			}
			return
		} else if ctx.Doer().ID == userToCheck.ID {
			ctx.NotFound()
			return
		}
	}

	redirectURL := setting.AppSubURL + "/api/v1/orgs/" + url.PathEscape(ctx.Org().Organization.Name) + "/public_members/" + url.PathEscape(userToCheck.Name)
	ctx.Redirect(redirectURL)
}

// IsPublicMember check if a user is a public member of an organization
func IsPublicMember(ctx *context.APIContext) {
	// swagger:operation GET /orgs/{org}/public_members/{username} organization orgIsPublicMember
	// ---
	// summary: Check if a user is a public member of an organization
	// parameters:
	// - name: org
	//   in: path
	//   description: name of the organization
	//   type: string
	//   required: true
	// - name: username
	//   in: path
	//   description: username of the user
	//   type: string
	//   required: true
	// responses:
	//   "204":
	//     description: user is a public member
	//   "404":
	//     description: user is not a public member

	userToCheck := user.GetUserByParams(ctx)
	if ctx.Written() {
		return
	}
	is, err := organization.IsPublicMembership(ctx, ctx.Org().Organization.ID, userToCheck.ID)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "IsPublicMembership", err)
		return
	}
	if is {
		ctx.Status(http.StatusNoContent)
	} else {
		ctx.NotFound()
	}
}

// PublicizeMember make a member's membership public
func PublicizeMember(ctx *context.APIContext) {
	// swagger:operation PUT /orgs/{org}/public_members/{username} organization orgPublicizeMember
	// ---
	// summary: Publicize a user's membership
	// produces:
	// - application/json
	// parameters:
	// - name: org
	//   in: path
	//   description: name of the organization
	//   type: string
	//   required: true
	// - name: username
	//   in: path
	//   description: username of the user
	//   type: string
	//   required: true
	// responses:
	//   "204":
	//     description: membership publicized
	//   "403":
	//     "$ref": "#/responses/forbidden"
	//   "404":
	//     "$ref": "#/responses/notFound"

	userToPublicize := user.GetUserByParams(ctx)
	if ctx.Written() {
		return
	}
	if userToPublicize.ID != ctx.Doer().ID {
		ctx.Error(http.StatusForbidden, "", "Cannot publicize another member")
		return
	}
	err := organization.ChangeOrgUserStatus(ctx, ctx.Org().Organization.ID, userToPublicize.ID, true)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "ChangeOrgUserStatus", err)
		return
	}
	ctx.Status(http.StatusNoContent)
}

// ConcealMember make a member's membership not public
func ConcealMember(ctx *context.APIContext) {
	// swagger:operation DELETE /orgs/{org}/public_members/{username} organization orgConcealMember
	// ---
	// summary: Conceal a user's membership
	// produces:
	// - application/json
	// parameters:
	// - name: org
	//   in: path
	//   description: name of the organization
	//   type: string
	//   required: true
	// - name: username
	//   in: path
	//   description: username of the user
	//   type: string
	//   required: true
	// responses:
	//   "204":
	//     "$ref": "#/responses/empty"
	//   "403":
	//     "$ref": "#/responses/forbidden"
	//   "404":
	//     "$ref": "#/responses/notFound"

	userToConceal := user.GetUserByParams(ctx)
	if ctx.Written() {
		return
	}
	if userToConceal.ID != ctx.Doer().ID {
		ctx.Error(http.StatusForbidden, "", "Cannot conceal another member")
		return
	}
	err := organization.ChangeOrgUserStatus(ctx, ctx.Org().Organization.ID, userToConceal.ID, false)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "ChangeOrgUserStatus", err)
		return
	}
	ctx.Status(http.StatusNoContent)
}

// DeleteMember remove a member from an organization
func DeleteMember(ctx *context.APIContext) {
	// swagger:operation DELETE /orgs/{org}/members/{username} organization orgDeleteMember
	// ---
	// summary: Remove a member from an organization
	// produces:
	// - application/json
	// parameters:
	// - name: org
	//   in: path
	//   description: name of the organization
	//   type: string
	//   required: true
	// - name: username
	//   in: path
	//   description: username of the user
	//   type: string
	//   required: true
	// responses:
	//   "204":
	//     description: member removed
	//   "404":
	//     "$ref": "#/responses/notFound"
	//   "409":
	//     "$ref": "#/responses/error"

	member := user.GetUserByParams(ctx)
	if ctx.Written() {
		return
	}
	if member.ID != ctx.Doer().ID {
		// a member of a user group linked to a team of the organization would be added back by the group
		if viaGroup, err := organization.IsUserInOrgUserGroupTeam(ctx, ctx.Org().Organization.ID, member.ID); err != nil {
			ctx.Error(http.StatusInternalServerError, "IsUserInOrgUserGroupTeam", err)
			return
		} else if viaGroup {
			ctx.Error(http.StatusConflict, "", "the user is a member through a user group linked to a team of the organization")
			return
		}
	}
	if err := models.RemoveOrgUser(ctx, ctx.Org().Organization.ID, member.ID); err != nil {
		ctx.Error(http.StatusInternalServerError, "RemoveOrgUser", err)
		return
	}
	ctx.Status(http.StatusNoContent)
}
