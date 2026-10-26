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

const tplCodeSecurity base.TplName = "repo/settings/code_security"

// CodeSecurity shows the repository's dependency graph switch, and which level decides it.
func CodeSecurity(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("repo.settings.code_security")
	ctx.Data["PageIsSettingsCodeSecurity"] = true
	sw, err := dependency_service.GetRepoSwitch(ctx, ctx.Repo.Repository)
	if err != nil {
		ctx.ServerError("GetRepoSwitch", err)
		return
	}
	ctx.Data["Switch"] = sw
	ctx.Data["CanChange"] = sw.CanChange()
	ctx.HTML(http.StatusOK, tplCodeSecurity)
}

// CodeSecurityPost stores the switch; off deletes the stored package list. It is refused while the site or the
// organization decides.
func CodeSecurityPost(ctx *context.Context) {
	err := dependency_service.SetRepoSwitch(ctx, ctx.Repo.Repository, ctx.FormBool("enabled"))
	if errors.Is(err, dependency_service.ErrSwitchLocked) {
		ctx.Error(http.StatusForbidden, ctx.Locale.TrString("repo.settings.dependency_graph.locked"))
		return
	}
	if err != nil {
		ctx.ServerError("SetRepoSwitch", err)
		return
	}
	ctx.Flash.Success(ctx.Tr("repo.settings.update_settings_success"))
	ctx.Redirect(ctx.Repo.RepoLink + "/settings/code_security")
}
