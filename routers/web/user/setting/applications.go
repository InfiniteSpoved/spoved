// Copyright 2014 The Gogs Authors. All rights reserved.
// Copyright 2018 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"net/http"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	"forgejo.org/modules/base"
	"forgejo.org/modules/setting"
	"forgejo.org/services/context"
)

const (
	tplSettingsApplications base.TplName = "user/settings/applications"
)

// Applications renders the OAuth2 applications page
func Applications(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("settings.applications")
	ctx.Data["PageIsSettingsApplications"] = true

	loadApplicationsData(ctx)

	ctx.HTML(http.StatusOK, tplSettingsApplications)
}

func loadApplicationsData(ctx *context.Context) {
	var err error
	ctx.Data["EnableOAuth2"] = setting.OAuth2.Enabled
	ctx.Data["IsAdmin"] = ctx.Doer.IsAdmin
	if setting.OAuth2.Enabled {
		if !SetCanCreateOAuth2App(ctx, ctx.Doer.ID) {
			return
		}
		ctx.Data["Applications"], err = db.Find[auth_model.OAuth2Application](ctx, auth_model.FindOAuth2ApplicationsOptions{
			OwnerID: ctx.Doer.ID,
		})
		if err != nil {
			ctx.ServerError("GetOAuth2ApplicationsByUserID", err)
			return
		}
		ctx.Data["Grants"], err = auth_model.GetOAuth2GrantsByUserID(ctx, ctx.Doer.ID)
		if err != nil {
			ctx.ServerError("GetOAuth2GrantsByUserID", err)
			return
		}
		ctx.Data["EnableAdditionalGrantScopes"] = setting.OAuth2.EnableAdditionalGrantScopes
	}
}
