// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package mailer

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"html/template"
	"strconv"

	issues_model "forgejo.org/models/issues"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/base"
	"forgejo.org/modules/cache"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/translation"
	"forgejo.org/modules/util"
)

const tplSecretPoster base.TplName = "secret/poster"

// MaxPosterNoticesPerDay is the most mails of this kind one person gets in a (UTC) day, over all repositories: a text
// full of secrets, or an import of many, can't flood anyone.
const MaxPosterNoticesPerDay = 5

// SecretPosterNotice names a text (an issue or pull request, or one of its comments) whose author is to be told that it
// holds a secret.
type SecretPosterNotice struct {
	PosterID  int64
	IssueID   int64
	CommentID int64  // 0 for the title or body of the issue
	Field     string // title or body, for a text that is not a comment
}

// SendSecretPosterNotice tells the author of a text that it seems to hold a secret, so they can remove it and revoke
// the credential. It goes only to an active individual with an address who is not an administrator of the repository
// (the administrators get the digest), can still read that issue or pull request and whose notification settings allow
// the event (ShouldEmail), and at most MaxPosterNoticesPerDay a day. The mail links to the text, never to the alert
// (the author may not be allowed to see alerts), and holds neither the secret, its type nor any of the text.
func SendSecretPosterNotice(ctx context.Context, repo *repo_model.Repository, n SecretPosterNotice) error {
	if setting.MailService == nil || n.PosterID <= 0 {
		return nil
	}
	u, err := user_model.GetUserByID(ctx, n.PosterID)
	if err != nil {
		if user_model.IsErrUserNotExist(err) {
			return nil
		}
		return err
	}
	if !mailable(u) {
		return nil
	}
	issue, err := issues_model.GetIssueByID(ctx, n.IssueID)
	if err != nil {
		if issues_model.IsErrIssueNotExist(err) {
			return nil
		}
		return err
	}
	if issue.RepoID != repo.ID {
		return nil
	}
	perm, err := access_model.GetUserRepoPermission(ctx, repo, u)
	if err != nil {
		return err
	}
	if perm.IsAdmin() || !perm.CanRead(util.Iif(issue.IsPull, unit.TypePullRequests, unit.TypeIssues)) || !ShouldEmail(ctx, u, repo, user_model.NotifyEventSecretAlert) {
		return nil
	}
	if !takePosterSlot(u.ID) {
		return nil
	}
	issue.Repo = repo
	ref := fmt.Sprintf("%s#%d", repo.FullName(), issue.Index)
	link := issue.HTMLURL()
	locale := translation.NewLocale(u.Language)
	// literal keys, so that lint-locale-usage finds them
	var item, open template.HTML
	switch {
	case n.CommentID != 0:
		link += "#issuecomment-" + strconv.FormatInt(n.CommentID, 10)
		item, open = locale.Tr("mail.secret_poster.item.comment"), locale.Tr("mail.secret_poster.open_comment")
	case issue.IsPull && n.Field == "title":
		item, open = locale.Tr("mail.secret_poster.item.pull_title"), locale.Tr("mail.secret_poster.open_pull")
	case issue.IsPull:
		item, open = locale.Tr("mail.secret_poster.item.pull_body"), locale.Tr("mail.secret_poster.open_pull")
	case n.Field == "title":
		item, open = locale.Tr("mail.secret_poster.item.issue_title"), locale.Tr("mail.secret_poster.open_issue")
	default:
		item, open = locale.Tr("mail.secret_poster.item.issue_body"), locale.Tr("mail.secret_poster.open_issue")
	}
	subject := html.UnescapeString(string(locale.Tr("mail.secret_poster.subject", item, ref)))
	data := map[string]any{
		"locale":   locale,
		"Language": locale.Language(),
		"Title":    subject,
		"Intro":    locale.Tr("mail.secret_poster.intro", item, ref),
		"Advice":   locale.Tr("mail.secret_poster.advice"),
		"NotReal":  locale.Tr("mail.secret_poster.not_real"),
		"Link":     link,
		"LinkText": open,
		"Footer":   locale.Tr("mail.secret_poster.foot", repo.FullName(), MaxPosterNoticesPerDay),
		"Settings": setting.AppURL + "user/settings/notifications",
	}
	var content bytes.Buffer
	if err := bodyTemplates.ExecuteTemplate(&content, string(tplSecretPoster), data); err != nil {
		return fmt.Errorf("ExecuteTemplate: %w", err)
	}
	msg := NewMessage(u.EmailTo(), subject, content.String())
	msg.Info = fmt.Sprintf("Secret notice to the poster: repository %d", repo.ID)
	SendAsync(msg)
	return nil
}

// takePosterSlot takes one of the person's MaxPosterNoticesPerDay mails of the (UTC) day before the mail is made: the
// counter is incremented first and the mail goes only while it is within the limit, so two workers can't both take the
// last slot (the first creation of the day's key can still race). The counter is in the cache: with the default
// in-memory cache each server counts for itself, with a shared cache (Redis) the limit holds across servers.
func takePosterSlot(userID int64) bool {
	c := cache.GetCache()
	if c == nil {
		return true
	}
	key := "secret_poster_notices_" + strconv.FormatInt(userID, 10) + "_" + timeutil.TimeStampNow().AsTime().UTC().Format("2006-01-02")
	if err := c.Incr(key); err != nil { // no counter yet today
		if err := c.Put(key, int64(1), 2*24*60*60); err != nil {
			log.Error("secret poster notice: store the count of user %d: %v", userID, err)
		}
		return true
	}
	n, err := util.ToInt64(c.Get(key))
	return err != nil || n <= MaxPosterNoticesPerDay
}
