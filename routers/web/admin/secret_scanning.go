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

	user_model "forgejo.org/models/user"
	"forgejo.org/modules/base"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
	"forgejo.org/services/context"
	secretscan_service "forgejo.org/services/secretscan"
)

const tplSecretScanning base.TplName = "admin/secret_scanning"

// secretScanningLimitRow is one limit of the form: its value in force (or as posted after a refused save), the app.ini
// value, and a message when it was refused.
type secretScanningLimitRow struct {
	Name, Label, Unit string
	Value, Default    string
	Error             template.HTML
}

// secretScanningFormUnits is the unit of a size limit in the form (MiB); a time is in seconds and the rest are counts.
var secretScanningFormUnits = map[string]int64{"max_blob_size": 1 << 20, "max_scan_bytes": 1 << 20}

// secretScanningLimitText is a limit in its form unit, exactly (0.5 for half a MiB).
func secretScanningLimitText(name string, v int64) string {
	if unit := secretScanningFormUnits[name]; unit > 1 {
		return strconv.FormatFloat(float64(v)/float64(unit), 'f', -1, 64)
	}
	return strconv.FormatInt(v, 10)
}

// parseSecretScanningLimit reads a limit in its form unit: a size may have decimals and is rounded to the byte, the
// others are whole numbers. The bounds are the save's check.
func parseSecretScanningLimit(name, text string) (int64, bool) {
	unit, ok := secretScanningFormUnits[name]
	if !ok {
		v, err := strconv.ParseInt(text, 10, 64)
		return v, err == nil
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	v := math.Round(f * float64(unit))
	if v >= math.MaxInt64 || v <= math.MinInt64 {
		return 0, false
	}
	return int64(v), true
}

// secretScanningLimitLabel and secretScanningLimitUnit are literal keys, so that lint-locale-usage finds them.
func secretScanningLimitLabel(ctx *context.Context, name string) string {
	switch name {
	case "max_concurrent_scans":
		return ctx.Locale.TrString("admin.secret_scanning.limit.max_concurrent_scans")
	case "scan_timeout":
		return ctx.Locale.TrString("admin.secret_scanning.limit.scan_timeout")
	case "max_blob_size":
		return ctx.Locale.TrString("admin.secret_scanning.limit.max_blob_size")
	case "max_scan_bytes":
		return ctx.Locale.TrString("admin.secret_scanning.limit.max_scan_bytes")
	case "max_scan_blobs":
		return ctx.Locale.TrString("admin.secret_scanning.limit.max_scan_blobs")
	case "max_alerts_per_repo":
		return ctx.Locale.TrString("admin.secret_scanning.limit.max_alerts_per_repo")
	case "max_locations_per_alert":
		return ctx.Locale.TrString("admin.secret_scanning.limit.max_locations_per_alert")
	}
	return name
}

func secretScanningLimitUnit(ctx *context.Context, name string) string {
	switch name {
	case "scan_timeout":
		return ctx.Locale.TrString("admin.secret_scanning.unit.seconds")
	case "max_blob_size", "max_scan_bytes":
		return ctx.Locale.TrString("admin.secret_scanning.unit.mib")
	case "max_scan_blobs":
		return ctx.Locale.TrString("admin.secret_scanning.unit.files")
	case "max_alerts_per_repo":
		return ctx.Locale.TrString("admin.secret_scanning.unit.alerts")
	case "max_locations_per_alert":
		return ctx.Locale.TrString("admin.secret_scanning.unit.places")
	}
	return ""
}

// SecretScanning shows the secret scanning settings: the switches and the limits.
func SecretScanning(ctx *context.Context) {
	renderSecretScanning(ctx, nil, nil, http.StatusOK)
}

// renderSecretScanning shows the page. After a refused save, posted holds the limits as typed and refused names the
// one that was refused, with its message.
func renderSecretScanning(ctx *context.Context, posted map[string]string, refused *secretScanningLimitRow, status int) {
	ctx.Data["Title"] = ctx.Tr("admin.secret_scanning")
	ctx.Data["PageIsAdminConfig"] = true
	ctx.Data["PageIsAdminConfigSecretScanning"] = true
	state := setting.Config().SecretScanning.Value(ctx)
	stored := setting.Config().SecretScanningConfig.Value(ctx)
	cfg := stored.Merge()
	ctx.Data["Enabled"] = state.Enabled()
	ctx.Data["Generic"] = cfg.GenericRules
	ctx.Data["FileEnabled"], ctx.Data["FileGeneric"] = setting.SecretScanning.Enabled, setting.SecretScanning.GenericRules
	by, at := state.UpdatedBy, state.UpdatedUnix // the later of the two saves
	if stored.UpdatedUnix > at {
		by, at = stored.UpdatedBy, stored.UpdatedUnix
	}
	if at > 0 {
		ctx.Data["UpdatedUnix"], ctx.Data["UpdatedBy"] = timeutil.TimeStamp(at), "-"
		if u, err := user_model.GetPossibleUserByID(ctx, by); err == nil {
			ctx.Data["UpdatedBy"] = u.GetDisplayName()
		}
	}
	rows := make([]secretScanningLimitRow, 0, len(setting.SecretScanningLimits))
	for _, l := range setting.SecretScanningLimits {
		row := secretScanningLimitRow{
			Name: l.Name, Label: secretScanningLimitLabel(ctx, l.Name), Unit: secretScanningLimitUnit(ctx, l.Name),
			Value:   secretScanningLimitText(l.Name, l.Get(&cfg)),
			Default: secretScanningLimitText(l.Name, l.Get(&setting.SecretScanning)),
		}
		if text, ok := posted[l.Name]; ok {
			row.Value = text
		}
		if refused != nil && refused.Name == l.Name {
			row.Error = refused.Error
		}
		rows = append(rows, row)
	}
	ctx.Data["Limits"] = rows
	if refused != nil {
		ctx.Data["LimitsError"] = refused.Error
		ctx.Data["LimitsErrorField"] = refused.Name
	}
	// the auditor reads the page; only a site administrator changes anything
	ctx.Data["CanChange"] = ctx.Doer.IsAdmin
	ctx.HTML(status, tplSecretScanning)
}

// SecretScanningSwitchesPost saves the site switch and the generic rule. It never touches the limits.
func SecretScanningSwitchesPost(ctx *context.Context) {
	changed, err := secretscan_service.SaveSwitches(ctx, ctx.Doer, ctx.FormBool("enabled"), ctx.FormBool("generic"))
	if err != nil {
		ctx.ServerError("SaveSwitches", err)
		return
	}
	flashSecretScanningSaved(ctx, changed)
}

// SecretScanningLimitsPost saves the limits. An empty field keeps what it is. A value outside its bounds, or one that
// is no number, refuses the whole save and shows the form again with the posted values.
func SecretScanningLimitsPost(ctx *context.Context) {
	limits, posted := map[string]int64{}, map[string]string{}
	for _, l := range setting.SecretScanningLimits {
		text := strings.TrimSpace(ctx.FormString(l.Name))
		if text == "" {
			continue
		}
		posted[l.Name] = text
		v, ok := parseSecretScanningLimit(l.Name, text)
		if !ok {
			msg := ctx.Tr("admin.secret_scanning.error.invalid", secretScanningLimitLabel(ctx, l.Name))
			renderSecretScanning(ctx, posted, &secretScanningLimitRow{Name: l.Name, Error: msg}, http.StatusUnprocessableEntity)
			return
		}
		limits[l.Name] = v
	}
	changed, err := secretscan_service.SaveLimits(ctx, ctx.Doer, limits)
	if lerr, ok := errors.AsType[*secretscan_service.LimitError](err); ok {
		l := lerr.Limit
		maxText := secretScanningLimitText(l.Name, l.Max)
		if unit := secretScanningLimitUnit(ctx, l.Name); unit != "" {
			maxText += " " + unit
		}
		var msg template.HTML
		if secretScanningFormUnits[l.Name] > 1 && l.Min < secretScanningFormUnits[l.Name] {
			msg = ctx.Tr("admin.secret_scanning.error.nothing_saved_max", secretScanningLimitLabel(ctx, l.Name), maxText)
		} else {
			msg = ctx.Tr("admin.secret_scanning.error.nothing_saved", secretScanningLimitLabel(ctx, l.Name), secretScanningLimitText(l.Name, l.Min), maxText)
		}
		renderSecretScanning(ctx, posted, &secretScanningLimitRow{Name: l.Name, Error: msg}, http.StatusUnprocessableEntity)
		return
	}
	if err != nil {
		ctx.ServerError("SaveLimits", err)
		return
	}
	flashSecretScanningSaved(ctx, changed)
}

func flashSecretScanningSaved(ctx *context.Context, changed bool) {
	if changed {
		ctx.Flash.Success(ctx.Tr("admin.secret_scanning.saved"))
	} else {
		ctx.Flash.Info(ctx.Tr("admin.secret_scanning.unchanged"))
	}
	ctx.Redirect(setting.AppSubURL + "/admin/config/secret-scanning")
}
