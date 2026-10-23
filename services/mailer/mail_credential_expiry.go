// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package mailer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"time"

	asymkey_model "forgejo.org/models/asymkey"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	"forgejo.org/models/organization"
	"forgejo.org/models/perm"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/base"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/translation"
)

const tplCredentialExpiry base.TplName = "credential/expiry"

// expiryNoticeWindow is how long before its expiry a credential gets its first mail.
const expiryNoticeWindow = 7 * 24 * time.Hour

// expiringCredential is a key or token about to expire, with the owner its mail names.
type expiringCredential struct {
	kind    string // access_token, ssh_key, deploy_key, org_deploy_key
	id      int64
	name    string
	expires timeutil.TimeStamp
	userID  int64 // a personal token or SSH key
	repoID  int64 // a repository token or deploy key
	orgID   int64 // an organization or project token or deploy key
	groupID int64 // the project
}

// expiryRecipients is who gets a credential's mail, and what the mail says about its owner.
type expiryRecipients struct {
	users    []*user_model.User
	repo     *repo_model.Repository // the repository whose notification overrides apply, if any
	owner    func(translation.Locale) string
	reason   func(translation.Locale) string
	keysLink string
}

// SendCredentialExpiryNotices mails the people who manage each key and token that expires within 7 days: once in
// that week and once in its last day. Recipients are the user for a personal token or SSH key, the repository's
// administrators for a repository token or deploy key, and the organization's acting owners for an organization or
// project one; each recipient's notification settings apply. The mail never holds a secret.
func SendCredentialExpiryNotices(ctx context.Context) error {
	if setting.MailService == nil {
		return nil
	}
	now := timeutil.TimeStampNow()
	if err := auth_model.DeleteOldCredentialExpiryNotices(ctx, now.AddDuration(-30*24*time.Hour)); err != nil {
		return err
	}
	creds, err := findExpiringCredentials(ctx, now, now.AddDuration(expiryNoticeWindow))
	if err != nil {
		return err
	}
	for _, c := range creds {
		if err := ctx.Err(); err != nil {
			return err
		}
		stage := auth_model.ExpiryNoticeSoon
		if c.expires <= now.AddDuration(24*time.Hour) {
			stage = auth_model.ExpiryNoticeToday
		}
		r, err := c.recipients(ctx)
		if err != nil {
			log.Error("credential expiry notice: recipients of %s %d: %v", c.kind, c.id, err)
			continue
		}
		if r == nil || len(r.users) == 0 {
			continue
		}
		if claimed, err := auth_model.ClaimCredentialExpiryNotice(ctx, c.kind, c.id, c.expires, stage); err != nil {
			return err
		} else if !claimed {
			continue
		}
		for _, u := range r.users {
			if err := sendCredentialExpiryMail(ctx, c, r, u, stage, now); err != nil {
				log.Error("credential expiry notice to %d: %v", u.ID, err)
			}
		}
	}
	return nil
}

func findExpiringCredentials(ctx context.Context, from, to timeutil.TimeStamp) ([]*expiringCredential, error) {
	var creds []*expiringCredential
	window := "expires_unix > ? AND expires_unix <= ?"

	var tokens []*auth_model.AccessToken
	if err := db.GetEngine(ctx).Where(window+" AND revoked_unix = 0", from, to).Find(&tokens); err != nil {
		return nil, err
	}
	for _, t := range tokens {
		c := &expiringCredential{kind: "access_token", id: t.ID, name: t.Name, expires: t.ExpiresUnix}
		switch t.OwnerType {
		case auth_model.AccessTokenOwnerUser:
			c.userID = t.UID
		case auth_model.AccessTokenOwnerRepo:
			c.repoID = t.OwnerID
		default:
			c.orgID, c.groupID = t.OwnerID, t.GroupID
		}
		creds = append(creds, c)
	}

	var keys []*asymkey_model.PublicKey
	if err := db.GetEngine(ctx).Where(window+" AND type <> ?", from, to, asymkey_model.KeyTypeDeploy).Find(&keys); err != nil {
		return nil, err
	}
	for _, k := range keys {
		creds = append(creds, &expiringCredential{kind: "ssh_key", id: k.ID, name: k.Name, expires: k.ExpiresUnix, userID: k.OwnerID})
	}

	var deployKeys []*asymkey_model.DeployKey
	if err := db.GetEngine(ctx).Where(window, from, to).Find(&deployKeys); err != nil {
		return nil, err
	}
	for _, k := range deployKeys {
		creds = append(creds, &expiringCredential{kind: "deploy_key", id: k.ID, name: k.Name, expires: k.ExpiresUnix, repoID: k.RepoID})
	}

	var orgKeys []*asymkey_model.OrgDeployKey
	if err := db.GetEngine(ctx).Where(window, from, to).Find(&orgKeys); err != nil {
		return nil, err
	}
	for _, k := range orgKeys {
		creds = append(creds, &expiringCredential{kind: "org_deploy_key", id: k.ID, name: k.Name, expires: k.ExpiresUnix, orgID: k.OrgID, groupID: k.GroupID})
	}
	return creds, nil
}

// mailable reports whether u can get the mail: an active individual who may sign in and has an address.
func mailable(u *user_model.User) bool {
	return u.IsIndividual() && u.IsMailable() && !u.ProhibitLogin && u.Email != ""
}

// recipients returns who gets the credential's mail; nil when its owner is gone (deleted, in the trash).
func (c *expiringCredential) recipients(ctx context.Context) (*expiryRecipients, error) {
	switch {
	case c.userID != 0:
		u, err := user_model.GetUserByID(ctx, c.userID)
		if user_model.IsErrUserNotExist(err) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		r := &expiryRecipients{
			owner:    func(l translation.Locale) string { return l.TrString("mail.credential_expiry.of_account") },
			reason:   func(l translation.Locale) string { return l.TrString("mail.credential_expiry.reason_self") },
			keysLink: setting.AppURL + "user/settings/keys",
		}
		if mailable(u) {
			r.users = []*user_model.User{u}
		}
		return r, nil
	case c.repoID != 0:
		return repoAdminRecipients(ctx, c.repoID)
	default:
		return orgOwnerRecipients(ctx, c.orgID, c.groupID)
	}
}

// repoAdminRecipients are the administrators of the repository: its individual owner and whoever the access table
// gives admin or more, each checked against their permission as it is now.
func repoAdminRecipients(ctx context.Context, repoID int64) (*expiryRecipients, error) {
	repo, err := repo_model.GetRepositoryByID(ctx, repoID)
	if repo_model.IsErrRepoNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if err := repo.LoadOwner(ctx); err != nil {
		return nil, err
	}
	var ids []int64
	if err := db.GetEngine(ctx).Table("access").Where("repo_id = ? AND mode >= ?", repo.ID, perm.AccessModeAdmin).Cols("user_id").Find(&ids); err != nil {
		return nil, err
	}
	if !repo.Owner.IsOrganization() {
		ids = append(ids, repo.OwnerID)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	candidates, err := user_model.GetUserByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	fullName := repo.FullName()
	r := &expiryRecipients{
		repo:     repo,
		owner:    func(l translation.Locale) string { return l.TrString("mail.credential_expiry.of_repo", fullName) },
		reason:   func(l translation.Locale) string { return l.TrString("mail.credential_expiry.reason_admin", fullName) },
		keysLink: repo.HTMLURL() + "/settings/keys",
	}
	for _, u := range candidates {
		if !mailable(u) {
			continue
		}
		p, err := access_model.GetUserRepoPermission(ctx, repo, u)
		if err != nil {
			return nil, err
		}
		if p.IsAdmin() {
			r.users = append(r.users, u)
		}
	}
	return r, nil
}

// orgOwnerRecipients are the acting owners of the organization, for its own or its project's credential.
func orgOwnerRecipients(ctx context.Context, orgID, groupID int64) (*expiryRecipients, error) {
	org, err := organization.GetOrgByID(ctx, orgID)
	if organization.IsErrOrgNotExist(err) || user_model.IsErrUserNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	r := &expiryRecipients{
		owner:    func(l translation.Locale) string { return l.TrString("mail.credential_expiry.of_org", org.Name) },
		reason:   func(l translation.Locale) string { return l.TrString("mail.credential_expiry.reason_owner", org.Name) },
		keysLink: setting.AppURL + "org/" + url.PathEscape(org.Name) + "/settings/keys",
	}
	if groupID != 0 {
		group, err := repo_model.GetRepoGroupByID(ctx, orgID, groupID)
		if errors.Is(err, repo_model.ErrRepoGroupNotExist) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		path := org.Name + "/" + group.Name
		r.owner = func(l translation.Locale) string { return l.TrString("mail.credential_expiry.of_project", path) }
		r.keysLink = setting.AppURL + url.PathEscape(org.Name) + "/" + url.PathEscape(group.Name) + "/-/settings/keys"
	}
	team, err := org.GetOwnerTeam(ctx)
	if err != nil {
		return nil, err
	}
	members, err := organization.GetTeamMembers(ctx, &organization.SearchMembersOptions{TeamID: team.ID})
	if err != nil {
		return nil, err
	}
	for _, u := range members {
		if !mailable(u) {
			continue
		}
		if acting, err := org.IsActingOwner(ctx, u); err != nil {
			return nil, err
		} else if acting {
			r.users = append(r.users, u)
		}
	}
	return r, nil
}

func sendCredentialExpiryMail(ctx context.Context, c *expiringCredential, r *expiryRecipients, u *user_model.User, stage int, now timeutil.TimeStamp) error {
	repo := r.repo
	if repo == nil {
		repo = &repo_model.Repository{} // the user's own settings only
	}
	if !ShouldEmail(ctx, u, repo, user_model.NotifyEventCredentialExpiry) {
		return nil
	}
	locale := translation.NewLocale(u.Language)
	kind := locale.TrString("mail.credential_expiry.kind." + c.kind)
	if c.kind == "org_deploy_key" {
		kind = locale.TrString("mail.credential_expiry.kind.deploy_key")
	}
	var subject string
	if stage == auth_model.ExpiryNoticeToday {
		subject = locale.TrString("mail.credential_expiry.subject_today", kind, c.name)
	} else {
		days := int((time.Duration(c.expires-now)*time.Second + 24*time.Hour - 1) / (24 * time.Hour))
		subject = locale.TrString("mail.credential_expiry.subject_soon", kind, c.name, days)
	}
	data := map[string]any{
		"locale":       locale,
		"Language":     locale.Language(),
		"Kind":         kind,
		"Name":         c.name,
		"Owner":        r.owner(locale),
		"Expires":      c.expires.AsTimeInLocation(setting.DefaultUILocation).Format("2006-01-02 15:04 MST"),
		"IsToken":      c.kind == "access_token",
		"Link":         r.keysLink,
		"Reason":       r.reason(locale),
		"SettingsLink": setting.AppURL + "user/settings/notifications",
	}
	var content bytes.Buffer
	if err := bodyTemplates.ExecuteTemplate(&content, string(tplCredentialExpiry), data); err != nil {
		return fmt.Errorf("ExecuteTemplate: %w", err)
	}
	msg := NewMessage(u.EmailTo(), subject, content.String())
	msg.Info = fmt.Sprintf("Credential expiry notice: %s %d", c.kind, c.id)
	SendAsync(msg)
	return nil
}
