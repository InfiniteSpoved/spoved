// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repo

import (
	"errors"
	"net/http"
	"strconv"

	repo_model "forgejo.org/models/repo"
	vulnerability_model "forgejo.org/models/vulnerability"
	"forgejo.org/modules/optional"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/util"
	"forgejo.org/routers/api/v1/utils"
	"forgejo.org/services/context"
	dependency_service "forgejo.org/services/dependency"
	vulnerability_service "forgejo.org/services/vulnerability"
)

func toDependencyRepository(r *repo_model.Repository) api.DependencyRepository {
	return api.DependencyRepository{ID: r.ID, FullName: r.FullName(), HTMLURL: r.HTMLURL(), Private: r.IsPrivate}
}

// searchFailed answers a search error: the caller's mistakes are 422.
func searchFailed(ctx *context.APIContext, err error) {
	if errors.Is(err, util.ErrInvalidArgument) {
		ctx.Error(http.StatusUnprocessableEntity, "", err.Error())
		return
	}
	ctx.Error(http.StatusInternalServerError, "search", err)
}

// SearchDependencyUsage answers the dependency search of the org and instance routes. The route's gate has decided
// who may ask; scope says which repositories the answer covers.
func SearchDependencyUsage(ctx *context.APIContext, scope dependency_service.SearchScope) {
	if !dependency_service.SiteEnabled(ctx) {
		ctx.NotFound()
		return
	}
	f := dependency_service.UsageFilter{
		Ecosystem: ctx.FormString("ecosystem"), Name: ctx.FormString("name"), Version: ctx.FormString("version"), Range: ctx.FormString("range"),
	}
	if v := ctx.FormString("direct"); v != "" {
		direct, err := strconv.ParseBool(v)
		if err != nil {
			ctx.Error(http.StatusUnprocessableEntity, "", "direct must be a boolean")
			return
		}
		f.Direct = optional.Some(direct)
	}
	scope.Project, scope.Owner = ctx.FormString("project"), ctx.FormString("owner")
	lo := utils.GetListOptions(ctx)
	rows, total, err := dependency_service.SearchUsage(ctx, scope, f, lo)
	if err != nil {
		searchFailed(ctx, err)
		return
	}
	out := make([]*api.DependencyUsage, 0, len(rows))
	for _, r := range rows {
		out = append(out, &api.DependencyUsage{
			Repository: toDependencyRepository(r.Repo), Ecosystem: r.Ecosystem, Name: r.Name, Version: r.Version, Requirement: r.Requirement,
			ManifestPath: r.ManifestPath, Direct: r.Direct, Uncertain: r.Uncertain,
		})
	}
	ctx.SetLinkHeader(int(total), lo.PageSize)
	ctx.SetTotalCountHeader(total)
	ctx.JSON(http.StatusOK, out)
}

// SearchVulnerabilityAlerts answers the alert search of the org and instance routes, as SearchDependencyUsage does.
func SearchVulnerabilityAlerts(ctx *context.APIContext, scope dependency_service.SearchScope) {
	if !dependency_service.SiteEnabled(ctx) {
		ctx.NotFound()
		return
	}
	f := vulnerability_service.AlertFilter{
		Advisory: ctx.FormString("advisory"), Ecosystem: ctx.FormString("ecosystem"), Name: ctx.FormString("name"), Version: ctx.FormString("version"),
		State: ctx.FormString("state"), Severity: ctx.FormString("severity"),
	}
	scope.Project, scope.Owner = ctx.FormString("project"), ctx.FormString("owner")
	lo := utils.GetListOptions(ctx)
	hits, total, err := vulnerability_service.SearchAlerts(ctx, scope, f, lo)
	if err != nil {
		searchFailed(ctx, err)
		return
	}
	alerts := make([]*vulnerability_model.Alert, len(hits))
	for i, h := range hits {
		alerts[i] = h.Alert
	}
	converted, err := ToAPIVulnerabilityAlerts(ctx, alerts)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "ToAPIVulnerabilityAlerts", err)
		return
	}
	out := make([]*api.RepoVulnerabilityAlert, len(hits))
	for i, h := range hits {
		out[i] = &api.RepoVulnerabilityAlert{VulnerabilityAlert: *converted[i], Repository: toDependencyRepository(h.Repo)}
	}
	ctx.SetLinkHeader(int(total), lo.PageSize)
	ctx.SetTotalCountHeader(total)
	ctx.JSON(http.StatusOK, out)
}
