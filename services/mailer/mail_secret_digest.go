// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package mailer

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"html/template"
	"slices"
	"strconv"

	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	secretscan_model "forgejo.org/models/secretscan"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/base"
	"forgejo.org/modules/log"
	"forgejo.org/modules/secretdetection"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/translation"
	vulnerability_service "forgejo.org/services/vulnerability"
)

const tplSecretDigest base.TplName = "secret/digest"

// SecretDigestInput is what the scan hands over after a push: the alerts above AfterAlertID are the new ones, in the
// repository's push to Ref (a short name, "" for the wiki) at Commit; PusherID is the person who pushed, 0 when nobody did.
type SecretDigestInput struct {
	Ref, Commit  string
	PusherID     int64
	AfterAlertID int64
}

// SendSecretDigest mails the repository's administrators one digest of the new secret alerts, and the pusher their own
// mail. Everyone is checked as they are now: an active individual with an address, who can see alerts (CanSeeAlerts)
// (the pusher too) and, for the alerts that are only in the wiki, can read the wiki, and whose notification settings allow the event.
// A pusher who is an administrator gets the digest only. The mail never holds a secret or a fingerprint: only types,
// counts and places. It runs after the scan's transaction is committed.
func SendSecretDigest(ctx context.Context, repo *repo_model.Repository, in SecretDigestInput) error {
	if setting.MailService == nil {
		return nil
	}
	admins, err := repoAdminRecipients(ctx, repo.ID)
	if err != nil {
		return err
	}
	users := admins.users
	adminIDs := map[int64]bool{}
	for _, u := range users {
		adminIDs[u.ID] = true
	}
	var pusher *user_model.User
	if in.PusherID > 0 && !adminIDs[in.PusherID] {
		if u, err := user_model.GetUserByID(ctx, in.PusherID); err == nil && mailable(u) {
			pusher = u
			users = append(slices.Clip(users), u)
		}
	}
	ids := make([]int64, len(users))
	for i, u := range users {
		ids[i] = u.ID
	}
	filter, err := newEmailFilter(ctx, repo.ID, ids...)
	if err != nil {
		log.Error("newEmailFilter: %v", err) // fall back to the preset and defaults
	}
	digests := map[bool]*secretscan_model.Digest{} // by noWiki
	for _, u := range users {
		isPusher := pusher != nil && u.ID == pusher.ID
		perm, err := access_model.GetUserRepoPermission(ctx, repo, u)
		if err != nil {
			return err
		}
		if !vulnerability_service.CanSeeAlerts(&perm, false) {
			continue // the pusher too: the mail names alerts and links to them
		}
		if !filter.ShouldEmail(u, user_model.NotifyEventSecretAlert) {
			continue
		}
		noWiki := !perm.CanRead(unit.TypeWiki)
		d, ok := digests[noWiki]
		if !ok {
			if d, err = secretscan_model.BuildDigest(ctx, repo.ID, in.AfterAlertID, noWiki); err != nil {
				return err
			}
			digests[noWiki] = d
		}
		if d.Alerts == 0 {
			continue
		}
		if err := sendSecretDigestMail(repo, u, d, in, isPusher); err != nil {
			log.Error("secret digest to %d: %v", u.ID, err)
		}
	}
	return nil
}

func sendSecretDigestMail(repo *repo_model.Repository, u *user_model.User, d *secretscan_model.Digest, in SecretDigestInput, isPusher bool) error {
	locale := translation.NewLocale(u.Language)
	name := repo.FullName()
	where := locale.TrString("mail.secret_digest.where_wiki")
	if in.Ref != "" {
		where = in.Ref
		if in.Commit != "" {
			where = locale.TrString("mail.secret_digest.where_commit", in.Ref, in.Commit[:min(7, len(in.Commit))])
		}
	}
	names := map[string]string{}
	for _, r := range secretdetection.BuiltinRules() {
		names[r.ID] = r.Name
	}
	type typeRow struct {
		Name  string
		Count int
	}
	types := make([]typeRow, len(d.Types))
	for i, t := range d.Types {
		types[i] = typeRow{Name: names[t.RuleID], Count: t.Count}
		if types[i].Name == "" {
			types[i].Name = t.RuleID
		}
	}
	places := make([]string, len(d.Locations))
	for i, l := range d.Locations {
		places[i] = l.Path
		if l.StartLine > 0 {
			places[i] += ":" + strconv.Itoa(l.StartLine)
		}
		if l.Kind == secretscan_model.KindWiki {
			places[i] = locale.TrString("mail.secret_digest.wiki_place", places[i])
		}
	}
	// literal keys, so that lint-locale-usage finds them; the plural phrases escape their arguments for HTML
	var subjectHTML, intro, advice, foot template.HTML
	if isPusher {
		subjectHTML = locale.TrPluralString(d.Alerts, "mail.secret_digest.subject_pusher", name)
		intro = locale.TrPluralString(d.Alerts, "mail.secret_digest.intro_pusher", where, d.Alerts)
		advice, foot = locale.Tr("mail.secret_digest.advice_pusher"), locale.Tr("mail.secret_digest.foot_pusher", name)
	} else {
		subjectHTML = locale.TrPluralString(d.Alerts, "mail.secret_digest.subject", name)
		intro = locale.TrPluralString(d.Alerts, "mail.secret_digest.intro", where, d.Alerts)
		advice, foot = locale.Tr("mail.secret_digest.advice_admin"), locale.Tr("mail.secret_digest.foot_admin", name)
	}
	subject := html.UnescapeString(string(subjectHTML))
	data := map[string]any{
		"locale":     locale,
		"Language":   locale.Language(),
		"Title":      subject,
		"Intro":      intro,
		"Types":      types,
		"Places":     places,
		"PlacesHead": locale.TrPluralString(len(places), "mail.secret_digest.places"),
		"Advice":     advice,
		"Footer":     foot,
		"Settings":   setting.AppURL + "user/settings/notifications",
		"Link":       repo.HTMLURL() + "/security/secrets",
		"LinkText":   locale.TrPluralString(d.Alerts, "mail.secret_digest.open"),
	}
	if d.TotalLocations > len(places) {
		data["PlacesHead"] = locale.Tr("mail.secret_digest.places_capped", len(places), d.TotalLocations)
		data["More"] = locale.TrPluralString(d.TotalLocations-len(places), "mail.secret_digest.more", d.TotalLocations-len(places))
	}
	var content bytes.Buffer
	if err := bodyTemplates.ExecuteTemplate(&content, string(tplSecretDigest), data); err != nil {
		return fmt.Errorf("ExecuteTemplate: %w", err)
	}
	msg := NewMessage(u.EmailTo(), subject, content.String())
	msg.Info = fmt.Sprintf("Secret digest: repository %d", repo.ID)
	SendAsync(msg)
	return nil
}
