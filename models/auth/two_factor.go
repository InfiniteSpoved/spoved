// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later
package auth

import (
	"context"

	"forgejo.org/models/db"
	"forgejo.org/modules/container"
)

// HasTwoFactorByUID returns true if the user has TOTP or WebAuthn enabled for
// their account.
func HasTwoFactorByUID(ctx context.Context, userID int64) (bool, error) {
	hasTOTP, err := HasTOTPByUID(ctx, userID)
	if err != nil {
		return false, err
	}
	if hasTOTP {
		return true, nil
	}

	return HasWebAuthnRegistrationsByUID(ctx, userID)
}

// UserIDsWithTwoFactor returns the users of ids that have TOTP or WebAuthn enabled.
func UserIDsWithTwoFactor(ctx context.Context, ids []int64) (container.Set[int64], error) {
	set := container.Set[int64]{}
	if len(ids) == 0 {
		return set, nil
	}
	var totp, webAuthn []int64
	if err := db.GetEngine(ctx).Table("two_factor").In("uid", ids).Cols("uid").Find(&totp); err != nil {
		return nil, err
	}
	if err := db.GetEngine(ctx).Table("webauthn_credential").In("user_id", ids).Distinct("user_id").Find(&webAuthn); err != nil {
		return nil, err
	}
	set.AddMultiple(totp...)
	set.AddMultiple(webAuthn...)
	return set, nil
}
