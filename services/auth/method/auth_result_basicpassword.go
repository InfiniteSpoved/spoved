// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package method

import (
	audit_model "forgejo.org/models/audit"
	user_model "forgejo.org/models/user"
	"forgejo.org/services/auth"
)

var _ auth.AuthenticationResult = &basicPaswordAuthenticationResult{}

type basicPaswordAuthenticationResult struct {
	*auth.BaseAuthenticationResult
	user *user_model.User
}

func (*basicPaswordAuthenticationResult) IsPasswordAuthentication() bool {
	return true
}

func (r *basicPaswordAuthenticationResult) User() *user_model.User {
	return r.user
}

func (r *basicPaswordAuthenticationResult) Credential() audit_model.Credential {
	return audit_model.Credential{Type: audit_model.CredentialPassword, OwnerID: r.user.ID}
}
