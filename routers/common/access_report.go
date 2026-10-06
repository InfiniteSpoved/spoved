// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package common

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/json"
	"forgejo.org/modules/log"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/util"
	"forgejo.org/services/accessreport"
	"forgejo.org/services/context"
	"forgejo.org/services/convert"

	"xorm.io/builder"
)

// AccessReport is the access report of the repositories of an org, or of every repository if Owner is nil, which
// only site admins may see. It is paged by repository.
type AccessReport struct {
	Owner  *user_model.User
	Doer   *user_model.User
	Filter accessreport.Filter
}

const accessReportBatch = 100

func (r *AccessReport) cond() builder.Cond {
	if r.Owner == nil {
		return builder.NewCond()
	}
	return builder.Eq{"owner_id": r.Owner.ID}
}

func (r *AccessReport) repos(ctx *context.Base, page, size int) ([]*repo_model.Repository, error) {
	repos := make([]*repo_model.Repository, 0, size)
	return repos, db.GetEngine(ctx).Where(r.cond()).OrderBy("id").Limit(size, (page-1)*size).Find(&repos)
}

// lines returns the lines of the repositories, with the person columns for site admins only.
func (r *AccessReport) lines(ctx *context.Base, repos []*repo_model.Repository) ([]*api.AccessReportLine, bool, error) {
	rows, activity, err := accessreport.ForRepos(ctx, r.Owner, repos, r.Doer, r.Filter)
	if err != nil {
		return nil, false, err
	}
	var people map[int64]*accessreport.Person
	if r.Doer.IsAdmin {
		repoIDs := make([]int64, 0, len(repos))
		for _, repo := range repos {
			repoIDs = append(repoIDs, repo.ID)
		}
		if people, err = accessreport.People(ctx, rows, repoIDs); err != nil {
			return nil, false, err
		}
	}
	return convert.ToAccessReportLines(ctx, rows, r.Doer, people), activity, nil
}

// Page returns the lines of a page of repositories, at most 100 per page, and how many repositories there are.
func (r *AccessReport) Page(ctx *context.Base, page, size int) (lines []*api.AccessReportLine, total int64, activity bool, err error) {
	repos, err := r.repos(ctx, page, size)
	if err != nil {
		return nil, 0, false, err
	}
	if lines, activity, err = r.lines(ctx, repos); err != nil {
		return nil, 0, false, err
	}
	total, err = db.GetEngine(ctx).Where(r.cond()).Count(new(repo_model.Repository))
	return lines, total, activity, err
}

func csvTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func csvBool(b *bool) string {
	if b == nil {
		return ""
	}
	return strconv.FormatBool(*b)
}

// Export streams the lines of every repository as CSV or JSON, in batches of repositories. It returns an error
// only if it wrote nothing.
func (r *AccessReport) Export(ctx *context.Base, format string) error {
	var write func(l *api.AccessReportLine) error
	var finish func() error
	if format == "csv" {
		ctx.SetServeHeaders(&context.ServeHeaderOptions{ContentType: "text/csv", ContentTypeCharset: "utf-8", Filename: "access-report.csv"})
		w := csv.NewWriter(ctx.Resp)
		header := []string{"type", "name", "full_name", "repository", "unit", "access", "path", "team", "user_groups", "grant", "applied"}
		if r.Doer.IsAdmin {
			header = append(header, "last_sign_in", "last_push", "two_factor", "account_source", "suspended")
		}
		if err := w.Write(header); err != nil {
			return err
		}
		write = func(l *api.AccessReportLine) error {
			groups := make([]string, 0, len(l.UserGroups))
			for _, g := range l.UserGroups {
				if g.LoginSource != "" {
					groups = append(groups, g.Name+" ("+g.LoginSource+")")
				} else {
					groups = append(groups, g.Name)
				}
			}
			rec := []string{
				l.Type, util.CSVSafe(l.Name), util.CSVSafe(l.FullName), util.CSVSafe(l.Repository), l.Unit, l.Access, l.Path,
				util.CSVSafe(l.Team), util.CSVSafe(strings.Join(groups, "; ")), l.Grant, strconv.FormatBool(l.Applied),
			}
			if r.Doer.IsAdmin {
				rec = append(rec, csvTime(l.LastLogin), csvTime(l.LastPush), csvBool(l.TwoFactor), util.CSVSafe(l.AccountSource), csvBool(l.Suspended))
			}
			return w.Write(rec)
		}
		finish = func() error {
			w.Flush()
			return w.Error()
		}
	} else {
		ctx.SetServeHeaders(&context.ServeHeaderOptions{ContentType: "application/json", ContentTypeCharset: "utf-8", Filename: "access-report.json"})
		enc := json.NewEncoder(ctx.Resp)
		sep := "["
		write = func(l *api.AccessReportLine) error {
			if _, err := ctx.Resp.Write([]byte(sep)); err != nil {
				return err
			}
			sep = ","
			return enc.Encode(l)
		}
		finish = func() error {
			end := "]\n"
			if sep == "[" {
				end = "[]\n"
			}
			_, err := ctx.Resp.Write([]byte(end))
			return err
		}
	}

	err := r.export(ctx, write)
	if err == nil {
		err = finish()
	}
	if err == nil {
		return nil
	}
	if ctx.Resp.WrittenStatus() == 0 {
		ctx.Resp.Header().Del("Content-Disposition")
		return err
	}
	// the body has started: the log records why
	log.Error("AccessReport.Export: %v", err)
	panic(http.ErrAbortHandler) // abort so the client sees a failed download, not a short file
}

func (r *AccessReport) export(ctx *context.Base, write func(l *api.AccessReportLine) error) error {
	for page := 1; ; page++ {
		repos, err := r.repos(ctx, page, accessReportBatch)
		if err != nil {
			return err
		}
		lines, _, err := r.lines(ctx, repos)
		if err != nil {
			return err
		}
		for _, l := range lines {
			if err := write(l); err != nil {
				return err
			}
		}
		if len(repos) < accessReportBatch {
			return nil
		}
	}
}
