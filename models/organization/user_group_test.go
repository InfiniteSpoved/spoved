// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package organization_test

import (
	"testing"

	"forgejo.org/models/db"
	"forgejo.org/models/organization"
	"forgejo.org/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserGroupCRUD(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := db.DefaultContext

	g := &organization.UserGroup{Name: " Platform-Devs ", Description: "Platform developers"}
	require.NoError(t, organization.InsertUserGroup(ctx, g))
	assert.Equal(t, "Platform-Devs", g.Name)
	assert.Equal(t, "platform-devs", g.LowerName)

	err := organization.InsertUserGroup(ctx, &organization.UserGroup{Name: "PLATFORM-devs"})
	require.True(t, organization.IsErrUserGroupAlreadyExist(err), "unexpected error: %v", err)

	got, err := organization.GetUserGroupByName(ctx, "platform-DEVS")
	require.NoError(t, err)
	assert.Equal(t, g.ID, got.ID)
	assert.Equal(t, "Platform developers", got.Description)

	_, err = organization.GetUserGroupByName(ctx, "missing")
	require.True(t, organization.IsErrUserGroupNotExist(err))
	_, err = organization.GetUserGroupByID(ctx, 9999)
	require.True(t, organization.IsErrUserGroupNotExist(err))

	other := &organization.UserGroup{Name: "qa"}
	require.NoError(t, organization.InsertUserGroup(ctx, other))
	other.Name = "Platform-devs"
	err = organization.UpdateUserGroup(ctx, other)
	require.True(t, organization.IsErrUserGroupAlreadyExist(err), "unexpected error: %v", err)

	other.Name = "quality"
	other.Description = "QA engineers"
	require.NoError(t, organization.UpdateUserGroup(ctx, other))
	got, err = organization.GetUserGroupByID(ctx, other.ID)
	require.NoError(t, err)
	assert.Equal(t, "quality", got.LowerName)
	assert.Equal(t, "QA engineers", got.Description)

	groups, count, err := organization.SearchUserGroups(ctx, &organization.SearchUserGroupOptions{Keyword: "PLAT"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)
	require.Len(t, groups, 1)
	assert.Equal(t, g.ID, groups[0].ID)

	groups, count, err = organization.SearchUserGroups(ctx, &organization.SearchUserGroupOptions{})
	require.NoError(t, err)
	assert.EqualValues(t, 2, count)
	assert.Equal(t, []string{"platform-devs", "quality"}, []string{groups[0].LowerName, groups[1].LowerName})
}

func TestUserGroupMembersAndTeams(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := db.DefaultContext

	g := &organization.UserGroup{Name: "devs"}
	require.NoError(t, organization.InsertUserGroup(ctx, g))
	require.NoError(t, organization.InsertUserGroupMember(ctx, g.ID, 5))
	require.NoError(t, organization.InsertUserGroupMember(ctx, g.ID, 4))

	members, err := organization.GetUserGroupMembers(ctx, g.ID)
	require.NoError(t, err)
	require.Len(t, members, 2)
	assert.Equal(t, "user4", members[0].Name)
	assert.Equal(t, "user5", members[1].Name)

	isMember, err := organization.IsUserGroupMember(ctx, g.ID, 5)
	require.NoError(t, err)
	assert.True(t, isMember)

	team := unittest.AssertExistsAndLoadBean(t, &organization.Team{ID: 2})
	viaGroup, err := organization.IsTeamMemberViaUserGroup(ctx, team.ID, 5)
	require.NoError(t, err)
	assert.False(t, viaGroup)

	require.NoError(t, organization.InsertUserGroupTeam(ctx, g.ID, team))
	unittest.AssertExistsAndLoadBean(t, &organization.UserGroupTeam{GroupID: g.ID, TeamID: team.ID, OrgID: team.OrgID})

	viaGroup, err = organization.IsTeamMemberViaUserGroup(ctx, team.ID, 5)
	require.NoError(t, err)
	assert.True(t, viaGroup)
	viaGroup, err = organization.IsTeamMemberViaUserGroup(ctx, team.ID, 8)
	require.NoError(t, err)
	assert.False(t, viaGroup)

	groups, err := organization.GetTeamUserGroups(ctx, team.ID)
	require.NoError(t, err)
	require.Len(t, groups, 1)
	assert.Equal(t, g.ID, groups[0].ID)

	require.NoError(t, g.LoadCounts(ctx))
	assert.Equal(t, 2, g.NumMembers)
	assert.Equal(t, 1, g.NumTeams)

	require.NoError(t, organization.DeleteUserGroupMember(ctx, g.ID, 5))
	require.NoError(t, organization.DeleteUserGroupTeam(ctx, g.ID, team.ID))
	require.NoError(t, g.LoadCounts(ctx))
	assert.Equal(t, 1, g.NumMembers)
	assert.Equal(t, 0, g.NumTeams)
}
