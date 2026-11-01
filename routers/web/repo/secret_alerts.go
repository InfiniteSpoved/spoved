// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repo

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"forgejo.org/models/db"
	secretscan_model "forgejo.org/models/secretscan"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/base"
	"forgejo.org/modules/secretdetection"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"
	"forgejo.org/services/context"
	"forgejo.org/services/convert"
	secretscan_service "forgejo.org/services/secretscan"
	vulnerability_service "forgejo.org/services/vulnerability"
	wiki_service "forgejo.org/services/wiki"
)

const (
	tplSecretAlerts base.TplName = "repo/security/secrets"
	tplSecretAlert  base.TplName = "repo/security/secret"

	secretPageSize   = 25
	secretLocPerPage = 10
	maxBulkClose     = 50
)

// MustSeeSecretAlerts is the gate of the secret alert pages: who may see alerts (as for vulnerability alerts), and
// only where the repository has the page: secret scanning is on, or alerts are left from before it was turned off
// (the header worked that out). Everyone else gets the 404 of a missing page. While scanning is off the list only
// shows a banner and the pages of one alert are a 404 (MustScanSecrets).
func MustSeeSecretAlerts(ctx *context.Context) {
	if !vulnerability_service.CanSeeAlerts(&ctx.Repo.Permission, ctx.IsAlertAuditorView()) || ctx.Data["ShowSecretAlerts"] != true {
		ctx.NotFound("MustSeeSecretAlerts", nil)
		return
	}
	enabled := secretscan_service.Enabled(ctx, ctx.Repo.Repository)
	writable := enabled && !ctx.Repo.Repository.IsArchived
	ctx.Data["PageIsSecurity"], ctx.Data["PageIsSecretAlerts"] = true, true
	ctx.Data["SecretScanningOn"] = enabled
	ctx.Data["CanCloseSecretAlerts"] = writable && vulnerability_service.CanDismissAlerts(&ctx.Repo.Permission)
	ctx.Data["CanRescan"] = writable && ctx.IsSigned && ctx.Repo.IsAdmin()
	ctx.Data["CanReadWiki"] = ctx.Repo.CanRead(unit.TypeWiki)
}

// MustScanSecrets 404s the pages of one alert while scanning is off: the alerts are kept but hidden until it is
// turned on again, as in the API.
func MustScanSecrets(ctx *context.Context) {
	if ctx.Data["SecretScanningOn"] != true {
		ctx.NotFound("MustScanSecrets", nil)
	}
}

// mustWriteSecretAlerts is the second check of a POST: closing and reopening are writes.
func mustWriteSecretAlerts(ctx *context.Context) bool {
	if ctx.Data["CanCloseSecretAlerts"] != true {
		ctx.NotFound("mustWriteSecretAlerts", nil)
		return false
	}
	return true
}

// secretAlertRow is an alert as the pages show it.
type secretAlertRow struct {
	*secretscan_model.Alert
	Name     string
	Generic  bool
	First    *secretscan_model.Location
	More     int // places beyond the first the viewer can see
	ClosedBy *user_model.User
}

func secretRules() map[string]secretdetection.Rule {
	rules := map[string]secretdetection.Rule{}
	for _, r := range secretdetection.BuiltinRules() {
		rules[r.ID] = r
	}
	return rules
}

func buildSecretRows(ctx *context.Context, alerts []*secretscan_model.Alert, canReadWiki bool) ([]*secretAlertRow, error) {
	var ids, userIDs []int64
	for _, a := range alerts {
		ids = append(ids, a.ID)
		if a.ClosedByID != 0 {
			userIDs = append(userIDs, a.ClosedByID)
		}
	}
	first, err := secretscan_model.FirstLocations(ctx, ctx.Repo.Repository.ID, ids, !canReadWiki)
	if err != nil {
		return nil, err
	}
	counts, err := secretscan_model.CountLocations(ctx, ctx.Repo.Repository.ID, ids, !canReadWiki)
	if err != nil {
		return nil, err
	}
	users, err := user_model.GetUsersByIDsWithBots(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	rules := secretRules()
	rows := make([]*secretAlertRow, 0, len(alerts))
	for _, a := range alerts {
		r := &secretAlertRow{Alert: a, Name: a.RuleID, First: first[a.ID], More: max(counts[a.ID]-1, 0)}
		if rule, ok := rules[a.RuleID]; ok {
			r.Name, r.Generic = rule.Name, rule.Type == "generic"
		}
		if a.ClosedByID != 0 {
			_, r.ClosedBy = user_model.GetUserFromMap(a.ClosedByID, users)
		}
		rows = append(rows, r)
	}
	return rows, nil
}

// scanStatus is the status line of the list: the summary of the scan rows the viewer may see.
func scanStatus(ctx *context.Context, canReadWiki bool) (*api.SecretScanningStatus, error) {
	scans, err := secretscan_model.FindScans(ctx, ctx.Repo.Repository.ID)
	if err != nil {
		return nil, err
	}
	if !canReadWiki {
		scans = slices.DeleteFunc(scans, func(s *secretscan_model.Scan) bool { return s.Kind == secretscan_model.KindWiki })
	}
	return convert.ToSecretScanningStatus(scans), nil
}

// SecretAlerts lists the secret alerts of the repository.
func SecretAlerts(ctx *context.Context) {
	repo := ctx.Repo.Repository
	canReadWiki := ctx.Repo.CanRead(unit.TypeWiki)
	ctx.Data["Title"] = ctx.Tr("repo.secrets.title")
	if ctx.Data["SecretScanningOn"] != true { // hidden: only the banner, no alert, no count
		if err := secretOffNotes(ctx); err != nil {
			ctx.ServerError("secretOffNotes", err)
			return
		}
		ctx.HTML(http.StatusOK, tplSecretAlerts)
		return
	}
	state := secretscan_model.StateOpen
	if ctx.FormString("state") == "closed" {
		state = "resolved"
	}
	opts := &secretscan_model.AlertSearchOptions{
		ListOptions: db.ListOptions{Page: max(ctx.FormInt("page"), 1), PageSize: secretPageSize},
		RepoID:      repo.ID, State: state, CodeOnly: !canReadWiki,
		Ascending: ctx.FormString("sort") == "oldest",
	}
	if r := ctx.FormString("reason"); state == "resolved" && slices.Contains(secretscan_model.ClosedStates, r) {
		opts.Resolutions = []string{r}
	}
	ruleIDs, err := secretscan_model.FindRuleIDs(ctx, repo.ID, !canReadWiki)
	if err != nil {
		ctx.ServerError("FindRuleIDs", err)
		return
	}
	if t := ctx.FormString("type"); slices.Contains(ruleIDs, t) {
		opts.SecretTypes = []string{t}
	}
	if q := ctx.FormTrim("q"); len(q) <= secretscan_model.MaxFilterLen*4 {
		opts.PathQuery = q
	}
	alerts, total, err := db.FindAndCount[secretscan_model.Alert](ctx, opts)
	if err != nil {
		ctx.ServerError("FindAndCount", err)
		return
	}
	rows, err := buildSecretRows(ctx, alerts, canReadWiki)
	if err != nil {
		ctx.ServerError("buildSecretRows", err)
		return
	}
	status, err := scanStatus(ctx, canReadWiki)
	if err != nil {
		ctx.ServerError("scanStatus", err)
		return
	}
	if err := secretOffNotes(ctx); err != nil {
		ctx.ServerError("secretOffNotes", err)
		return
	}
	rules := secretRules()
	type ruleOption struct{ ID, Name string }
	var types []ruleOption
	for _, id := range ruleIDs {
		name := id
		if r, ok := rules[id]; ok {
			name = r.Name
		}
		types = append(types, ruleOption{id, name})
	}
	typ, reason, sort := strings.Join(opts.SecretTypes, ""), strings.Join(opts.Resolutions, ""), util.Iif(opts.Ascending, "oldest", "")
	ctx.Data["Alerts"], ctx.Data["Total"] = rows, total
	ctx.Data["State"], ctx.Data["Closed"] = ctx.FormString("state"), state == "resolved"
	ctx.Data["Query"], ctx.Data["Type"], ctx.Data["Reason"], ctx.Data["Sort"] = opts.PathQuery, typ, reason, sort
	ctx.Data["HasFilter"] = opts.PathQuery != "" || typ != "" || reason != ""
	ctx.Data["TypeOptions"], ctx.Data["ClosedStates"] = types, secretscan_model.ClosedStates
	ctx.Data["ScanStatus"] = status
	if status.LastScanned != nil {
		ctx.Data["LastScanned"] = timeutil.TimeStamp(status.LastScanned.Unix())
	}
	ctx.Data["TotalSecretAlerts"] = ctx.Data["SecretOpenCount"].(int64) + ctx.Data["SecretClosedCount"].(int64)
	ctx.Data["MaxCommentLen"] = secretscan_model.MaxCommentLen
	pager := context.NewPagination(int(total), secretPageSize, opts.Page, 5)
	for k, v := range map[string]string{"state": ctx.FormString("state"), "q": opts.PathQuery, "type": typ, "reason": reason, "sort": sort} {
		if v != "" {
			pager.AddParamString(k, v)
		}
	}
	ctx.Data["Page"] = pager
	ctx.HTML(http.StatusOK, tplSecretAlerts)
}

// secretOffNotes adds what the banner of a list says while scanning is off: which level decides, and for the
// repository's own switch who turned it off and when.
func secretOffNotes(ctx *context.Context) error {
	if ctx.Data["SecretScanningOn"] == true {
		return nil
	}
	sw, err := secretscan_service.GetRepoSwitch(ctx, ctx.Repo.Repository)
	if err != nil {
		return err
	}
	ctx.Data["Switch"] = sw
	if sw.Site && !sw.Own && (sw.Org == nil || sw.CanChange()) {
		change, err := secretscan_service.LastSwitchChange(ctx, ctx.Repo.Repository.ID)
		if err != nil || change == nil || change.On {
			return err
		}
		ctx.Data["OffBy"], ctx.Data["OffAt"] = secretActorName(ctx, change.ActorID, change.ActorName), change.CreatedUnix
	}
	return nil
}

func secretActorName(ctx *context.Context, id int64, fallback string) string {
	if id > 0 {
		if u, err := user_model.GetUserByID(ctx, id); err == nil {
			return u.GetDisplayName()
		}
	}
	return fallback
}

// loadSecretAlertForPage finds the alert by its number; an alert only in the wiki is missing for someone without Wiki
// read, as in the API.
func loadSecretAlertForPage(ctx *context.Context) *secretscan_model.Alert {
	alert, err := secretscan_model.GetAlert(ctx, ctx.Repo.Repository.ID, ctx.ParamsInt64(":index"))
	if err == nil && !ctx.Repo.CanRead(unit.TypeWiki) {
		var has bool
		if has, err = secretscan_model.HasCodeLocation(ctx, alert.RepoID, alert.ID); err == nil && !has {
			err = secretscan_model.ErrAlertNotExist
		}
	}
	switch {
	case errors.Is(err, secretscan_model.ErrAlertNotExist):
		ctx.NotFound("GetAlert", err)
	case err != nil:
		ctx.ServerError("GetAlert", err)
	default:
		return alert
	}
	return nil
}

type secretGuidance struct {
	Key, Provider, URL string
}

// providerDocs are the pages where a provider explains revoking its credentials.
var providerDocs = map[string]secretGuidance{
	"aws":    {URL: "https://docs.aws.amazon.com/IAM/latest/UserGuide/id_credentials_access-keys.html"},
	"slack":  {URL: "https://api.slack.com/authentication/best-practices"},
	"github": {URL: "https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/token-expiration-and-revocation"},
	"gitlab": {URL: "https://docs.gitlab.com/user/profile/personal_access_tokens/"},
	"gcp":    {URL: "https://cloud.google.com/iam/docs/keys-create-delete"},
	"azure":  {URL: "https://learn.microsoft.com/en-us/azure/storage/common/storage-account-keys-manage"},
	"stripe": {URL: "https://docs.stripe.com/keys"},
}

// guidanceFor is the help box of a type of secret: what to do first, and where the provider explains revoking it.
func guidanceFor(secretType string) secretGuidance {
	switch secretType {
	case "private-key":
		return secretGuidance{Key: "private_key"}
	case "generic":
		return secretGuidance{Key: "generic"}
	}
	if g, ok := providerDocs[secretType]; ok {
		g.Key, g.Provider = "provider", secretType
		return g
	}
	return secretGuidance{Key: "other"}
}

type secretHistoryRow struct {
	*secretscan_service.Event
	Actor string
}

// SecretAlert shows one alert: its places, and what the audit log recorded about it.
func SecretAlert(ctx *context.Context) {
	alert := loadSecretAlertForPage(ctx)
	if alert == nil {
		return
	}
	repo := ctx.Repo.Repository
	canReadWiki := ctx.Repo.CanRead(unit.TypeWiki)
	rows, err := buildSecretRows(ctx, []*secretscan_model.Alert{alert}, canReadWiki)
	if err != nil {
		ctx.ServerError("buildSecretRows", err)
		return
	}
	row := rows[0]
	locOpts := &secretscan_model.LocationSearchOptions{
		ListOptions: db.ListOptions{Page: max(ctx.FormInt("page"), 1), PageSize: secretLocPerPage},
		RepoID:      repo.ID, AlertID: alert.ID, NoWiki: !canReadWiki,
	}
	locs, total, err := db.FindAndCount[secretscan_model.Location](ctx, locOpts)
	if err != nil {
		ctx.ServerError("FindAndCount", err)
		return
	}
	stored, err := secretscan_model.CountLocations(ctx, repo.ID, []int64{alert.ID}, false)
	if err != nil {
		ctx.ServerError("CountLocations", err)
		return
	}
	history, err := secretscan_service.AlertHistory(ctx, repo.ID, alert.ID)
	if err != nil {
		ctx.ServerError("AlertHistory", err)
		return
	}
	events := make([]*secretHistoryRow, 0, len(history))
	for _, e := range history {
		events = append(events, &secretHistoryRow{Event: e, Actor: secretActorName(ctx, e.ActorID, e.ActorName)})
	}
	ctx.Data["Title"] = row.Name
	ctx.Data["Alert"] = row
	ctx.Data["ShortFingerprint"] = alert.Fingerprint[:min(12, len(alert.Fingerprint))]
	ctx.Data["Locations"] = convert.ToSecretScanningLocations(repo, locs, func(path string) string { return wiki_service.PageHTMLURL(repo, path) })
	ctx.Data["NotListed"] = max(alert.LocationCount-stored[alert.ID], 0) // places over the cap, which have no row
	ctx.Data["Guidance"] = guidanceFor(alert.SecretType)
	ctx.Data["History"] = events
	ctx.Data["MaxCommentLen"] = secretscan_model.MaxCommentLen
	ctx.Data["Page"] = context.NewPagination(int(total), secretLocPerPage, locOpts.Page, 5)
	ctx.HTML(http.StatusOK, tplSecretAlert)
}

// secretAlertError flashes the refusal of a close or reopen; it reports whether err was one.
func secretAlertError(ctx *context.Context, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, util.ErrNotExist):
		ctx.NotFound("alert", err)
	case errors.Is(err, secretscan_service.ErrCommentSecret):
		ctx.Flash.Error(ctx.Tr("repo.secrets.close.comment_secret"))
	case errors.Is(err, secretscan_service.ErrCommentTooCostly):
		ctx.Flash.Error(ctx.Tr("repo.secrets.close.comment_costly"))
	case errors.Is(err, util.ErrInvalidArgument):
		ctx.Flash.Error(ctx.Tr("repo.secrets.close.invalid", secretscan_model.MaxCommentLen))
	case errors.Is(err, secretscan_service.ErrConflict):
		ctx.Flash.Error(ctx.Tr("repo.secrets.alert.conflict"))
	default:
		ctx.ServerError("alert", err)
	}
	return true
}

// closeForm reads the reason and the comment of a close: a reason is required, and so is a comment for won't fix.
func closeForm(ctx *context.Context) (reason, comment string, ok bool) {
	reason, comment = strings.TrimSpace(ctx.FormString("reason")), ctx.FormString("comment")
	switch {
	case !slices.Contains(secretscan_model.ClosedStates, reason):
		ctx.Flash.Error(ctx.Tr("repo.secrets.close.no_reason"))
	case reason == secretscan_model.StateWontFix && strings.TrimSpace(comment) == "":
		ctx.Flash.Error(ctx.Tr("repo.secrets.close.wont_fix_comment"))
	default:
		return reason, comment, true
	}
	return "", "", false
}

func secretAlertLink(ctx *context.Context) string {
	return ctx.Repo.RepoLink + "/security/secrets/" + strconv.FormatInt(ctx.ParamsInt64(":index"), 10)
}

// SecretAlertClose closes an alert with a reason and a comment, through the same service as the API.
func SecretAlertClose(ctx *context.Context) {
	if !mustWriteSecretAlerts(ctx) || loadSecretAlertForPage(ctx) == nil {
		return
	}
	if reason, comment, ok := closeForm(ctx); ok {
		_, err := secretscan_service.Close(ctx, ctx.Repo.Repository, ctx.Doer, ctx.ParamsInt64(":index"), reason, comment, ctx.Repo.CanRead(unit.TypeWiki))
		if secretAlertError(ctx, err) {
			if ctx.Written() {
				return
			}
		} else {
			ctx.Flash.Success(ctx.Tr("repo.secrets.alert.closed_done"))
		}
	}
	ctx.Redirect(secretAlertLink(ctx))
}

// SecretAlertReopen reopens a closed alert.
func SecretAlertReopen(ctx *context.Context) {
	if !mustWriteSecretAlerts(ctx) || loadSecretAlertForPage(ctx) == nil {
		return
	}
	_, err := secretscan_service.Reopen(ctx, ctx.Repo.Repository, ctx.ParamsInt64(":index"))
	if secretAlertError(ctx, err) {
		if ctx.Written() {
			return
		}
	} else {
		ctx.Flash.Success(ctx.Tr("repo.secrets.alert.reopened_done"))
	}
	ctx.Redirect(secretAlertLink(ctx))
}

// SecretAlertsClose closes the selected alerts of the list with one reason, one alert at a time through the service
// (an audit entry each); the ones that can't be closed (gone, closed meanwhile, wiki only for this viewer) are counted.
func SecretAlertsClose(ctx *context.Context) {
	if !mustWriteSecretAlerts(ctx) {
		return
	}
	back := ctx.Repo.RepoLink + "/security/secrets"
	reason, comment, ok := closeForm(ctx)
	selected := slices.Compact(slices.Sorted(slices.Values(ctx.Req.PostForm["index"])))
	if ok && (len(selected) == 0 || len(selected) > maxBulkClose) {
		ctx.Flash.Error(ctx.Tr("repo.secrets.bulk.none", maxBulkClose))
		ok = false
	}
	if !ok {
		ctx.Redirect(back)
		return
	}
	canReadWiki := ctx.Repo.CanRead(unit.TypeWiki)
	closed, failed := 0, 0
	for _, s := range selected {
		index, err := strconv.ParseInt(s, 10, 64)
		if err == nil {
			var alert *secretscan_model.Alert
			if alert, err = secretscan_model.GetAlert(ctx, ctx.Repo.Repository.ID, index); err == nil && !canReadWiki {
				var has bool
				if has, err = secretscan_model.HasCodeLocation(ctx, alert.RepoID, alert.ID); err == nil && !has {
					err = secretscan_model.ErrAlertNotExist
				}
			}
		} else {
			err = secretscan_model.ErrAlertNotExist
		}
		if err == nil {
			_, err = secretscan_service.Close(ctx, ctx.Repo.Repository, ctx.Doer, index, reason, comment, canReadWiki)
		}
		switch {
		case err == nil:
			closed++
		case errors.Is(err, secretscan_model.ErrAlertNotExist), errors.Is(err, secretscan_service.ErrConflict):
			failed++
		case errors.Is(err, util.ErrInvalidArgument):
			secretAlertError(ctx, err) // the comment is the same for every alert: nothing more to try
			ctx.Redirect(back)
			return
		default:
			ctx.ServerError("SecretAlertsClose", err)
			return
		}
	}
	ctx.Flash.Success(ctx.Locale.TrPluralString(closed, "repo.secrets.bulk.closed", closed))
	if failed > 0 {
		ctx.Flash.Warning(ctx.Locale.TrPluralString(failed, "repo.secrets.bulk.failed", failed))
	}
	ctx.Redirect(back)
}

// SecretRescan scans the repository again from the beginning (a repository admin's action).
func SecretRescan(ctx *context.Context) {
	if ctx.Data["CanRescan"] != true {
		ctx.NotFound("SecretRescan", nil)
		return
	}
	switch err := secretscan_service.Rescan(ctx, ctx.Repo.Repository); {
	case errors.Is(err, secretscan_service.ErrNotEnabled):
		ctx.Flash.Error(ctx.Tr("repo.secrets.rescan.off"))
	case err != nil:
		ctx.ServerError("Rescan", err)
		return
	default:
		ctx.Flash.Success(ctx.Tr("repo.secrets.rescan.started"))
	}
	ctx.Redirect(ctx.Repo.RepoLink + "/security/secrets")
}
