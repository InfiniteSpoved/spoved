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
	"strings"

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

// maxDigestRecipients is the most administrators one digest is mailed to: a repository with more is logged, and the
// rest read the alerts on the page. (Permissions are checked one recipient at a time, so the number is bounded.)
const maxDigestRecipients = 100

// SecretDigestInput is what the scan hands over after a push: the alerts above AfterAlertID are the new ones, in the
// repository's push to Ref (a short name, "" for the wiki) at Commit; PusherID is the person who pushed, 0 when nobody
// did. Kinds are the secretscan_model.DigestKind* bits of what the pushes touched.
type SecretDigestInput struct {
	Ref, Commit  string
	PusherID     int64
	AfterAlertID int64
	Kinds        int
}

// digestKinds are the kinds of places a recipient is told about. An administrator hears of every new alert (the wiki's
// only when the wiki had been scanned before the push, and the recipient can read it); the pusher only of what they
// pushed: code for a branch or tag, the wiki for the wiki, so a fork pull request or a wiki edit is never "your push".
func digestKinds(in SecretDigestInput, isPusher, wikiRead bool) []string {
	var kinds []string
	if isPusher {
		if in.Kinds&secretscan_model.DigestKindCode != 0 {
			kinds = append(kinds, secretscan_model.KindRepo)
		}
		if in.Kinds&secretscan_model.DigestKindWiki != 0 && wikiRead {
			kinds = append(kinds, secretscan_model.KindWiki)
		}
		return kinds
	}
	kinds = []string{secretscan_model.KindRepo, secretscan_model.KindPull}
	if in.Kinds&secretscan_model.DigestWikiScanned != 0 && wikiRead {
		kinds = append(kinds, secretscan_model.KindWiki)
	}
	return kinds
}

// SendSecretDigest mails the repository's administrators one digest of the new secret alerts, and the pusher their own
// mail. Everyone is checked as they are now: an active individual with an address, who can see alerts (CanSeeAlerts)
// (the pusher too) and whose notification settings allow the event; wiki alerts need wiki read. A pusher who is an
// administrator gets the digest only. The mail never holds a secret or a fingerprint: only types, counts and places.
// It runs after the scan's transaction is committed.
func SendSecretDigest(ctx context.Context, repo *repo_model.Repository, in SecretDigestInput) error {
	if setting.MailService == nil {
		return nil
	}
	admins, err := repoAdminRecipients(ctx, repo.ID)
	if err != nil {
		return err
	}
	users := admins.users
	if len(users) > maxDigestRecipients {
		log.Warn("secret digest of repository %d: %d administrators, mailed to the first %d", repo.ID, len(users), maxDigestRecipients)
		users = users[:maxDigestRecipients]
	}
	adminIDs := map[int64]bool{}
	for _, u := range users {
		adminIDs[u.ID] = true
	}
	adminUsers := users
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
	names := map[string]string{} // rule names, once per digest
	for _, r := range secretdetection.BuiltinRules() {
		names[r.ID] = r.Name
	}
	digests := map[string]*secretscan_model.Digest{} // by kinds
	adminsNotified := false
	for _, u := range adminUsers { // the administrators first: the pusher's mail says whether they were told
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
		wikiRead := perm.CanRead(unit.TypeWiki)
		kinds := digestKinds(in, false, wikiRead)
		if len(kinds) == 0 {
			continue
		}
		key := strings.Join(kinds, ",")
		d, ok := digests[key]
		if !ok {
			if d, err = secretscan_model.BuildDigest(ctx, repo.ID, in.AfterAlertID, kinds); err != nil {
				return err
			}
			digests[key] = d
		}
		if d.Alerts == 0 {
			continue
		}
		if err := sendSecretDigestMail(repo, u, d, in, names, false, wikiRead, false); err != nil {
			log.Error("secret digest to %d: %v", u.ID, err)
			continue
		}
		adminsNotified = true
	}
	if pusher == nil {
		return nil
	}
	perm, err := access_model.GetUserRepoPermission(ctx, repo, pusher)
	if err != nil || !vulnerability_service.CanSeeAlerts(&perm, false) || !filter.ShouldEmail(pusher, user_model.NotifyEventSecretAlert) {
		return err
	}
	wikiRead := perm.CanRead(unit.TypeWiki)
	kinds := digestKinds(in, true, wikiRead)
	if len(kinds) == 0 {
		return nil
	}
	d, ok := digests[strings.Join(kinds, ",")]
	if !ok {
		if d, err = secretscan_model.BuildDigest(ctx, repo.ID, in.AfterAlertID, kinds); err != nil {
			return err
		}
	}
	if d.Alerts == 0 {
		return nil
	}
	if err := sendSecretDigestMail(repo, pusher, d, in, names, true, wikiRead, adminsNotified); err != nil {
		log.Error("secret digest to %d: %v", pusher.ID, err)
	}
	return nil
}

func sendSecretDigestMail(repo *repo_model.Repository, u *user_model.User, d *secretscan_model.Digest, in SecretDigestInput, names map[string]string, isPusher, wikiRead, adminsNotified bool) error {
	locale := translation.NewLocale(u.Language)
	name := repo.FullName()
	// the push as the mail names it: the branch as code, the commit linked (the arguments are HTML, built escaped); a
	// wiki push is named only to someone who can read the wiki
	where := locale.Tr("mail.secret_digest.where_push")
	switch {
	case in.Ref != "":
		where = template.HTML("<code>" + template.HTMLEscapeString(in.Ref) + "</code>")
		if in.Commit != "" {
			short := in.Commit[:min(7, len(in.Commit))]
			link := template.HTML(`<a href="` + template.HTMLEscapeString(repo.HTMLURL()+"/commit/"+in.Commit) + `">` + template.HTMLEscapeString(short) + "</a>")
			where = locale.Tr("mail.secret_digest.where_commit", where, link)
		}
	case wikiRead:
		where = locale.Tr("mail.secret_digest.where_wiki")
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
	var subjectHTML, intro, advice, foot, notified template.HTML
	if isPusher {
		subjectHTML = locale.TrPluralString(d.Alerts, "mail.secret_digest.subject_pusher", name)
		intro = locale.TrPluralString(d.Alerts, "mail.secret_digest.intro_pusher", where, d.Alerts)
		advice, foot = locale.TrPluralString(d.Alerts, "mail.secret_digest.advice_pusher"), locale.Tr("mail.secret_digest.foot_pusher", name)
		if adminsNotified {
			notified = locale.Tr("mail.secret_digest.admins_notified")
		}
	} else {
		subjectHTML = locale.TrPluralString(d.Alerts, "mail.secret_digest.subject", name)
		intro = locale.TrPluralString(d.Alerts, "mail.secret_digest.intro", where, d.Alerts)
		advice, foot = locale.Tr("mail.secret_digest.advice_admin"), locale.Tr("mail.secret_digest.foot_admin", name)
	}
	subject := html.UnescapeString(string(subjectHTML))
	link := repo.HTMLURL() + "/security/secrets"
	if d.FirstIndex > 0 {
		link += "/" + strconv.FormatInt(d.FirstIndex, 10) // one alert: straight to it
	}
	data := map[string]any{
		"locale":     locale,
		"Language":   locale.Language(),
		"Title":      subject,
		"Intro":      intro,
		"Types":      types,
		"Places":     places,
		"PlacesHead": locale.TrPluralString(len(places), "mail.secret_digest.places"),
		"Advice":     advice,
		"Notified":   notified,
		"Footer":     foot,
		"Settings":   setting.AppURL + "user/settings/notifications",
		"Link":       link,
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
