// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// Package usergroup manages instance-wide user groups and keeps the
// memberships of the organization teams they are linked to in sync.
//
// A user group carries no permissions of its own. An organization owner
// links a group to one of the organization's teams, and every member of the
// group becomes a member of that team, with the reason
// MembershipReasonAddedByUserGroup. A team membership created that way is
// removed again once the user is no longer in any group linked to the team.
// Memberships created any other way (directly, by invitation, by an
// authentication source) are never touched.
package usergroup

import (
	"context"
	"errors"

	"forgejo.org/models"
	"forgejo.org/models/db"
	org_model "forgejo.org/models/organization"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/optional"
)

var (
	// ErrOrganizationCannotBeMember is returned when adding an organization to a user group.
	ErrOrganizationCannotBeMember = errors.New("an organization cannot be a member of a user group")
	// ErrAlreadyMember is returned when adding a user who is already a member of the group.
	ErrAlreadyMember = errors.New("user is already a member of the user group")
	// ErrAlreadyLinked is returned when linking a group to a team it is already linked to.
	ErrAlreadyLinked = errors.New("user group is already linked to the team")
)

// AddMember adds a user to a user group and to every team the group is linked to.
func AddMember(ctx context.Context, group *org_model.UserGroup, user *user_model.User) error {
	if user.IsOrganization() {
		return ErrOrganizationCannotBeMember
	}
	return db.WithTx(ctx, func(ctx context.Context) error {
		isMember, err := org_model.IsUserGroupMember(ctx, group.ID, user.ID)
		if err != nil {
			return err
		} else if isMember {
			return ErrAlreadyMember
		}
		if err := org_model.InsertUserGroupMember(ctx, group.ID, user.ID); err != nil {
			return err
		}
		return syncUserForGroupTeams(ctx, group.ID, user.ID)
	})
}

// RemoveMember removes a user from a user group, and from every team the user
// was only a member of because of the group.
func RemoveMember(ctx context.Context, group *org_model.UserGroup, userID int64) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		if err := org_model.DeleteUserGroupMember(ctx, group.ID, userID); err != nil {
			return err
		}
		return syncUserForGroupTeams(ctx, group.ID, userID)
	})
}

// LinkTeam links a user group to a team and adds the members of the group to the team.
func LinkTeam(ctx context.Context, group *org_model.UserGroup, team *org_model.Team) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		linked, err := org_model.IsUserGroupLinkedToTeam(ctx, group.ID, team.ID)
		if err != nil {
			return err
		} else if linked {
			return ErrAlreadyLinked
		}
		if err := org_model.InsertUserGroupTeam(ctx, group.ID, team); err != nil {
			return err
		}
		return syncGroupMembersForTeam(ctx, group.ID, team)
	})
}

// UnlinkTeam unlinks a user group from a team, and removes the members of the
// group that were only members of the team because of the group.
func UnlinkTeam(ctx context.Context, group *org_model.UserGroup, team *org_model.Team) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		if err := org_model.DeleteUserGroupTeam(ctx, group.ID, team.ID); err != nil {
			return err
		}
		return syncGroupMembersForTeam(ctx, group.ID, team)
	})
}

// DeleteGroup unlinks a user group from all teams and deletes it.
func DeleteGroup(ctx context.Context, group *org_model.UserGroup) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		linked, err := org_model.GetUserGroupTeams(ctx, group.ID)
		if err != nil {
			return err
		}
		for _, l := range linked {
			if err := UnlinkTeam(ctx, group, l.Team); err != nil {
				return err
			}
		}
		return db.DeleteBeans(ctx,
			&org_model.UserGroupUser{GroupID: group.ID},
			&org_model.UserGroup{ID: group.ID},
		)
	})
}

// syncUserForGroupTeams updates the membership of a user in all teams the group is linked to.
func syncUserForGroupTeams(ctx context.Context, groupID, userID int64) error {
	linked, err := org_model.GetUserGroupTeams(ctx, groupID)
	if err != nil {
		return err
	}
	for _, l := range linked {
		if err := syncTeamMembership(ctx, l.Team, userID); err != nil {
			return err
		}
	}
	return nil
}

// syncGroupMembersForTeam updates the membership of all group members in a team.
func syncGroupMembersForTeam(ctx context.Context, groupID int64, team *org_model.Team) error {
	memberIDs, err := org_model.GetUserGroupMemberIDs(ctx, groupID)
	if err != nil {
		return err
	}
	for _, uid := range memberIDs {
		if err := syncTeamMembership(ctx, team, uid); err != nil {
			return err
		}
	}
	return nil
}

// syncTeamMembership makes the membership of a user in a team match the user
// groups linked to the team. Memberships not created by a user group are left alone.
func syncTeamMembership(ctx context.Context, team *org_model.Team, userID int64) error {
	viaGroup, err := org_model.IsTeamMemberViaUserGroup(ctx, team.ID, userID)
	if err != nil {
		return err
	}
	if viaGroup {
		_, err := models.InsertTeamMember(ctx, team, userID, org_model.MembershipReasonAddedByUserGroup, optional.None[int64](), optional.None[int64]())
		return err
	}

	membership, err := org_model.GetTeamUser(ctx, team.ID, userID)
	if err != nil {
		return err
	}
	if membership == nil || !membership.IsMembershipAddedByUserGroup() {
		return nil
	}
	return models.RemoveTeamMember(ctx, team, userID)
}
