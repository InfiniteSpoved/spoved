// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package security

import (
	"errors"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	secretscan_model "forgejo.org/models/secretscan"
	"forgejo.org/modules/log"
	"forgejo.org/modules/secretdetection"
	"forgejo.org/modules/setting"
	"forgejo.org/routers/common"
	"forgejo.org/services/context"
	secretscan_service "forgejo.org/services/secretscan"
)

// maxSecretQueryLen is the longest text of the repository search.
const maxSecretQueryLen = 128

// SecretsForm is what the secret alert list's form sends.
type SecretsForm struct {
	State   string
	Reason  string
	Type    string
	Query   string
	Project string
	Owner   string
}

func readSecretsForm(ctx *context.Context) SecretsForm {
	return SecretsForm{
		State: ctx.FormTrim("state"), Reason: ctx.FormTrim("reason"), Type: ctx.FormTrim("type"),
		Query: ctx.FormTrim("q"), Project: ctx.FormTrim("project"), Owner: ctx.FormTrim("owner"),
	}
}

// Values is the form as URL parameters: the links of the pager and of a refused download.
func (f SecretsForm) Values() url.Values {
	q := url.Values{}
	for k, v := range map[string]string{"state": f.State, "reason": f.Reason, "type": f.Type, "q": f.Query, "project": f.Project, "owner": f.Owner} {
		if v != "" {
			q.Set(k, v)
		}
	}
	return q
}

// SecretType is a choice of the type filter.
type SecretType struct{ ID, Name string }

// SecretsPage is the data of a list page, as ctx.Data["Secrets"].
type SecretsPage struct {
	Link         string // the page's own path: the form posts to it and the pager links to it
	Form         SecretsForm
	Admin        bool // across every owner (the instance list)
	Narrow       bool // an organization list of a member: only the repositories where they can write
	Ignored      bool // a filter value was not valid and is not applied
	HasFilter    bool
	Types        []SecretType
	ClosedStates []string
	Projects     []*repo_model.RepoGroup
	Result       *secretscan_service.Result
}

func secretTypes() []SecretType {
	rules := secretdetection.BuiltinRules()
	out := make([]SecretType, 0, len(rules))
	for _, r := range rules {
		out = append(out, SecretType{r.ID, r.Name})
	}
	slices.SortFunc(out, func(a, b SecretType) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// scopeOf puts the form's project (organization lists) or owner (instance list) and search text into the scope.
func (f SecretsForm) scopeOf(scope secretscan_service.Scope) secretscan_service.Scope {
	if scope.Org != nil {
		scope.Project = f.Project
	} else {
		scope.Owner = f.Owner
	}
	scope.Query = f.Query
	return scope
}

// clean drops a text that is too long, and says so.
func (f *SecretsForm) clean() (ignored bool) {
	if len(f.Query) > maxSecretQueryLen {
		f.Query, ignored = "", true
	}
	return ignored
}

// Secrets runs the list the request asks for and sets ctx.Data["Secrets"]. scope is the visibility rule, link the
// page's path.
func Secrets(ctx *context.Context, scope secretscan_service.Scope, link string) bool {
	f := readSecretsForm(ctx)
	ignored := f.clean()
	filter, filterIgnored := secretscan_service.ParseFilter(f.State, f.Reason, f.Type)
	f.State, f.Reason, f.Type = filter.State, filter.Reason, filter.Type
	page := &SecretsPage{
		Link: link, Form: f, Admin: scope.Org == nil, Narrow: scope.Narrow, Ignored: ignored || filterIgnored,
		HasFilter: f != SecretsForm{}, Types: secretTypes(), ClosedStates: secretscan_model.ClosedStates,
	}
	ctx.Data["Secrets"] = page
	if scope.Org != nil {
		groups, _, err := repo_model.FindVisibleRepoGroups(ctx, ctx.Doer, scope.Org.ID, "", db.ListOptions{ListAll: true})
		if err != nil {
			ctx.ServerError("FindVisibleRepoGroups", err)
			return false
		}
		page.Projects = groups
	}
	lo := db.ListOptions{Page: max(ctx.FormInt("page"), 1), PageSize: setting.UI.IssuePagingNum}
	res, err := secretscan_service.Search(ctx, f.scopeOf(scope), filter, lo)
	if err != nil {
		ctx.ServerError("secretscan Search", err)
		return false
	}
	page.Result = res
	if res.Total > int64(lo.PageSize) {
		pager := context.NewPagination(int(res.Total), lo.PageSize, lo.Page, 5)
		q := f.Values()
		for _, k := range slices.Sorted(maps.Keys(q)) {
			pager.AddParamString(k, q.Get(k))
		}
		ctx.Data["Page"] = pager
	}
	return true
}

// SecretsExport sends the CSV download the posted form asks for. A refusal before the first byte (a wrong filter,
// too many rows, no free slot) goes back to the page with a message.
func SecretsExport(ctx *context.Context, scope secretscan_service.Scope, link string) {
	f := readSecretsForm(ctx)
	ignored := f.clean()
	filter, filterIgnored := secretscan_service.ParseFilter(f.State, f.Reason, f.Type)
	var err error
	if ignored || filterIgnored {
		err = errors.New("invalid filter")
	} else {
		err = common.ExportSecretAlerts(ctx.Base, ctx.Doer, f.scopeOf(scope), filter, func(a *secretscan_model.Alert, repo *repo_model.Repository) string {
			return repo.HTMLURL() + "/security/secrets/" + strconv.FormatInt(a.Index, 10)
		})
	}
	if err == nil {
		return
	}
	switch {
	case errors.Is(err, common.ErrExportBusy):
		ctx.Flash.Error(ctx.Tr("security_search.export_busy"))
	case errors.Is(err, secretscan_service.ErrExportTooMany):
		ctx.Flash.Error(ctx.Tr("secret_search.export_too_many"))
	case errors.Is(err, common.ErrExportTimeLimit):
		ctx.Flash.Error(ctx.Tr("security_search.export_time_limit"))
	case ignored || filterIgnored:
		ctx.Flash.Error(ctx.Tr("security_search.export_invalid"))
	default:
		log.Error("secret alert export: %v", err)
		ctx.ServerError("SecretsExport", err)
		return
	}
	target := link
	if q := f.Values().Encode(); q != "" {
		target += "?" + q
	}
	ctx.Redirect(target)
}
