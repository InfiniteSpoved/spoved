// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package setting

import (
	"errors"
	"net/http"

	"forgejo.org/models/db"
	secretscan_model "forgejo.org/models/secretscan"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/base"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
	"forgejo.org/services/context"
	dependency_service "forgejo.org/services/dependency"
	secretscan_service "forgejo.org/services/secretscan"
)

const (
	tplSettingsCodeSecurity  base.TplName = "org/settings/code_security"
	tplSecretScanningOffList base.TplName = "org/settings/secret_scanning_off"
)

// CodeSecurity shows the organization's dependency graph switch.
func CodeSecurity(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("org.settings.code_security")
	ctx.Data["PageIsOrgSettingsCodeSecurity"] = true
	sw, err := dependency_service.GetOrgSwitch(ctx, ctx.Org.Organization.ID)
	if err != nil {
		ctx.ServerError("GetOrgSwitch", err)
		return
	}
	ctx.Data["SiteEnabled"] = dependency_service.SiteEnabled(ctx)
	ctx.Data["CanChangeThere"] = ctx.Doer.IsAdmin // the site switch
	ctx.Data["Enabled"] = sw.On
	ctx.Data["Locked"] = sw.Locked
	if err := secretScanningBox(ctx); err != nil {
		ctx.ServerError("secretScanningBox", err)
		return
	}
	ctx.HTML(http.StatusOK, tplSettingsCodeSecurity)
}

// secretScanningBox loads the organization's secret scanning switch and what the confirmations say: how many
// repositories there are, how many admins turned it off, and how many are scanned now.
func secretScanningBox(ctx *context.Context) error {
	sw, err := secretscan_service.GetOrgSwitch(ctx, ctx.Org.Organization.ID)
	if err != nil {
		return err
	}
	total, off, err := secretscan_model.CountOrgRepos(ctx, ctx.Org.Organization.ID)
	if err != nil {
		return err
	}
	scanned := total - off
	switch {
	case !sw.On:
		scanned = 0
	case sw.Locked:
		scanned = total
	}
	ctx.Data["SecretSiteOn"] = secretscan_service.SiteEnabled(ctx)
	ctx.Data["SecretOn"], ctx.Data["SecretLocked"] = sw.On, sw.Locked
	ctx.Data["SecretTotal"], ctx.Data["SecretOff"], ctx.Data["SecretScanned"] = total, off, scanned
	return nil
}

// SecretScanningPost stores the organization's secret scanning switch and lock (every owner may; a site administrator
// acts as one). Turning it off keeps the alerts; the lock applies the switch to every repository.
func SecretScanningPost(ctx *context.Context) {
	if err := secretscan_service.SetOrgSwitch(ctx, ctx.Org.Organization.AsUser(), ctx.FormBool("enabled"), ctx.FormBool("locked")); err != nil {
		ctx.ServerError("SetOrgSwitch", err)
		return
	}
	ctx.Flash.Success(ctx.Tr("org.settings.secret_scanning.saved"))
	ctx.Redirect(ctx.Org.OrgLink + "/settings/code_security")
}

// secretOffRepo is a row of the list of repositories whose admins turned secret scanning off.
type secretOffRepo struct {
	FullName, Link string
	By             string // "" when the audit log has no entry (the setting was never changed through the page, or the entry is gone)
	At             timeutil.TimeStamp
}

// SecretScanningOff lists the repositories whose admins turned secret scanning off, with who and when.
func SecretScanningOff(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("org.settings.secret_scanning.off_list")
	ctx.Data["PageIsOrgSettingsCodeSecurity"] = true
	page := max(ctx.FormInt("page"), 1)
	repos, total, err := secretscan_model.FindOrgOffRepos(ctx, ctx.Org.Organization.ID, db.ListOptions{Page: page, PageSize: setting.UI.User.RepoPagingNum})
	if err != nil {
		ctx.ServerError("FindOrgOffRepos", err)
		return
	}
	rows := make([]secretOffRepo, 0, len(repos))
	for _, repo := range repos { // one page at most: the audit log has no bulk read of this
		row := secretOffRepo{FullName: repo.FullName(), Link: repo.Link() + "/settings/code_security"}
		change, err := secretscan_service.LastSwitchChange(ctx, repo.ID)
		if err != nil {
			ctx.ServerError("LastSwitchChange", err)
			return
		}
		if change != nil && !change.On {
			row.By, row.At = change.ActorName, change.CreatedUnix
			if u, err := user_model.GetUserByID(ctx, change.ActorID); err == nil {
				row.By = u.GetDisplayName()
			}
		}
		rows = append(rows, row)
	}
	ctx.Data["Rows"] = rows
	pager := context.NewPagination(int(total), setting.UI.User.RepoPagingNum, page, 5)
	ctx.Data["Page"] = pager
	ctx.HTML(http.StatusOK, tplSecretScanningOffList)
}

// CodeSecurityPost stores the switch; off deletes the stored package lists of every repository of the organization.
// While the site switch is off, the save is refused with a message on the settings page.
func CodeSecurityPost(ctx *context.Context) {
	err := dependency_service.SetOrgSwitch(ctx, ctx.Org.Organization.AsUser(), ctx.FormBool("enabled"), ctx.FormBool("locked"))
	switch {
	case errors.Is(err, dependency_service.ErrSwitchLocked):
		ctx.Flash.Error(ctx.Tr("repo.settings.dependency_graph.site_off"))
	case err != nil:
		ctx.ServerError("SetOrgSwitch", err)
		return
	default:
		ctx.Flash.Success(ctx.Tr("repo.settings.dependency_graph.saved"))
	}
	ctx.Redirect(ctx.Org.OrgLink + "/settings/code_security")
}
