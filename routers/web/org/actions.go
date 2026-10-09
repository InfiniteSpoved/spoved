// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package org

import (
	"net/http"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/db"
	access_model "forgejo.org/models/perm/access"
	"forgejo.org/models/unit"
	"forgejo.org/modules/base"
	shared_user "forgejo.org/routers/web/shared/user"
	actions_service "forgejo.org/services/actions"
	"forgejo.org/services/context"
	"forgejo.org/services/convert"
)

// tplRuns is the template of the workflow runs of an organization
const tplRuns base.TplName = "org/actions/runs"

// Runs lists the workflow runs of the repositories of the organization that the doer can read the Actions of.
func Runs(ctx *context.Context) {
	if !actions_service.OrgRunsEnabled() {
		ctx.NotFound("Runs", nil)
		return
	}
	filter, err := actions_service.ParseOrgRunFilter(ctx.Base)
	if err != nil {
		ctx.Error(http.StatusBadRequest, err.Error())
		return
	}

	lo := db.ListOptions{Page: max(ctx.FormInt("page"), 1), PageSize: convert.ToCorrectPageSize(ctx.FormInt("limit"))}
	runs, total, err := actions_service.FindOrgRuns(ctx, ctx.Org.Organization, ctx.Doer, ctx.IsUserSiteAdmin(), nil, filter, lo)
	if err == nil {
		runs, err = actions_service.LoadOrgRunRepos(ctx, runs)
	}
	if err == nil {
		err = runs.LoadTriggerUser(ctx)
	}
	var perms map[int64]*access_model.Permission
	if err == nil {
		perms, err = actions_service.OrgRunPermissions(ctx, runs, ctx.Doer)
	}
	if err == nil {
		err = shared_user.LoadHeaderCount(ctx)
	}
	if err != nil {
		ctx.ServerError("Runs", err)
		return
	}

	// the buttons only link to the routes of the repository, which check the permission again
	canWrite := make(map[int64]bool, len(runs))
	for _, run := range runs {
		canWrite[run.ID] = perms[run.RepoID].CanWrite(unit.TypeActions) && !run.Repo.IsArchived
	}

	type statusOption struct{ Value, Label string }
	var statuses []statusOption
	for _, info := range actions_model.GetStatusInfoList(ctx, ctx.Locale) {
		statuses = append(statuses, statusOption{actions_model.Status(info.Status).String(), info.DisplayedStatus})
	}
	ctx.Data["Title"] = ctx.Tr("actions.actions")
	ctx.Data["PageIsOrgActions"] = true
	ctx.Data["Runs"] = runs
	ctx.Data["RunCanWrite"] = canWrite
	ctx.Data["StatusOptions"] = statuses

	pager := context.NewPagination(int(total), lo.PageSize, lo.Page, 5)
	for _, key := range []string{"status", "event", "repo", "workflow_id", "branch", "actor", "from", "to"} {
		ctx.Data["Cur_"+key] = ctx.FormString(key)
		ctx.Data["IsFiltered"] = ctx.Data["IsFiltered"] == true || ctx.FormString(key) != ""
		pager.AddParamString(key, ctx.FormString(key))
	}
	pager.AddParamString("limit", ctx.FormString("limit"))
	ctx.Data["Page"] = pager

	ctx.HTML(http.StatusOK, tplRuns)
}
