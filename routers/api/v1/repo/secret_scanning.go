// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repo

import (
	"errors"
	"net/http"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	secretscan_model "forgejo.org/models/secretscan"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/util"
	"forgejo.org/modules/web"
	"forgejo.org/routers/api/v1/utils"
	"forgejo.org/services/auth"
	"forgejo.org/services/context"
	"forgejo.org/services/convert"
	secretscan_service "forgejo.org/services/secretscan"
	vulnerability_service "forgejo.org/services/vulnerability"
	wiki_service "forgejo.org/services/wiki"
)

// maxAlertLocations is the number of places one alert read lists.
const maxAlertLocations = 100

// MustSeeSecretAlerts is the one gate of the secret alert routes, like MustSeeVulnerabilityAlerts: everyone who may
// not see alerts (a reader, an anonymous user, a token that only reads, a repository secret scanning is off for)
// gets the plain 404 of a missing repository, decided before anything else is looked at. Writers and admins pass,
// and so does an auditor reading with their own credential. An Actions job token never sees alerts (GET is a 404, a
// write a 403).
func MustSeeSecretAlerts(ctx *context.APIContext) {
	if ctx.Authentication().ActionsTaskID().Has() {
		if ctx.IsReadOnlyMethod() {
			ctx.NotFound()
		} else {
			ctx.Error(http.StatusForbidden, "", "an Actions job token can't change secret alerts")
		}
		return
	}
	auditor := vulnerability_service.AuditorView(ctx, ctx.Doer(), ctx.Repo().Owner, ctx.IsReadOnlyMethod(), auth.IsHeldByUser(ctx.Authentication()))
	if !vulnerability_service.CanSeeAlerts(ctx.Permission(), auditor) || !secretscan_service.Enabled(ctx, ctx.Repo().Repository) {
		ctx.NotFound()
	}
}

// ListSecretScanningAlerts lists the secret alerts of a repository
func ListSecretScanningAlerts(ctx *context.APIContext) {
	// swagger:operation GET /repos/{owner}/{repo}/secret-scanning/alerts repository repoListSecretScanningAlerts
	// ---
	// summary: List the secret alerts of a repository
	// description: Secrets found in the code and wiki of the repository, newest first. The alerts never hold the
	//   secret. Only people who can write to the code, and auditors, see them; everyone else gets 404, as does a
	//   repository secret scanning is turned off for.
	// produces:
	// - application/json
	// parameters:
	// - name: owner
	//   in: path
	//   description: owner of the repo
	//   type: string
	//   required: true
	// - name: repo
	//   in: path
	//   description: name of the repo
	//   type: string
	//   required: true
	// - name: state
	//   in: query
	//   description: only alerts in this state; closed is every closed state
	//   type: string
	//   enum: [open, closed, revoked, false_positive, used_in_tests]
	// - name: secret_type
	//   in: query
	//   description: only this secret type (aws, github, ...)
	//   type: string
	// - name: rule
	//   in: query
	//   description: only this rule
	//   type: string
	// - name: page
	//   in: query
	//   description: page number of results to return (1-based)
	//   type: integer
	// - name: limit
	//   in: query
	//   description: page size of results
	//   type: integer
	// responses:
	//   "200":
	//     "$ref": "#/responses/SecretScanningAlertList"
	//   "404":
	//     "$ref": "#/responses/notFound"
	//   "422":
	//     "$ref": "#/responses/validationError"

	repo := ctx.Repo().Repository
	opts := &secretscan_model.AlertSearchOptions{
		ListOptions: utils.GetListOptions(ctx), RepoID: repo.ID,
		State: ctx.FormString("state"), SecretType: ctx.FormString("secret_type"), RuleID: ctx.FormString("rule"),
	}
	if !secretscan_model.ValidSearchState(opts.State) {
		ctx.Error(http.StatusUnprocessableEntity, "", "unknown state")
		return
	}
	if len(opts.SecretType) > secretscan_model.MaxFilterLen || len(opts.RuleID) > secretscan_model.MaxFilterLen {
		ctx.Error(http.StatusUnprocessableEntity, "", "the filter is too long")
		return
	}
	opts.SetDefaultValues()
	alerts, total, err := db.FindAndCount[secretscan_model.Alert](ctx, opts)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "FindAndCount", err)
		return
	}
	list, err := convert.ToSecretScanningAlerts(ctx, ctx.Doer(), repo, alerts, nil, nil)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "ToSecretScanningAlerts", err)
		return
	}
	ctx.SetLinkHeader(int(total), opts.PageSize)
	ctx.SetTotalCountHeader(total)
	ctx.JSON(http.StatusOK, list)
}

// GetSecretScanningAlert gets a secret alert with its places
func GetSecretScanningAlert(ctx *context.APIContext) {
	// swagger:operation GET /repos/{owner}/{repo}/secret-scanning/alerts/{index} repository repoGetSecretScanningAlert
	// ---
	// summary: Get a secret alert
	// description: The alert with its places, at most 100 (locations_count is the number of all of them).
	// produces:
	// - application/json
	// parameters:
	// - name: owner
	//   in: path
	//   description: owner of the repo
	//   type: string
	//   required: true
	// - name: repo
	//   in: path
	//   description: name of the repo
	//   type: string
	//   required: true
	// - name: index
	//   in: path
	//   description: number of the alert
	//   type: integer
	//   format: int64
	//   required: true
	// responses:
	//   "200":
	//     "$ref": "#/responses/SecretScanningAlert"
	//   "404":
	//     "$ref": "#/responses/notFound"

	alert := loadSecretScanningAlert(ctx)
	if alert == nil {
		return
	}
	respondSecretScanningAlert(ctx, alert)
}

// EditSecretScanningAlert closes or reopens a secret alert
func EditSecretScanningAlert(ctx *context.APIContext) {
	// swagger:operation PATCH /repos/{owner}/{repo}/secret-scanning/alerts/{index} repository repoEditSecretScanningAlert
	// ---
	// summary: Close or reopen a secret alert
	// description: Closing needs one of the states revoked, false_positive and used_in_tests, which is also its
	//   resolution, and takes an optional comment of at most 1000 bytes that must not look like a secret. State open
	//   reopens a closed alert. An alert that is not open can't be closed again (409).
	// consumes:
	// - application/json
	// produces:
	// - application/json
	// parameters:
	// - name: owner
	//   in: path
	//   description: owner of the repo
	//   type: string
	//   required: true
	// - name: repo
	//   in: path
	//   description: name of the repo
	//   type: string
	//   required: true
	// - name: index
	//   in: path
	//   description: number of the alert
	//   type: integer
	//   format: int64
	//   required: true
	// - name: body
	//   in: body
	//   schema:
	//     "$ref": "#/definitions/EditSecretScanningAlertOption"
	// responses:
	//   "200":
	//     "$ref": "#/responses/SecretScanningAlert"
	//   "404":
	//     "$ref": "#/responses/notFound"
	//   "409":
	//     "$ref": "#/responses/conflict"
	//   "422":
	//     "$ref": "#/responses/validationError"
	//   "423":
	//     "$ref": "#/responses/repoArchivedError"

	form := web.GetForm(ctx).(*api.EditSecretScanningAlertOption)
	alert := loadSecretScanningAlert(ctx)
	if alert == nil {
		return
	}
	var err error
	if form.State == secretscan_model.StateOpen {
		if form.Comment != "" {
			ctx.Error(http.StatusUnprocessableEntity, "", "a comment is only for closing")
			return
		}
		alert, err = secretscan_service.Reopen(ctx, ctx.Repo().Repository, alert.Index)
	} else {
		alert, err = secretscan_service.Close(ctx, ctx.Repo().Repository, ctx.Doer(), alert.Index, form.State, form.Comment)
	}
	switch {
	case errors.Is(err, util.ErrInvalidArgument):
		ctx.Error(http.StatusUnprocessableEntity, "", err.Error())
	case errors.Is(err, util.ErrNotExist):
		ctx.NotFound()
	case errors.Is(err, secretscan_service.ErrConflict):
		ctx.Error(http.StatusConflict, "", "the alert changed, reload it")
	case errors.Is(err, secretscan_service.ErrArchived):
		ctx.Error(http.StatusLocked, "", "the repository is archived")
	case err != nil:
		ctx.Error(http.StatusInternalServerError, "secretscan_service", err)
	default:
		respondSecretScanningAlert(ctx, alert)
	}
}

// loadSecretScanningAlert finds the alert of the route in this repository only; a number that is not positive, is
// unknown or belongs to another repository is the same 404.
func loadSecretScanningAlert(ctx *context.APIContext) *secretscan_model.Alert {
	index := ctx.ParamsInt64("index")
	if index <= 0 {
		ctx.NotFound()
		return nil
	}
	alert, err := secretscan_model.GetAlert(ctx, ctx.Repo().Repository.ID, index)
	if errors.Is(err, util.ErrNotExist) {
		ctx.NotFound()
		return nil
	}
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "GetAlert", err)
		return nil
	}
	return alert
}

func respondSecretScanningAlert(ctx *context.APIContext, alert *secretscan_model.Alert) {
	repo := ctx.Repo().Repository
	locs, err := secretscan_model.FindLocations(ctx, repo.ID, alert.ID, maxAlertLocations)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "FindLocations", err)
		return
	}
	list, err := convert.ToSecretScanningAlerts(ctx, ctx.Doer(), repo, []*secretscan_model.Alert{alert}, map[int64][]*secretscan_model.Location{alert.ID: locs}, wikiPageURL(repo))
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "ToSecretScanningAlerts", err)
		return
	}
	ctx.JSON(http.StatusOK, list[0])
}

// wikiPageURL turns the git path of a wiki file into the URL of its page ("" for a name that is no page).
func wikiPageURL(repo *repo_model.Repository) func(string) string {
	return func(path string) string {
		wp, err := wiki_service.GitPathToWebPath(path)
		if err != nil {
			return ""
		}
		return repo.HTMLURL() + "/wiki/" + wiki_service.WebPathToURLPath(wp)
	}
}
