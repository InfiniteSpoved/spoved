// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package admin

import (
	"net/http"

	"forgejo.org/modules/base"
	"forgejo.org/modules/setting"
	"forgejo.org/routers/common"
	shared_security "forgejo.org/routers/web/shared/security"
	"forgejo.org/services/auth"
	"forgejo.org/services/context"
	dependency_service "forgejo.org/services/dependency"
)

const tplSecuritySearch base.TplName = "admin/security_search"

const securitySearchLink = "/admin/config/dependency-graph/"

// AuditExportReq lets a usable site admin, or an auditor reading with their own credential, post a download of what
// they may read; it runs after the sign-in check. An auditor changes nothing: the download is audited.
func AuditExportReq(ctx *context.Context) {
	if !common.UsableAuditReader(ctx.Doer) || !ctx.Doer.IsAdmin && !(ctx.Doer.IsAuditor && auth.IsHeldByUser(ctx.Authentication)) {
		ctx.Error(http.StatusForbidden)
	}
}

func securitySearchScope(ctx *context.Context) dependency_service.SearchScope {
	return dependency_service.SearchScope{Doer: ctx.Doer, Admin: true}
}

// SecurityVulnerabilities searches the vulnerability alerts of every repository on the instance.
func SecurityVulnerabilities(ctx *context.Context) {
	securitySearch(ctx, shared_security.Vulnerabilities)
}

// SecurityDependencies searches the dependencies of every repository on the instance.
func SecurityDependencies(ctx *context.Context) { securitySearch(ctx, shared_security.Dependencies) }

func securitySearch(ctx *context.Context, kind string) {
	if !shared_security.Search(ctx, securitySearchScope(ctx), kind, setting.AppSubURL+securitySearchLink+kind) {
		return
	}
	ctx.Data["Title"] = ctx.Tr("security_search.title")
	ctx.Data["PageIsAdminConfig"] = true
	ctx.Data["PageIsAdminConfigDependencyGraphVulnerabilities"] = kind == shared_security.Vulnerabilities
	ctx.Data["PageIsAdminConfigDependencyGraphDependencies"] = kind == shared_security.Dependencies
	ctx.HTML(http.StatusOK, tplSecuritySearch)
}

// SecurityVulnerabilitiesExport downloads the alert search as CSV or JSON.
func SecurityVulnerabilitiesExport(ctx *context.Context) {
	shared_security.Export(ctx, securitySearchScope(ctx), shared_security.Vulnerabilities, setting.AppSubURL+securitySearchLink+shared_security.Vulnerabilities)
}

// SecurityDependenciesExport downloads the dependency search as CSV or JSON.
func SecurityDependenciesExport(ctx *context.Context) {
	shared_security.Export(ctx, securitySearchScope(ctx), shared_security.Dependencies, setting.AppSubURL+securitySearchLink+shared_security.Dependencies)
}
