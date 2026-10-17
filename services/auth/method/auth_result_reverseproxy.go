// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package method

import (
	audit_model "forgejo.org/models/audit"
	user_model "forgejo.org/models/user"
	"forgejo.org/services/auth"
)

var _ auth.AuthenticationResult = &reverseProxyAuthenticationResult{}

type reverseProxyAuthenticationResult struct {
	*auth.BaseAuthenticationResult
	user       *user_model.User
	newSession bool
}

// NewSession reports whether the request signed the user in: a session was created for them.
func (r *reverseProxyAuthenticationResult) NewSession() bool {
	return r.newSession
}

func (r *reverseProxyAuthenticationResult) User() *user_model.User {
	return r.user
}

func (*reverseProxyAuthenticationResult) IsReverseProxyAuthentication() bool {
	return true
}

func (r *reverseProxyAuthenticationResult) Credential() audit_model.Credential {
	return audit_model.Credential{Type: audit_model.CredentialReverseProxy, OwnerID: r.user.ID}
}
