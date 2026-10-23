// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package auth

import (
	"context"
	"fmt"

	auth_model "forgejo.org/models/auth"
	user_model "forgejo.org/models/user"
)

// AccessTokenUser returns who a valid access token acts as: the user of a personal access token, or the identity of
// a token of an organization, a project or a repository. The latter is refused (a CredentialOwnerError, as the token
// matched) when its scopes or permission are not allowed for such a token, whatever was stored.
func AccessTokenUser(ctx context.Context, token *auth_model.AccessToken) (*user_model.User, error) {
	if token.IsOwnedByUser() {
		return user_model.GetUserByID(ctx, token.UID)
	}
	if err := auth_model.ValidateOwnedTokenScope(token.Scope); err != nil {
		return nil, &CredentialOwnerError{Err: fmt.Errorf("access token %q: %w", token.Name, err)}
	}
	if err := auth_model.ValidateOwnedTokenMode(token.Mode); err != nil {
		return nil, &CredentialOwnerError{Err: fmt.Errorf("access token %q: %w", token.Name, err)}
	}
	return user_model.NewAccessTokenBot(token), nil
}
