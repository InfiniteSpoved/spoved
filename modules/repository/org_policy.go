// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repository

import (
	"context"
	"errors"

	"forgejo.org/models/organization"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/structs"
	"forgejo.org/modules/util"
)

// OrgPolicyAction is an action on a repository that an organization's repository policies can refuse.
type OrgPolicyAction int

const (
	OrgPolicyVisibility OrgPolicyAction = iota
	OrgPolicyFork
)

// ErrOrgPolicy is returned when a policy of the organization that owns the repository refuses the action.
// Policy is the name of the setting: restrict_repo_visibility_change or private_fork_policy.
type ErrOrgPolicy struct {
	Policy string
}

func (e ErrOrgPolicy) Error() string {
	return "blocked by the organization policy " + e.Policy
}

func (ErrOrgPolicy) Unwrap() error {
	return util.ErrPermissionDenied
}

// CheckOrgPolicy returns an ErrOrgPolicy if a policy of the organization that owns repo refuses the action to
// doer. The visibility policy binds everyone but the organization's owners; a private fork policy binds the
// owners too, and forkInto is the owner of the new fork. Active site admins always pass, and repositories that an
// organization doesn't own are never restricted. It changes no permission: callers check it after their
// permission checks. Any other error means the check failed and the action must be refused.
//
// A nil doer is a system path and is never restricted: every request handler passes the signed-in user. Pseudo
// users (the Actions user -2, a deploy key pushing as the organization) are bound like any non-owner.
func CheckOrgPolicy(ctx context.Context, doer *user_model.User, repo *repo_model.Repository, action OrgPolicyAction, forkInto *user_model.User) error {
	if doer == nil || doer.IsAdmin && doer.IsActive && !doer.ProhibitLogin {
		return nil
	}
	owner := repo.Owner
	if owner == nil || owner.ID != repo.OwnerID {
		var err error
		if owner, err = user_model.GetUserByIDIncludingTrashed(ctx, repo.OwnerID); err != nil {
			return err
		}
	}
	if !owner.IsOrganization() {
		return nil
	}

	switch action {
	case OrgPolicyVisibility:
		if !owner.RestrictRepoVisibilityChange {
			return nil
		}
	case OrgPolicyFork:
		if !repo.IsPrivate && owner.Visibility != structs.VisibleTypePrivate || owner.PrivateForkPolicy == user_model.PrivateForkAll ||
			owner.PrivateForkPolicy == user_model.PrivateForkOrg && forkInto != nil && forkInto.ID == owner.ID {
			return nil
		}
		return ErrOrgPolicy{Policy: "private_fork_policy"}
	default:
		return errors.New("unknown organization policy action")
	}

	isOwner, err := organization.OrgFromUser(owner).IsOwnedBy(ctx, doer.ID)
	if err != nil {
		return err
	} else if !isOwner {
		return ErrOrgPolicy{Policy: "restrict_repo_visibility_change"}
	}
	return nil
}

// AllowedByOrgPolicy reports whether CheckOrgPolicy allows the action, for the UI.
func AllowedByOrgPolicy(ctx context.Context, doer *user_model.User, repo *repo_model.Repository, action OrgPolicyAction, forkInto *user_model.User) (bool, error) {
	err := CheckOrgPolicy(ctx, doer, repo, action, forkInto)
	if errors.As(err, &ErrOrgPolicy{}) {
		return false, nil
	}
	return err == nil, err
}
