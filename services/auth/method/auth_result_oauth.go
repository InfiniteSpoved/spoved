// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package method

import (
	audit_model "forgejo.org/models/audit"
	auth_model "forgejo.org/models/auth"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/optional"
	"forgejo.org/services/auth"
)

var _ auth.AuthenticationResult = &oAuth2JWTAuthenticationResult{}

type oAuth2JWTAuthenticationResult struct {
	*auth.BaseAuthenticationResult
	user        *user_model.User
	scope       optional.Option[auth_model.AccessTokenScope]
	grantScopes string
	grantID     int64
	appID       int64
}

func (*oAuth2JWTAuthenticationResult) IsOAuth2JWTAuthentication() bool {
	return true
}

func (r *oAuth2JWTAuthenticationResult) User() *user_model.User {
	return r.user
}

func (r *oAuth2JWTAuthenticationResult) Scope() optional.Option[auth_model.AccessTokenScope] {
	return r.scope
}

func (r *oAuth2JWTAuthenticationResult) OAuth2GrantScopes() optional.Option[string] {
	return optional.Some(r.grantScopes)
}

// Credential is the grant; Record names it after its application.
func (r *oAuth2JWTAuthenticationResult) Credential() audit_model.Credential {
	return audit_model.Credential{Type: audit_model.CredentialOAuth2Grant, ID: r.grantID, AppID: r.appID, OwnerID: r.user.ID}
}
