// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repo

import (
	"encoding/csv"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	audit_model "forgejo.org/models/audit"
	repo_model "forgejo.org/models/repo"
	vulnerability_model "forgejo.org/models/vulnerability"
	"forgejo.org/modules/json"
	"forgejo.org/modules/log"
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

func toDependencyUsage(r *dependency_service.UsageRow) *api.DependencyUsage {
	return &api.DependencyUsage{
		Repository: toDependencyRepository(r.Repo), Ecosystem: r.Ecosystem, Name: r.Name, Version: r.Version, Requirement: r.Requirement,
		ManifestPath: r.ManifestPath, Direct: r.Direct, Uncertain: r.Uncertain,
	}
}

func toRepoAlerts(ctx *context.APIContext, hits []*vulnerability_service.AlertHit) ([]*api.RepoVulnerabilityAlert, error) {
	alerts := make([]*vulnerability_model.Alert, len(hits))
	for i, h := range hits {
		alerts[i] = h.Alert
	}
	converted, err := ToAPIVulnerabilityAlerts(ctx, alerts)
	if err != nil {
		return nil, err
	}
	out := make([]*api.RepoVulnerabilityAlert, len(hits))
	for i, h := range hits {
		out[i] = &api.RepoVulnerabilityAlert{VulnerabilityAlert: *converted[i], Repository: toDependencyRepository(h.Repo)}
	}
	return out, nil
}

// exportRow is a row of an export: the object of the JSON form and the cells of the CSV form.
type exportRow struct {
	obj   any
	cells []string
}

// recordExport writes the audit entry of an export (fail closed, before the first byte): the kind, the format, the
// filters given and the row count, never a repository name.
func recordExport(ctx *context.APIContext, scope dependency_service.SearchScope, kind, format string, filters map[string]string, count int64) error {
	doer := ctx.Doer()
	data := map[string]any{"kind": kind, "format": format, "count": count}
	for k, v := range filters {
		if v != "" {
			data[k] = v
		}
	}
	e := audit_model.Event{
		Action: "admin.dependency_graph.exported", Result: audit_model.ResultSuccess, TargetType: "dependency_export", Data: data,
	}
	if scope.Org != nil {
		e.Action, e.OwnerID, e.TargetID, e.TargetName = "org.dependency_graph.exported", scope.Org.ID, scope.Org.ID, scope.Org.Name
	} else {
		e.OwnerID, e.TargetID, e.TargetName = doer.ID, doer.ID, doer.Name
	}
	if doer != nil {
		e.SubjectUserID = doer.ID
	}
	return audit_model.Record(ctx.Req.Context(), e)
}

// serveExport streams the rows run reads as a download in format ("csv" or "json"). run calls start with the row
// count once, before any row; start records the audit entry and sends the headers, so an error before it (a wrong
// filter, too many rows, a failed audit insert) is an ordinary error answer with no body.
func serveExport(ctx *context.APIContext, scope dependency_service.SearchScope, kind, format string, filters map[string]string, header []string,
	run func(start func(total int64) error, emit func([]exportRow) error) error,
) {
	started, sep := false, "["
	cw, enc := csv.NewWriter(ctx.Resp), json.NewEncoder(ctx.Resp)
	err := run(func(total int64) error {
		if err := recordExport(ctx, scope, kind, format, filters, total); err != nil {
			return err
		}
		started = true
		name, owner, contentType := "dependencies", "instance", "text/csv"
		if kind == "alerts" {
			name = "vulnerability-alerts"
		}
		if scope.Org != nil {
			owner = scope.Org.Name
		}
		if format == "json" {
			contentType = "application/json"
		}
		ctx.SetServeHeaders(&context.ServeHeaderOptions{
			ContentType: contentType, ContentTypeCharset: "utf-8",
			Filename: name + "-" + owner + "-" + time.Now().UTC().Format("2006-01-02") + "." + format,
		})
		ctx.Resp.Header().Set("Cache-Control", "no-store")
		if format == "csv" {
			return cw.Write(header)
		}
		return nil
	}, func(rows []exportRow) error {
		for _, r := range rows {
			if format == "json" {
				if _, err := ctx.Resp.Write([]byte(sep)); err != nil {
					return err
				}
				sep = ","
				if err := enc.Encode(r.obj); err != nil {
					return err
				}
				continue
			}
			cells := make([]string, len(r.cells))
			for i, c := range r.cells {
				cells[i] = util.CSVSafe(c)
			}
			if err := cw.Write(cells); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil && format == "json" {
		end := "]\n"
		if sep == "[" {
			end = "[]\n"
		}
		_, err = ctx.Resp.Write([]byte(end))
	}
	if err == nil {
		cw.Flush()
		err = cw.Error()
	}
	switch {
	case err == nil:
	case !started:
		searchFailed(ctx, err)
	default:
		log.Error("serveExport: %v", err)
		panic(http.ErrAbortHandler) // abort so the client sees a failed download, not a short file
	}
}

// readFormat reads the format parameter: "" is the paged answer, otherwise csv or json.
func readFormat(ctx *context.APIContext) (string, bool) {
	format := ctx.FormString("format")
	if format != "" && format != "csv" && format != "json" {
		ctx.Error(http.StatusUnprocessableEntity, "", "format must be csv or json")
		return "", false
	}
	return format, true
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
	format, ok := readFormat(ctx)
	if !ok {
		return
	}
	if format != "" {
		serveExport(ctx, scope, "dependencies", format, map[string]string{
			"ecosystem": f.Ecosystem, "name": f.Name, "version": f.Version, "range": f.Range, "direct": ctx.FormString("direct"), "owner": scope.Owner, "project": scope.Project,
		}, []string{"repository", "ecosystem", "name", "version", "requirement", "manifest", "direct", "uncertain"},
			func(start func(int64) error, emit func([]exportRow) error) error {
				return dependency_service.ExportUsage(ctx, scope, f, start, func(rows []*dependency_service.UsageRow) error {
					out := make([]exportRow, len(rows))
					for i, r := range rows {
						out[i] = exportRow{obj: toDependencyUsage(r), cells: []string{
							r.Repo.FullName(), r.Ecosystem, r.Name, r.Version, r.Requirement, r.ManifestPath, strconv.FormatBool(r.Direct), strconv.FormatBool(r.Uncertain),
						}}
					}
					return emit(out)
				})
			})
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
		out = append(out, toDependencyUsage(r))
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
	format, ok := readFormat(ctx)
	if !ok {
		return
	}
	if format != "" {
		serveExport(ctx, scope, "alerts", format, map[string]string{
			"advisory": f.Advisory, "ecosystem": f.Ecosystem, "name": f.Name, "version": f.Version, "state": f.State, "severity": f.Severity, "owner": scope.Owner, "project": scope.Project,
		}, []string{"repository", "advisory", "aliases", "severity", "ecosystem", "package", "version", "fixed in", "manifest", "state"},
			func(start func(int64) error, emit func([]exportRow) error) error {
				return vulnerability_service.ExportAlerts(ctx, scope, f, start, func(hits []*vulnerability_service.AlertHit) error {
					alerts, err := toRepoAlerts(ctx, hits)
					if err != nil {
						return err
					}
					out := make([]exportRow, len(alerts))
					for i, a := range alerts {
						out[i] = exportRow{obj: a, cells: []string{
							a.Repository.FullName, a.Advisory.ID, strings.Join(a.Advisory.Aliases, " "), a.Severity, a.Ecosystem, a.Package, a.Version,
							strings.Join(a.FixedVersions, " "), a.ManifestPath, a.State,
						}}
					}
					return emit(out)
				})
			})
		return
	}
	lo := utils.GetListOptions(ctx)
	hits, total, err := vulnerability_service.SearchAlerts(ctx, scope, f, lo)
	if err != nil {
		searchFailed(ctx, err)
		return
	}
	out, err := toRepoAlerts(ctx, hits)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "ToAPIVulnerabilityAlerts", err)
		return
	}
	recordInstanceSearch(ctx, scope, "alerts", map[string]string{"advisory": f.Advisory, "ecosystem": f.Ecosystem, "name": f.Name, "owner": scope.Owner, "state": f.State}, total)
	ctx.SetLinkHeader(int(total), lo.PageSize)
	ctx.SetTotalCountHeader(total)
	ctx.JSON(http.StatusOK, out)
}
