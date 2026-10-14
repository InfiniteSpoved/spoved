// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package admin

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"forgejo.org/models/auth"
	"forgejo.org/models/db"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/base"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/util"
	shared_user "forgejo.org/routers/web/shared/user"
	user_setting "forgejo.org/routers/web/user/setting"
	"forgejo.org/services/auth/oauth2app"
	"forgejo.org/services/context"
)

var (
	tplSettingsApplications          base.TplName = "admin/applications/list"
	tplSettingsOauth2ApplicationEdit base.TplName = "admin/applications/oauth2_edit"
)

func newOAuth2CommonHandlers() *user_setting.OAuth2CommonHandlers {
	return &user_setting.OAuth2CommonHandlers{
		OwnerID:            0,
		BasePathList:       fmt.Sprintf("%s/admin/applications", setting.AppSubURL),
		BasePathEditPrefix: fmt.Sprintf("%s/admin/applications/oauth2", setting.AppSubURL),
		TplAppEdit:         tplSettingsOauth2ApplicationEdit,
	}
}

// Applications render org applications page (for org, at the moment, there are only OAuth2 applications)
func Applications(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("settings.applications")
	ctx.Data["PageIsAdminApplications"] = true

	apps, err := db.Find[auth.OAuth2Application](ctx, auth.FindOAuth2ApplicationsOptions{
		IsGlobal: true,
	})
	if err != nil {
		ctx.ServerError("GetOAuth2ApplicationsByUserID", err)
		return
	}
	ctx.Data["Applications"] = apps
	ctx.Data["BuiltinApplications"] = auth.BuiltinApplications()
	ctx.Data["CanCreateOAuth2App"] = true // site admins always may
	shared_user.SetExpiryFormData(ctx.Base, setting.OAuth2AppSecretExpiry)

	// review list: apps owned by users and organizations, whose owners may no longer be allowed to create apps
	all, err := db.Find[auth.OAuth2Application](ctx, auth.FindOAuth2ApplicationsOptions{})
	if err != nil {
		ctx.ServerError("FindOAuth2Applications", err)
		return
	}
	var ownerIDs []int64
	for _, a := range all {
		if a.UID != 0 {
			ownerIDs = append(ownerIDs, a.UID)
		}
	}
	owners, err := user_model.GetUsersByIDs(ctx, ownerIDs)
	if err != nil {
		ctx.ServerError("GetUsersByIDs", err)
		return
	}
	ownerByID := make(map[int64]*user_model.User, len(owners))
	for _, o := range owners {
		ownerByID[o.ID] = o
	}
	p := oauth2app.Policy(ctx)
	type reviewRow struct {
		App     *auth.OAuth2Application
		Owner   *user_model.User // nil when the owner was deleted
		Allowed bool
	}
	var review []reviewRow
	for _, a := range all {
		if a.UID != 0 {
			review = append(review, reviewRow{a, ownerByID[a.UID], oauth2app.OwnerAllowed(p, ownerByID[a.UID])})
		}
	}
	ctx.Data["Review"] = review
	ctx.Data["Policy"] = p
	selected, err := user_model.GetUsersByIDs(ctx, p.OrgIDs)
	if err != nil {
		ctx.ServerError("GetUsersByIDs", err)
		return
	}
	var orgNames []string
	for _, o := range selected {
		orgNames = append(orgNames, o.Name)
	}
	ctx.Data["PolicyOrgNames"] = strings.Join(orgNames, "\n")
	ctx.HTML(http.StatusOK, tplSettingsApplications)
}

// ApplicationsPost response for adding an oauth2 application
func ApplicationsPost(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("settings.applications")
	ctx.Data["PageIsAdminApplications"] = true

	oa := newOAuth2CommonHandlers()
	oa.AddApp(ctx)
}

// EditApplication displays the given application
func EditApplication(ctx *context.Context) {
	ctx.Data["PageIsAdminApplications"] = true

	oa := newOAuth2CommonHandlers()
	oa.EditShow(ctx)
}

// EditApplicationPost response for editing oauth2 application
func EditApplicationPost(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("settings.applications")
	ctx.Data["PageIsAdminApplications"] = true

	oa := newOAuth2CommonHandlers()
	oa.EditSave(ctx)
}

// ApplicationsRegenerateSecret handles the post request for regenerating the secret
func ApplicationsRegenerateSecret(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("settings")
	ctx.Data["PageIsAdminApplications"] = true

	oa := newOAuth2CommonHandlers()
	oa.RegenerateSecret(ctx)
}

// DeleteApplication deletes the given oauth2 application
func DeleteApplication(ctx *context.Context) {
	app, err := auth.GetOAuth2ApplicationByID(ctx, ctx.ParamsInt64("id"))
	if err != nil {
		if auth.IsErrOAuthApplicationNotFound(err) {
			ctx.NotFound("Application not found", err)
			return
		}
		ctx.ServerError("GetOAuth2ApplicationByID", err)
		return
	}
	// the review list shows apps of every owner, so delete them as their owner
	oa := newOAuth2CommonHandlers()
	oa.OwnerID = app.UID
	oa.DeleteApp(ctx)
}

// ApplicationsPolicyPost stores who may register OAuth2 applications
func ApplicationsPolicyPost(ctx *context.Context) {
	err := oauth2app.Set(ctx, ctx.Doer, ctx.FormString("mode"), splitList(ctx.FormString("org_names")))
	if errors.Is(err, util.ErrInvalidArgument) {
		ctx.Error(http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		ctx.ServerError("Set", err)
		return
	}
	ctx.Flash.Success(ctx.Tr("admin.oauth2_app_policy.saved"))
	ctx.Redirect(setting.AppSubURL + "/admin/applications")
}

// TODO: revokes the grant with the given id
