// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package user

import (
	"errors"
	"strings"

	"forgejo.org/modules/optional"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
	"forgejo.org/services/authz"
	"forgejo.org/services/context"
)

// Translate errors from [ValidateAccessToken] into user-visible messages, if the error is expected as a user-facing
// error.  None[string] may be returned to indicate the error is an unexpected server-side error, not a user validation
// error.
func TranslateAccessTokenValidationError(ctx *context.Base, err error) optional.Option[string] {
	if msg := TranslateScopePolicyError(ctx, err); msg.Has() {
		return msg
	}
	switch {
	case errors.Is(err, authz.ErrSpecifiedReposNone):
		return optional.Some[string](ctx.Locale.TrString("access_token.error.specified_repos_none"))
	case errors.Is(err, authz.ErrSpecifiedReposNoPublicOnly):
		return optional.Some[string](ctx.Locale.TrString("access_token.error.specified_repos_and_public_only"))
	case errors.Is(err, authz.ErrSpecifiedReposInvalidScope):
		return optional.Some[string](ctx.Locale.TrString("access_token.error.specified_repos_and_invalid_scope"))
	default:
		return TranslateExpiryError(ctx, err, setting.AccessTokenExpiry)
	}
}

// TranslateScopePolicyError translates errors from [authz.ValidateScopePolicy], or returns None.
func TranslateScopePolicyError(ctx *context.Base, err error) optional.Option[string] {
	var policyErr *authz.ScopePolicyError
	if !errors.As(err, &policyErr) {
		return optional.None[string]()
	}
	if policyErr.Err == authz.ErrScopeNotAllowed {
		return optional.Some(ctx.Locale.TrString("access_token.error.scope_not_allowed", policyErr.Scope, strings.Join(policyErr.Allowed, ", ")))
	}
	return optional.Some(ctx.Locale.TrString("access_token.error.scope_needs_repo_restriction", policyErr.Scope))
}

// TranslateExpiryError translates errors from [authz.ValidateExpiry] and [authz.ParseExpiryDate], or returns None.
func TranslateExpiryError(ctx *context.Base, err error, p setting.CredentialExpiryPolicy) optional.Option[string] {
	switch {
	case errors.Is(err, authz.ErrExpiryInvalid):
		return optional.Some(ctx.Locale.TrString("credential.error.expiry_invalid"))
	case errors.Is(err, authz.ErrExpiryRequired):
		return optional.Some(ctx.Locale.TrString("credential.error.expiry_required"))
	case errors.Is(err, authz.ErrExpiryInPast):
		return optional.Some(ctx.Locale.TrString("credential.error.expiry_in_past"))
	case errors.Is(err, authz.ErrExpiryTooLate):
		return optional.Some(ctx.Locale.TrString("credential.error.expiry_too_late", p.MaxLifetimeDays))
	default:
		return optional.None[string]()
	}
}

// ParseExpiryForm parses the expires_at form value and checks it against the policy. On failure it returns the
// translated message.
func ParseExpiryForm(ctx *context.Base, p setting.CredentialExpiryPolicy, value string) (timeutil.TimeStamp, optional.Option[string]) {
	expires, err := authz.ParseExpiryDate(value)
	if err == nil {
		err = authz.ValidateExpiry(p, expires)
	}
	if err != nil {
		return 0, TranslateExpiryError(ctx, err, p)
	}
	return expires, optional.None[string]()
}

// SetExpiryFormData sets the data used by the shared expiry input template.
func SetExpiryFormData(ctx *context.Base, p setting.CredentialExpiryPolicy) {
	ctx.Data["ExpiryRequired"] = p.Required || p.MaxLifetimeDays > 0
	ctx.Data["ExpiryMaxDate"] = authz.MaxExpiryDate(p)
}
