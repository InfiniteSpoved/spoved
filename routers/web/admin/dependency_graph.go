// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package admin

import (
	"net/http"

	user_model "forgejo.org/models/user"
	"forgejo.org/modules/base"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
	"forgejo.org/services/context"
	dependency_service "forgejo.org/services/dependency"
)

const tplDependencyGraph base.TplName = "admin/dependency_graph"

// DependencyGraph shows the site switch of the dependency graph.
func DependencyGraph(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("admin.dependency_graph")
	ctx.Data["PageIsAdminConfig"] = true
	ctx.Data["PageIsAdminConfigDependencyGraph"] = true
	s := setting.Config().DependencyGraph.Value(ctx)
	ctx.Data["Enabled"] = s.Enabled()
	ctx.Data["FileDefault"] = setting.DependencyGraph.Enabled
	if s.UpdatedUnix > 0 {
		ctx.Data["UpdatedUnix"] = timeutil.TimeStamp(s.UpdatedUnix)
		ctx.Data["UpdatedBy"] = "-"
		if u, err := user_model.GetPossibleUserByID(ctx, s.UpdatedBy); err == nil {
			ctx.Data["UpdatedBy"] = u.Name
		}
	}
	ctx.HTML(http.StatusOK, tplDependencyGraph)
}

// DependencyGraphPost stores the site switch; off deletes the stored package lists.
func DependencyGraphPost(ctx *context.Context) {
	if err := dependency_service.SetSiteEnabled(ctx, ctx.Doer, ctx.FormBool("enabled")); err != nil {
		ctx.ServerError("SetSiteEnabled", err)
		return
	}
	ctx.Flash.Success(ctx.Tr("repo.settings.dependency_graph.saved"))
	ctx.Redirect(setting.AppSubURL + "/admin/config/dependency-graph")
}
