// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package method

import (
	audit_model "forgejo.org/models/audit"
	auth_model "forgejo.org/models/auth"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/optional"
	"forgejo.org/modules/timeutil"
	"forgejo.org/services/auth"
	"forgejo.org/services/authz"
)

var _ auth.AuthenticationResult = &authorizedIntegrationAuthenticationResult{}

type authorizedIntegrationAuthenticationResult struct {
	*auth.BaseAuthenticationResult
	id        int64
	name      string
	user      *user_model.User
	scope     auth_model.AccessTokenScope
	reducer   authz.AuthorizationReducer
	expiresAt optional.Option[timeutil.TimeStamp]
}

func (*authorizedIntegrationAuthenticationResult) IsAuthorizedIntegration() bool {
	return true
}

func (r *authorizedIntegrationAuthenticationResult) User() *user_model.User {
	return r.user
}

func (r *authorizedIntegrationAuthenticationResult) Scope() optional.Option[auth_model.AccessTokenScope] {
	return optional.Some(r.scope)
}

func (r *authorizedIntegrationAuthenticationResult) Reducer() authz.AuthorizationReducer {
	return r.reducer
}

func (r *authorizedIntegrationAuthenticationResult) ExpiresAt() optional.Option[timeutil.TimeStamp] {
	return r.expiresAt
}

func (r *authorizedIntegrationAuthenticationResult) Credential() audit_model.Credential {
	return audit_model.Credential{Type: audit_model.CredentialAuthorizedIntegration, ID: r.id, Name: r.name, OwnerID: r.user.ID}
}
