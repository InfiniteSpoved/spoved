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
	"forgejo.org/modules/timeutil"
	shared_user "forgejo.org/routers/web/shared/user"
	actions_service "forgejo.org/services/actions"
	"forgejo.org/services/context"
	"forgejo.org/services/convert"
)

// tplRuns is the template of the workflow runs of an organization
const tplRuns base.TplName = "org/actions/runs"

// tplOverview is the template of the overview of the Actions of an organization
const tplOverview base.TplName = "org/actions/overview"

// overviewLimit is how many failing runs and queued jobs the overview lists
const overviewLimit = 50

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
	ctx.Data["PageIsOrgActionsRuns"] = true
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

// Overview shows the numbers, the failing default branches and the queue of the runs that the doer can see.
func Overview(ctx *context.Context) {
	if !actions_service.OrgRunsEnabled() {
		ctx.NotFound("Overview", nil)
		return
	}
	org, doer, admin, now := ctx.Org.Organization, ctx.Doer, ctx.IsUserSiteAdmin(), timeutil.TimeStampNow()
	overview, err := actions_service.GetOrgOverview(ctx, org, doer, admin, nil, now)
	if err != nil {
		ctx.ServerError("GetOrgOverview", err)
		return
	}
	failing, failingTotal, err := actions_service.FindOrgFailingNow(ctx, org, doer, admin, nil, overviewLimit)
	var queue []*actions_service.QueuedJob
	var queueTotal int64
	if err == nil {
		queue, queueTotal, err = actions_service.FindOrgQueue(ctx, org, doer, admin, nil, overviewLimit, now)
	}
	if err == nil {
		err = shared_user.LoadHeaderCount(ctx)
	}
	if err != nil {
		ctx.ServerError("Overview", err)
		return
	}
	ctx.Data["Title"] = ctx.Tr("actions.actions")
	ctx.Data["PageIsOrgActions"] = true
	ctx.Data["PageIsOrgActionsOverview"] = true
	ctx.Data["Overview"] = overview
	ctx.Data["Failing"] = failing
	ctx.Data["FailingTotal"] = failingTotal
	ctx.Data["Queue"] = queue
	ctx.Data["QueueTotal"] = queueTotal
	ctx.HTML(http.StatusOK, tplOverview)
}
