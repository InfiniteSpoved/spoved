// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package auth

import (
	"context"
	"maps"

	audit_model "forgejo.org/models/audit"
	"forgejo.org/models/db"
)

// appFields are the audited fields of the app: its name and where it redirects to, never its secret.
func appFields(app *OAuth2Application) map[string]any {
	uris := make([]string, 0, len(app.RedirectURIs))
	for _, u := range app.RedirectURIs {
		if u = audit_model.URL(u); u != "" {
			uris = append(uris, u)
		}
	}
	return map[string]any{"name": app.Name, "redirect_uris": uris, "confidential": app.ConfidentialClient}
}

// recordOAuth2App records a change of the app. A user's app has the user as its subject; an organization's app and
// an instance app (uid 0) have none.
func recordOAuth2App(ctx context.Context, action string, app *OAuth2Application, extra map[string]any) error {
	var subject int64
	if app.UID != 0 {
		var typ int
		// models/user can't be imported here: 0 is an individual (user_model.UserTypeIndividual)
		if _, err := db.GetEngine(ctx).Table("user").Where("id = ?", app.UID).Cols("type").Get(&typ); err != nil {
			return err
		}
		if typ == 0 {
			subject = app.UID
		}
	}
	data := appFields(app)
	maps.Copy(data, extra)
	return audit_model.RecordCredential(ctx, audit_model.Event{
		Action: action, OwnerID: app.UID, SubjectUserID: subject,
		TargetType: "oauth2_app", TargetID: app.ID, TargetName: app.Name, Data: data,
	})
}

func recordGrant(ctx context.Context, action string, grant *OAuth2Grant) error {
	var appName string
	if _, err := db.GetEngine(ctx).Table("oauth2_application").Where("id = ?", grant.ApplicationID).Cols("name").Get(&appName); err != nil {
		return err
	}
	return audit_model.RecordCredential(ctx, audit_model.Event{
		Action: action, OwnerID: grant.UserID, SubjectUserID: grant.UserID,
		TargetType: "oauth2_grant", TargetID: grant.ID, TargetName: appName,
		Data: map[string]any{"app_id": grant.ApplicationID, "app_name": appName, "scopes": grant.Scope},
	})
}

// IntegrationFields are the audited fields of the integration and its repositories; never its claim rules.
func IntegrationFields(ai *AuthorizedIntegration, repoIDs []int64) map[string]any {
	return map[string]any{
		"name": ai.Name, "issuer": audit_model.URL(ai.Issuer), "scopes": string(ai.Scope),
		"resource_all_repos": ai.ResourceAllRepos, "repo_ids": repoIDs, "ui": string(ai.UI),
	}
}

// RecordAuthorizedIntegration records a change of the user's authorized integration.
func RecordAuthorizedIntegration(ctx context.Context, action string, ai *AuthorizedIntegration, data map[string]any) error {
	return audit_model.RecordCredential(ctx, audit_model.Event{
		Action: action, OwnerID: ai.UserID, SubjectUserID: ai.UserID,
		TargetType: "authorized_integration", TargetID: ai.ID, TargetName: ai.Name, Data: data,
	})
}
