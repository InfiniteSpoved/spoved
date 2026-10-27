// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repo

import (
	"errors"
	"net/http"
	"strconv"

	audit_model "forgejo.org/models/audit"
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

// MustEnableDependencySearch answers 404 to everyone while the dependency graph is off for the site, before any gate
// can tell the caller anything else.
func MustEnableDependencySearch(ctx *context.APIContext) {
	if !dependency_service.SiteEnabled(ctx) {
		ctx.NotFound()
	}
}

// readScopeFilters reads the project filter (organization routes) or the owner filter (instance routes); the one the
// route doesn't take is a 422.
func readScopeFilters(ctx *context.APIContext, scope *dependency_service.SearchScope) bool {
	scope.Project, scope.Owner = ctx.FormString("project"), ctx.FormString("owner")
	switch {
	case scope.Org != nil && scope.Owner != "":
		ctx.Error(http.StatusUnprocessableEntity, "", "owner is a filter of the instance routes")
	case scope.Org == nil && scope.Project != "":
		ctx.Error(http.StatusUnprocessableEntity, "", "project is a filter of the organization routes")
	default:
		return true
	}
	return false
}

// recordInstanceSearch writes the audit entry of a search across every owner (one per request, aggregated per
// minute and kind, fail open): the route kind, the filters given and the total, never a repository name. The
// organization routes record nothing: an owner reads their own organization.
func recordInstanceSearch(ctx *context.APIContext, scope dependency_service.SearchScope, kind string, filters map[string]string, total int64) {
	doer := ctx.Doer()
	if scope.Org != nil || doer == nil {
		return
	}
	data := map[string]any{"kind": kind, "total": total}
	for k, v := range filters {
		if v != "" {
			data[k] = v
		}
	}
	audit_model.RecordAggregated(ctx.Req.Context(), audit_model.Event{
		Action: "activity.dependency_graph.searched", Result: audit_model.ResultSuccess, OwnerID: doer.ID, SubjectUserID: doer.ID,
		TargetType: "dependency_search", TargetID: doer.ID, TargetName: doer.Name, Data: data,
	}, strconv.FormatInt(doer.ID, 10)+"|"+kind)
}

// SearchDependencyUsage answers the dependency search of the org and instance routes. The route's gate has decided
// who may ask; scope says which repositories the answer covers.
func SearchDependencyUsage(ctx *context.APIContext, scope dependency_service.SearchScope) {
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
	if !readScopeFilters(ctx, &scope) {
		return
	}
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
	recordInstanceSearch(ctx, scope, "dependencies", map[string]string{"ecosystem": f.Ecosystem, "name": f.Name, "owner": scope.Owner}, total)
	ctx.SetLinkHeader(int(total), lo.PageSize)
	ctx.SetTotalCountHeader(total)
	ctx.JSON(http.StatusOK, out)
}

// SearchVulnerabilityAlerts answers the alert search of the org and instance routes, as SearchDependencyUsage does.
func SearchVulnerabilityAlerts(ctx *context.APIContext, scope dependency_service.SearchScope) {
	f := vulnerability_service.AlertFilter{
		Advisory: ctx.FormString("advisory"), Ecosystem: ctx.FormString("ecosystem"), Name: ctx.FormString("name"), Version: ctx.FormString("version"),
		State: ctx.FormString("state"), Severity: ctx.FormString("severity"),
	}
	if !readScopeFilters(ctx, &scope) {
		return
	}
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
	recordInstanceSearch(ctx, scope, "alerts", map[string]string{"advisory": f.Advisory, "ecosystem": f.Ecosystem, "name": f.Name, "owner": scope.Owner, "state": f.State}, total)
	ctx.SetLinkHeader(int(total), lo.PageSize)
	ctx.SetTotalCountHeader(total)
	ctx.JSON(http.StatusOK, out)
}
