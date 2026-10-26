// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package admin

import (
	"errors"
	"net/http"

	user_model "forgejo.org/models/user"
	vulnerability_model "forgejo.org/models/vulnerability"
	"forgejo.org/modules/base"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
	"forgejo.org/services/context"
	dependency_service "forgejo.org/services/dependency"
	vulnerability_service "forgejo.org/services/vulnerability"
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
	statuses, err := vulnerability_model.Statuses(ctx)
	if err != nil {
		ctx.ServerError("Statuses", err)
		return
	}
	ctx.Data["MirrorStatuses"] = statuses
	ctx.Data["MirrorURL"] = setting.DependencyGraph.OSVMirrorURL
	ctx.Data["MirrorInterval"] = setting.DependencyGraph.UpdateInterval.String()
	ctx.Data["MirrorRunning"] = vulnerability_service.Running()
	// the auditor reads the page; only a site admin changes anything
	ctx.Data["CanChange"] = ctx.Doer.IsAdmin
	ctx.HTML(http.StatusOK, tplDependencyGraph)
}

// DependencyGraphUpdateAdvisories starts an update of the advisory mirror ("Update now").
func DependencyGraphUpdateAdvisories(ctx *context.Context) {
	switch err := vulnerability_service.UpdateNow(ctx, ctx.Doer); {
	case err == nil:
		ctx.Flash.Success(ctx.Tr("admin.dependency_graph.advisories.started"))
	case errors.Is(err, vulnerability_service.ErrNoMirror):
		ctx.Flash.Error(ctx.Tr("admin.dependency_graph.advisories.no_mirror"))
	case errors.Is(err, vulnerability_service.ErrAlreadyRunning):
		ctx.Flash.Error(ctx.Tr("admin.dependency_graph.advisories.running"))
	case errors.Is(err, vulnerability_service.ErrTooSoon):
		ctx.Flash.Error(ctx.Tr("admin.dependency_graph.advisories.too_soon"))
	default:
		ctx.ServerError("UpdateNow", err)
		return
	}
	ctx.Redirect(setting.AppSubURL + "/admin/config/dependency-graph")
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
