// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package auth

import (
	"context"
	"strconv"

	audit_model "forgejo.org/models/audit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/log"
	"forgejo.org/modules/timeutil"
)

// RecordSignIn records a successful interactive sign-in of u with its method (e.g. "password", "ldap:3",
// "password+totp"), and makes u the actor of the rest of the request. reason says how a sign-in that isn't a plain
// credential check happened, e.g. "reset_code".
func RecordSignIn(ctx context.Context, u *user_model.User, method, reason string) {
	a := audit_model.ActorFromContext(ctx)
	if a == nil {
		a = &audit_model.Actor{}
		ctx = audit_model.WithActor(ctx, a)
	}
	a.Type, a.ID, a.Name = audit_model.ActorUser, u.ID, u.Name
	a.Credential = audit_model.Credential{Type: audit_model.CredentialSession, OwnerID: u.ID}
	data := map[string]any{"method": method}
	if reason != "" {
		data["reason"] = reason
	}
	audit_model.RecordActivity(ctx, audit_model.Event{
		Action: "auth.signin.success", Result: audit_model.ResultSuccess, OwnerID: u.ID, SubjectUserID: u.ID,
		TargetType: "signin", TargetID: u.ID, TargetName: u.Name, Data: data,
	})
}

func actorIPKey(ctx context.Context, bits int) string {
	if a := audit_model.ActorFromContext(ctx); a != nil {
		return audit_model.IPKey(a, bits)
	}
	return ""
}

// RecordSignInFailure records a failed sign-in, aggregated per client IP (/48 for IPv6), user and reason. subject is
// the user when it is known, else 0: a typed name that matches no user is never recorded.
func RecordSignInFailure(ctx context.Context, subject int64, reason string) {
	audit_model.RecordAggregated(ctx, audit_model.Event{
		Action: "auth.signin.failed", Result: audit_model.ResultFailure, OwnerID: subject, SubjectUserID: subject,
		TargetType: "signin", TargetID: subject, Data: map[string]any{"reason": reason},
	}, actorIPKey(ctx, 48)+"|"+strconv.FormatInt(subject, 10)+"|"+reason)
}

// RecordRefusal records a request refused by a policy (reason "ip_allowlist", "rate_limit", "expired", ...),
// aggregated per key. ownerID is the organization whose policy refused, if any.
func RecordRefusal(ctx context.Context, reason string, ownerID, subject int64, key string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["reason"] = reason
	audit_model.RecordAggregated(ctx, audit_model.Event{
		Action: "auth.refused", Result: audit_model.ResultRefused, OwnerID: ownerID, SubjectUserID: subject,
		TargetType: "refusal", TargetID: subject, Data: data,
	}, reason+"|"+key)
}

// RecordTwoFactorBlocked records once per user, organization and UTC day that the organization's two-factor
// requirement refused the user.
func RecordTwoFactorBlocked(ctx context.Context, uid, orgID int64) {
	key := "2fa|" + strconv.FormatInt(uid, 10) + "|" + strconv.FormatInt(orgID, 10)
	if audit_model.SeenToday(key) {
		return
	}
	if audit_model.RecordActivity(ctx, audit_model.Event{
		Action: "auth.refused", Result: audit_model.ResultRefused, OwnerID: orgID, SubjectUserID: uid,
		TargetType: "refusal", TargetID: uid, Data: map[string]any{"reason": "org_two_factor"},
	}) {
		audit_model.MarkSeenToday(key)
	}
}

var expiredActions = map[string]string{
	audit_model.CredentialAccessToken:      "credential.access_token.expired",
	audit_model.CredentialSSHKey:           "credential.ssh_key.expired",
	audit_model.CredentialDeployKey:        "credential.deploy_key.expired",
	audit_model.CredentialOrgDeployKey:     "credential.org_deploy_key.expired",
	audit_model.CredentialProjectDeployKey: "credential.project_deploy_key.expired",
	audit_model.CredentialOAuth2App:        "credential.oauth2_app.expired",
}

// RecordExpired records the first refusal of an expired credential once (credential.<type>.expired, also across
// pods and restarts: an existing entry counts), and every later one as an aggregated "expired" refusal.
func RecordExpired(ctx context.Context, credType string, id, ownerID int64, name string, expires timeutil.TimeStamp) {
	action := expiredActions[credType]
	key := credType + ":" + strconv.FormatInt(id, 10)
	if !audit_model.Seen("expired|" + key) {
		has, err := audit_model.HasEntry(ctx, action, credType, id)
		if err != nil {
			log.Error("audit: HasEntry: %v", err)
			return
		}
		field := "expires"
		if credType == audit_model.CredentialOAuth2App {
			field = "secret_expires"
		}
		if has || audit_model.RecordActivity(ctx, audit_model.Event{
			Action: action, Result: audit_model.ResultRefused, OwnerID: ownerID, SubjectUserID: ownerID,
			TargetType: credType, TargetID: id, TargetName: name, Data: map[string]any{field: int64(expires)},
		}) {
			audit_model.MarkSeen("expired|" + key)
		}
		if !has {
			return
		}
	}
	RecordRefusal(ctx, "expired", 0, ownerID, key, map[string]any{"class": credType})
}
