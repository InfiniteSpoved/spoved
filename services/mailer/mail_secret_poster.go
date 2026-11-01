// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package mailer

import (
	"bytes"
	"context"
	"fmt"
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
	CommentID int64 // 0 for the title or body of the issue
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
	c := cache.GetCache()
	key := "secret_poster_notices_" + strconv.FormatInt(u.ID, 10) + "_" + timeutil.TimeStampNow().AsTime().UTC().Format("2006-01-02")
	sent := int64(0)
	if c != nil {
		if v, err := util.ToInt64(c.Get(key)); err == nil {
			sent = v
		}
		if sent >= MaxPosterNoticesPerDay {
			return nil
		}
	}
	issue.Repo = repo
	link := issue.HTMLURL()
	if n.CommentID != 0 {
		link += "#issuecomment-" + strconv.FormatInt(n.CommentID, 10)
	}
	locale := translation.NewLocale(u.Language)
	name := repo.FullName()
	subject := locale.TrString("mail.secret_poster.subject", name)
	data := map[string]any{
		"locale":   locale,
		"Language": locale.Language(),
		"Title":    subject,
		"Intro":    locale.Tr("mail.secret_poster.intro", name),
		"Advice":   locale.Tr("mail.secret_poster.advice"),
		"Link":     link,
		"LinkText": locale.Tr("mail.secret_poster.open"),
		"Footer":   locale.Tr("mail.secret_poster.foot", name),
		"Settings": setting.AppURL + "user/settings/notifications",
	}
	var content bytes.Buffer
	if err := bodyTemplates.ExecuteTemplate(&content, string(tplSecretPoster), data); err != nil {
		return fmt.Errorf("ExecuteTemplate: %w", err)
	}
	msg := NewMessage(u.EmailTo(), subject, content.String())
	msg.Info = fmt.Sprintf("Secret notice to the poster: repository %d", repo.ID)
	SendAsync(msg)
	if c != nil {
		if err := c.Put(key, sent+1, 2*24*60*60); err != nil {
			log.Error("secret poster notice: store the count of user %d: %v", u.ID, err)
		}
	}
	return nil
}
