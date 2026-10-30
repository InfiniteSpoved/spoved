// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package org

import (
	"net/http"

	"forgejo.org/modules/base"
	shared_security "forgejo.org/routers/web/shared/security"
	shared_user "forgejo.org/routers/web/shared/user"
	"forgejo.org/services/context"
	dependency_service "forgejo.org/services/dependency"
)

const tplSecurity base.TplName = "org/security"

// MustEnableSecurity answers 404 (owners too) while the dependency graph is off for the site or the organization.
func MustEnableSecurity(ctx *context.Context) {
	if !dependency_service.OrgEnabled(ctx, ctx.Org.Organization.ID) {
		ctx.NotFound("MustEnableSecurity", nil)
	}
}

// SecurityIndex sends `/org/{org}/security` to its first tab.
func SecurityIndex(ctx *context.Context) {
	ctx.Redirect(ctx.Org.OrgLink + "/security/" + shared_security.Vulnerabilities)
}

func securityScope(ctx *context.Context) dependency_service.SearchScope {
	return dependency_service.SearchScope{Org: ctx.Org.Organization, Doer: ctx.Doer, Admin: ctx.IsUserSiteAdmin()}
}

// SecurityVulnerabilities searches the alerts of the organization's repositories, by advisory or package.
func SecurityVulnerabilities(ctx *context.Context) {
	securitySearch(ctx, shared_security.Vulnerabilities)
}

// SecurityDependencies searches the organization's dependencies by package.
func SecurityDependencies(ctx *context.Context) { securitySearch(ctx, shared_security.Dependencies) }

func securitySearch(ctx *context.Context, kind string) {
	if !shared_security.Search(ctx, securityScope(ctx), kind, ctx.Org.OrgLink+"/security/"+kind) {
		return
	}
	if err := shared_user.LoadHeaderCount(ctx); err != nil {
		ctx.ServerError("LoadHeaderCount", err)
		return
	}
	ctx.Data["Title"] = ctx.Tr("security_search.title")
	ctx.Data["PageIsOrgSecurity"] = true
	ctx.HTML(http.StatusOK, tplSecurity)
}

// SecurityVulnerabilitiesExport downloads the alert search as CSV or JSON.
func SecurityVulnerabilitiesExport(ctx *context.Context) {
	shared_security.Export(ctx, securityScope(ctx), shared_security.Vulnerabilities, ctx.Org.OrgLink+"/security/vulnerabilities")
}

// SecurityDependenciesExport downloads the dependency search as CSV or JSON.
func SecurityDependenciesExport(ctx *context.Context) {
	shared_security.Export(ctx, securityScope(ctx), shared_security.Dependencies, ctx.Org.OrgLink+"/security/dependencies")
}
