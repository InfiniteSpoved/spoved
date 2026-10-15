// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package audit

// Actions maps every action key to its category. Record refuses a key that is not listed; TestActionCatalog checks
// that each one is recorded somewhere, tested and has a phrase.
var Actions = map[string]string{
	"audit.exported":         CategoryAudit,
	"audit.instance.started": CategoryAudit,

	"auth.credential.used":          CategoryAuth,
	"auth.refused":                  CategoryAuth,
	"auth.refused.aggregated":       CategoryAuth,
	"auth.signin.failed":            CategoryAuth,
	"auth.signin.failed.aggregated": CategoryAuth,
	"auth.signin.success":           CategoryAuth,

	"credential.access_token.created":       CategoryCredential,
	"credential.access_token.deleted":       CategoryCredential,
	"credential.access_token.expired":       CategoryCredential,
	"credential.access_token.regenerated":   CategoryCredential,
	"credential.deploy_key.expired":         CategoryCredential,
	"credential.oauth2_app.expired":         CategoryCredential,
	"credential.org_deploy_key.expired":     CategoryCredential,
	"credential.project_deploy_key.expired": CategoryCredential,
	"credential.ssh_key.expired":            CategoryCredential,
}
