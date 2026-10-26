// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package setting

import (
	"errors"
	"net/http"

	"forgejo.org/modules/base"
	"forgejo.org/services/context"
	dependency_service "forgejo.org/services/dependency"
)

const tplSettingsCodeSecurity base.TplName = "org/settings/code_security"

// CodeSecurity shows the organization's dependency graph switch.
func CodeSecurity(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("org.settings.code_security")
	ctx.Data["PageIsOrgSettingsCodeSecurity"] = true
	sw, err := dependency_service.GetOrgSwitch(ctx, ctx.Org.Organization.ID)
	if err != nil {
		ctx.ServerError("GetOrgSwitch", err)
		return
	}
	ctx.Data["SiteEnabled"] = dependency_service.SiteEnabled(ctx)
	ctx.Data["CanChangeThere"] = ctx.Doer.IsAdmin // the site switch
	ctx.Data["Enabled"] = sw.On
	ctx.Data["Locked"] = sw.Locked
	ctx.HTML(http.StatusOK, tplSettingsCodeSecurity)
}

// CodeSecurityPost stores the switch; off deletes the stored package lists of every repository of the organization.
// While the site switch is off, the save is refused with a message on the settings page.
func CodeSecurityPost(ctx *context.Context) {
	err := dependency_service.SetOrgSwitch(ctx, ctx.Org.Organization.AsUser(), ctx.FormBool("enabled"), ctx.FormBool("locked"))
	switch {
	case errors.Is(err, dependency_service.ErrSwitchLocked):
		ctx.Flash.Error(ctx.Tr("repo.settings.dependency_graph.site_off"))
	case err != nil:
		ctx.ServerError("SetOrgSwitch", err)
		return
	default:
		ctx.Flash.Success(ctx.Tr("repo.settings.dependency_graph.saved"))
	}
	ctx.Redirect(ctx.Org.OrgLink + "/settings/code_security")
}
