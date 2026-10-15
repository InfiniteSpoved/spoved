// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package method

import (
	asymkey_model "forgejo.org/models/asymkey"
	audit_model "forgejo.org/models/audit"
	user_model "forgejo.org/models/user"
	"forgejo.org/services/auth"
)

var _ auth.AuthenticationResult = &httpSignAuthenticationResult{}

type httpSignAuthenticationResult struct {
	*auth.BaseAuthenticationResult
	user *user_model.User
	key  *asymkey_model.PublicKey
}

func (r *httpSignAuthenticationResult) User() *user_model.User {
	return r.user
}

func (r *httpSignAuthenticationResult) Credential() audit_model.Credential {
	return audit_model.Credential{Type: audit_model.CredentialSSHKey, ID: r.key.ID, Name: r.key.Name, OwnerID: r.user.ID}
}
