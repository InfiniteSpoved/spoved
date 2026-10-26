// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package setting

import (
	"errors"
	"net/http"

	"forgejo.org/models/organization"
	"forgejo.org/modules/base"
	"forgejo.org/services/context"
	dependency_service "forgejo.org/services/dependency"
)

const tplCodeSecurity base.TplName = "repo/settings/code_security"

// CodeSecurity shows the repository's dependency graph switch, and which level decides it: the site administrator
// and the organization's owners get a link to the page where they can change it.
func CodeSecurity(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("repo.settings.code_security")
	ctx.Data["PageIsSettingsCodeSecurity"] = true
	repo := ctx.Repo.Repository
	sw, err := dependency_service.GetRepoSwitch(ctx, repo)
	if err != nil {
		ctx.ServerError("GetRepoSwitch", err)
		return
	}
	ctx.Data["Switch"] = sw
	ctx.Data["CanChange"] = sw.CanChange()
	switch {
	case !sw.Site:
		ctx.Data["CanChangeThere"] = ctx.Doer.IsAdmin
	case !sw.CanChange():
		owner, err := organization.OrgFromUser(repo.Owner).IsActingOwner(ctx, ctx.Doer)
		if err != nil {
			ctx.ServerError("IsActingOwner", err)
			return
		}
		ctx.Data["CanChangeThere"] = owner
	}
	ctx.HTML(http.StatusOK, tplCodeSecurity)
}

// CodeSecurityPost stores the switch; off deletes the stored package list. While the site or the organization
// decides, the save is refused with a message on the settings page, which then shows who decides.
func CodeSecurityPost(ctx *context.Context) {
	err := dependency_service.SetRepoSwitch(ctx, ctx.Repo.Repository, ctx.FormBool("enabled"))
	switch {
	case errors.Is(err, dependency_service.ErrSwitchLocked):
		ctx.Flash.Error(ctx.Tr("repo.settings.dependency_graph.locked"))
	case err != nil:
		ctx.ServerError("SetRepoSwitch", err)
		return
	default:
		ctx.Flash.Success(ctx.Tr("repo.settings.dependency_graph.saved"))
	}
	ctx.Redirect(ctx.Repo.RepoLink + "/settings/code_security")
}
