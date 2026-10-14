// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repository

import (
	"context"

	user_model "forgejo.org/models/user"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/util"
)

// ErrPersonalRepoPolicy is returned when the site policy for personal repositories refuses a repository in a
// user's namespace. Mode is private_only or disabled.
type ErrPersonalRepoPolicy struct {
	Mode string
}

func (e ErrPersonalRepoPolicy) Error() string {
	if e.Mode == setting.PersonalReposPrivateOnly {
		return "personal repositories must be private on this site"
	}
	return "personal repositories are disabled on this site"
}

// TrKey is the locale key of the message for the user.
func (e ErrPersonalRepoPolicy) TrKey() string {
	return "repo.personal_repos." + e.Mode
}

func (ErrPersonalRepoPolicy) Unwrap() error {
	return util.ErrPermissionDenied
}

func usableAdmin(u *user_model.User) bool {
	return u != nil && u.IsAdmin && u.IsActive && !u.ProhibitLogin && u.Type == user_model.UserTypeIndividual
}

// CheckPersonalRepoPolicy returns an ErrPersonalRepoPolicy if the site policy for personal repositories refuses
// a repository with the given visibility in owner's namespace: a new one, one moved there, or one made public
// (private false). Organizations are never restricted, and a usable site admin as doer or owner is exempt; a nil
// doer is not. Refusals are logged.
func CheckPersonalRepoPolicy(ctx context.Context, doer, owner *user_model.User, private bool) error {
	if owner.IsOrganization() || usableAdmin(doer) || usableAdmin(owner) {
		return nil
	}
	mode := setting.Config().PersonalRepos.Value(ctx).EffectiveMode()
	if mode == setting.PersonalReposAllowed || mode == setting.PersonalReposPrivateOnly && private {
		return nil
	}
	doerName := "-"
	if doer != nil {
		doerName = doer.Name
	}
	log.Warn("Personal repository policy %s refused a repository of %s (doer %s, private %t)", mode, owner.Name, doerName, private)
	return ErrPersonalRepoPolicy{Mode: mode}
}
