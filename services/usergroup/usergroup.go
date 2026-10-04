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
//
// A user group can also grant instance-level roles (site administrator,
// creating organizations, auditor). They are synced into the user's IsAdmin,
// AllowCreateOrganization and IsAuditor flags, and a UserGroupRoleGrant
// records the flags a group turned on. A flag is only turned off again when
// such a grant exists and no remaining group of the user grants the role.
package usergroup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"forgejo.org/models"
	"forgejo.org/models/db"
	org_model "forgejo.org/models/organization"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/container"
	"forgejo.org/modules/log"
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
	return addMember(ctx, group, user, 0)
}

// addMember is AddMember recording the login source that synced the membership (0 for none).
func addMember(ctx context.Context, group *org_model.UserGroup, user *user_model.User, loginSourceID int64) error {
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
		if err := org_model.InsertUserGroupMember(ctx, group.ID, user.ID, loginSourceID); err != nil {
			return err
		}
		if err := syncRoles(ctx, user.ID); err != nil {
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
		if err := syncRoles(ctx, userID); err != nil {
			return err
		}
		return syncUserForGroupTeams(ctx, group.ID, userID)
	})
}

// SyncDirectoryGroups syncs the user group memberships of a user from the
// directory groups reported by a login source. mapping maps a directory group
// to the names of user groups. The user is added to every group mapped from
// one of their directory groups. With remove, the user is also removed from
// the other mapped groups, but only where this login source added them.
// Memberships added by an admin or another source are never touched.
func SyncDirectoryGroups(ctx context.Context, user *user_model.User, loginSourceID int64, userDirectoryGroups container.Set[string], mapping map[string]container.Set[string], remove bool) error {
	toAdd, toRemove := make(container.Set[string]), make(container.Set[string])
	for dirGroup, names := range mapping {
		target := toRemove
		if userDirectoryGroups.Contains(dirGroup) {
			target = toAdd
		}
		for name := range names {
			target.Add(strings.ToLower(name))
		}
	}

	for name := range toAdd {
		toRemove.Remove(name) // still mapped from another directory group of the user
	}

	addGroups, err := loadGroupsForSync(ctx, toAdd)
	if err != nil {
		return err
	}
	for _, group := range addGroups {
		if err := addMember(ctx, group, user, loginSourceID); err != nil && !errors.Is(err, ErrAlreadyMember) {
			return err
		}
	}

	if !remove {
		return nil
	}
	removeGroups, err := loadGroupsForSync(ctx, toRemove)
	if err != nil {
		return err
	}
	for _, group := range removeGroups {
		err := db.WithTx(ctx, func(ctx context.Context) error {
			deleted, err := org_model.DeleteSyncedUserGroupMember(ctx, group.ID, user.ID, loginSourceID)
			if err != nil || !deleted {
				return err
			}
			if err := syncRoles(ctx, user.ID); err != nil {
				return err
			}
			return syncUserForGroupTeams(ctx, group.ID, user.ID)
		})
		if org_model.IsErrLastOrgOwner(err) || models.IsErrDeleteLastAdminUser(err) {
			log.Warn("user group sync: keeping %s in user group %s: %v", user.Name, group.Name, err)
		} else if err != nil {
			return err
		}
	}
	return nil
}

// loadGroupsForSync returns the user groups with the given names, skipping the ones that do not exist.
func loadGroupsForSync(ctx context.Context, names container.Set[string]) ([]*org_model.UserGroup, error) {
	groups := make([]*org_model.UserGroup, 0, len(names))
	for name := range names {
		group, err := org_model.GetUserGroupByName(ctx, name)
		if org_model.IsErrUserGroupNotExist(err) {
			log.Warn("user group sync: user group %s does not exist", name)
			continue
		} else if err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, nil
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

// UpdateGroup updates the name, description and roles of a user group, and
// the role flags of its members.
func UpdateGroup(ctx context.Context, group *org_model.UserGroup) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		if err := org_model.UpdateUserGroup(ctx, group); err != nil {
			return err
		}
		memberIDs, err := org_model.GetUserGroupMemberIDs(ctx, group.ID)
		if err != nil {
			return err
		}
		return syncRoles(ctx, memberIDs...)
	})
}

// DeleteGroup unlinks a user group from all teams and deletes it.
func DeleteGroup(ctx context.Context, group *org_model.UserGroup) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		memberIDs, err := org_model.GetUserGroupMemberIDs(ctx, group.ID)
		if err != nil {
			return err
		}
		linked, err := org_model.GetUserGroupTeams(ctx, group.ID)
		if err != nil {
			return err
		}
		for _, l := range linked {
			if err := UnlinkTeam(ctx, group, l.Team); err != nil {
				return err
			}
		}
		if err := db.DeleteBeans(ctx,
			&org_model.UserGroupUser{GroupID: group.ID},
			&org_model.UserGroup{ID: group.ID},
		); err != nil {
			return err
		}
		return syncRoles(ctx, memberIDs...)
	})
}

// HasRoleViaGroup returns true if a user group of the user grants the role.
func HasRoleViaGroup(ctx context.Context, uid int64, role org_model.UserGroupRole) (bool, error) {
	groups, err := org_model.GetUserRoleGroups(ctx, uid, role)
	return len(groups) > 0, err
}

// DirectoryAdmin returns the admin flag a login source should set for a user
// it reports as admin or not. A user group granting site administrator wins
// over the directory: such a user is never demoted.
func DirectoryAdmin(ctx context.Context, user *user_model.User, directoryIsAdmin bool) (bool, error) {
	if directoryIsAdmin || !user.IsAdmin {
		return directoryIsAdmin, nil
	}
	return HasRoleViaGroup(ctx, user.ID, org_model.UserGroupRoleSiteAdmin)
}

// syncRoles makes the role flags of the users match the roles their user
// groups grant. A flag is turned on when a group grants the role, and turned
// off only when a group turned it on and none grants it anymore. It refuses
// to remove the last site administrator with models.ErrDeleteLastAdminUser.
func syncRoles(ctx context.Context, uids ...int64) error {
	for _, uid := range uids {
		user, err := user_model.GetUserByID(ctx, uid)
		if user_model.IsErrUserNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		for _, role := range org_model.UserGroupRoles {
			var flag *bool
			var col string
			switch role {
			case org_model.UserGroupRoleSiteAdmin:
				flag, col = &user.IsAdmin, "is_admin"
			case org_model.UserGroupRoleCreateOrganization:
				flag, col = &user.AllowCreateOrganization, "allow_create_organization"
			case org_model.UserGroupRoleAuditor:
				flag, col = &user.IsAuditor, "is_auditor"
			default:
				return fmt.Errorf("user group role %q has no user flag", role)
			}
			granted, err := HasRoleViaGroup(ctx, uid, role)
			if err != nil {
				return err
			}
			grant := &org_model.UserGroupRoleGrant{UID: uid, Role: role}
			hasGrant, err := db.GetEngine(ctx).Exist(grant)
			if err != nil {
				return err
			}

			switch {
			case granted && !*flag:
				*flag = true
				if err := user_model.UpdateUserCols(ctx, user, col); err != nil {
					return err
				}
				if !hasGrant {
					if err := db.Insert(ctx, grant); err != nil {
						return err
					}
				}
			case !granted && hasGrant:
				if *flag {
					if role == org_model.UserGroupRoleSiteAdmin && user_model.IsLastAdminUser(ctx, user) {
						return models.ErrDeleteLastAdminUser{UID: uid}
					}
					*flag = false
					if err := user_model.UpdateUserCols(ctx, user, col); err != nil {
						return err
					}
				}
				if _, err := db.DeleteByBean(ctx, grant); err != nil {
					return err
				}
			}
		}
	}
	return nil
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
