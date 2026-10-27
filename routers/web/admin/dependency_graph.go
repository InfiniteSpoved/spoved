// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package admin

import (
	"errors"
	"html/template"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	cfg := setting.DependencyGraphSettings(ctx)
	ctx.Data["MirrorURL"] = cfg.OSVMirrorURL
	ctx.Data["MirrorInterval"] = shortDuration(cfg.UpdateInterval)
	ctx.Data["MirrorRunning"] = vulnerability_service.Running()
	ctx.Data["MirrorSiteOn"] = dependency_service.SiteEnabled(ctx)
	// the auditor reads the page; only a site admin changes anything
	ctx.Data["CanChange"] = ctx.Doer.IsAdmin
	ctx.HTML(http.StatusOK, tplDependencyGraph)
}

// shortDuration writes a duration as 24h or 90m, not 24h0m0s.
func shortDuration(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	case d >= time.Minute && d%time.Minute == 0:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	}
	return d.String()
}

// DependencyGraphUpdateAdvisories starts an update of the advisory mirror ("Update now").
func DependencyGraphUpdateAdvisories(ctx *context.Context) {
	switch err := vulnerability_service.UpdateNow(ctx, ctx.Doer); {
	case err == nil:
		ctx.Flash.Success(ctx.Tr("admin.dependency_graph.advisories.started"))
	case errors.Is(err, vulnerability_service.ErrNoMirror):
		ctx.Flash.Error(ctx.Tr("admin.dependency_graph.advisories.no_mirror"))
	case errors.Is(err, vulnerability_service.ErrSiteOff):
		ctx.Flash.Error(ctx.Tr("admin.dependency_graph.advisories.site_off"))
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

// dependencyGraphFormUnits is the unit of a limit in the form, counted in its stored unit (bytes or seconds); a
// limit that isn't listed is a plain number.
var dependencyGraphFormUnits = map[string]int64{
	"scan_timeout": 1, "update_interval": 3600, "osv_ecosystem_timeout": 60,
	"max_manifest_size": 1 << 20, "osv_max_download": 1 << 20, "osv_max_entry_size": 1 << 20, "osv_max_uncompressed": 1 << 20,
}

// DependencyGraphSettingsPost saves the dependency graph settings. An empty limit or host list is the app.ini value;
// an empty token keeps the one in force.
func DependencyGraphSettingsPost(ctx *context.Context) {
	in := vulnerability_service.SettingsInput{
		Limits:      map[string]int64{},
		Source:      ctx.FormString("source"),
		MirrorURL:   ctx.FormString("mirror_url"),
		SignIn:      ctx.FormString("sign_in"),
		User:        ctx.FormString("user"),
		Token:       strings.TrimSpace(ctx.FormString("token")),
		RemoveToken: ctx.FormBool("remove_token"),
		Hosts:       ctx.FormString("allowed_hosts"),
	}
	var err error
	for _, l := range setting.DependencyGraphLimits {
		text := strings.TrimSpace(ctx.FormString(l.Name))
		if text == "" {
			continue
		}
		unit := max(dependencyGraphFormUnits[l.Name], 1)
		v, perr := strconv.ParseInt(text, 10, 64)
		if perr != nil || v > math.MaxInt64/unit || v < math.MinInt64/unit {
			err = &vulnerability_service.SettingsError{Field: l.Name, Reason: vulnerability_service.SettingsInvalid}
			break
		}
		in.Limits[l.Name] = v * unit
	}
	changed := false
	if err == nil {
		changed, err = vulnerability_service.SaveSettings(ctx, ctx.Doer, in)
	}
	var serr *vulnerability_service.SettingsError
	switch {
	case errors.As(err, &serr):
		ctx.Flash.Error(dependencyGraphSettingsError(ctx, serr))
	case err != nil:
		ctx.ServerError("SaveSettings", err)
		return
	case changed:
		ctx.Flash.Success(ctx.Tr("admin.dependency_graph.settings.saved"))
	default:
		ctx.Flash.Info(ctx.Tr("admin.dependency_graph.settings.unchanged"))
	}
	ctx.Redirect(setting.AppSubURL + "/admin/config/dependency-graph")
}

// dependencyGraphSettingsError is the message of a refused save, from literal keys only.
func dependencyGraphSettingsError(ctx *context.Context, e *vulnerability_service.SettingsError) template.HTML {
	field := dependencyGraphFieldLabel(ctx, e.Field)
	switch e.Reason {
	case vulnerability_service.SettingsOutOfRange:
		l, _ := setting.DependencyGraphLimitByName(e.Field)
		unit := max(dependencyGraphFormUnits[e.Field], 1)
		return ctx.Tr("admin.dependency_graph.settings.error.out_of_range", field, (l.Min+unit-1)/unit, l.Max/unit)
	case vulnerability_service.SettingsOwnHost:
		return ctx.Tr("admin.dependency_graph.settings.error.own_host")
	case vulnerability_service.SettingsTokenRequired:
		return ctx.Tr("admin.dependency_graph.settings.error.token_required")
	case vulnerability_service.SettingsTokenHostChanged:
		return ctx.Tr("admin.dependency_graph.settings.error.token_host_changed")
	case vulnerability_service.SettingsUserRequired:
		return ctx.Tr("admin.dependency_graph.settings.error.user_required")
	}
	return ctx.Tr("admin.dependency_graph.settings.error.invalid", field)
}

// dependencyGraphFieldLabel is the label of a field of the settings form.
func dependencyGraphFieldLabel(ctx *context.Context, field string) string {
	switch field {
	case "max_concurrent_scans":
		return ctx.Locale.TrString("admin.dependency_graph.settings.max_concurrent_scans")
	case "scan_timeout":
		return ctx.Locale.TrString("admin.dependency_graph.settings.scan_timeout")
	case "max_manifest_size":
		return ctx.Locale.TrString("admin.dependency_graph.settings.max_manifest_size")
	case "max_manifests":
		return ctx.Locale.TrString("admin.dependency_graph.settings.max_manifests")
	case "max_dependencies":
		return ctx.Locale.TrString("admin.dependency_graph.settings.max_dependencies")
	case "max_tree_entries":
		return ctx.Locale.TrString("admin.dependency_graph.settings.max_tree_entries")
	case "update_interval":
		return ctx.Locale.TrString("admin.dependency_graph.settings.update_interval")
	case "osv_max_download":
		return ctx.Locale.TrString("admin.dependency_graph.settings.osv_max_download")
	case "osv_max_entries":
		return ctx.Locale.TrString("admin.dependency_graph.settings.osv_max_entries")
	case "osv_max_entry_size":
		return ctx.Locale.TrString("admin.dependency_graph.settings.osv_max_entry_size")
	case "osv_max_uncompressed":
		return ctx.Locale.TrString("admin.dependency_graph.settings.osv_max_uncompressed")
	case "osv_ecosystem_timeout":
		return ctx.Locale.TrString("admin.dependency_graph.settings.osv_ecosystem_timeout")
	case "source":
		return ctx.Locale.TrString("admin.dependency_graph.settings.source")
	case "mirror_url":
		return ctx.Locale.TrString("admin.dependency_graph.settings.mirror_url")
	case "sign_in":
		return ctx.Locale.TrString("admin.dependency_graph.settings.sign_in")
	case "user":
		return ctx.Locale.TrString("admin.dependency_graph.settings.user")
	case "token":
		return ctx.Locale.TrString("admin.dependency_graph.settings.token")
	case "allowed_hosts":
		return ctx.Locale.TrString("admin.dependency_graph.settings.allowed_hosts")
	}
	return field
}

// DependencyGraphTestConnection tests the saved mirror settings; the answer is a class, never error text.
func DependencyGraphTestConnection(ctx *context.Context) {
	class, err := vulnerability_service.TestConnection(ctx)
	switch {
	case errors.Is(err, vulnerability_service.ErrNoMirror):
		ctx.Flash.Error(ctx.Tr("admin.dependency_graph.test.no_mirror"))
	case errors.Is(err, vulnerability_service.ErrSiteOff):
		ctx.Flash.Error(ctx.Tr("admin.dependency_graph.advisories.site_off"))
	case errors.Is(err, vulnerability_service.ErrTestTooSoon):
		ctx.Flash.Error(ctx.Tr("admin.dependency_graph.test.too_soon"))
	case err != nil:
		ctx.ServerError("TestConnection", err)
		return
	case class == vulnerability_service.ConnectionOK:
		ctx.Flash.Success(ctx.Tr("admin.dependency_graph.test.ok"))
	default:
		ctx.Flash.Error(dependencyGraphTestError(ctx, class))
	}
	ctx.Redirect(setting.AppSubURL + "/admin/config/dependency-graph")
}

func dependencyGraphTestError(ctx *context.Context, class string) template.HTML {
	switch class {
	case vulnerability_model.ErrClassAuthFailed:
		return ctx.Tr("admin.dependency_graph.test.auth_failed")
	case vulnerability_model.ErrClassBlockedHost:
		return ctx.Tr("admin.dependency_graph.test.blocked_host")
	case vulnerability_model.ErrClassTimeout:
		return ctx.Tr("admin.dependency_graph.test.timeout")
	case vulnerability_model.ErrClassStatus4xx:
		return ctx.Tr("admin.dependency_graph.test.status_4xx")
	case vulnerability_model.ErrClassStatus5xx:
		return ctx.Tr("admin.dependency_graph.test.status_5xx")
	case vulnerability_model.ErrClassRedirectRefused:
		return ctx.Tr("admin.dependency_graph.test.redirect_refused")
	case vulnerability_model.ErrClassConfigInvalid:
		return ctx.Tr("admin.dependency_graph.test.config_invalid")
	}
	return ctx.Tr("admin.dependency_graph.test.connection_failed")
}
