// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"fmt"
	"net/http"
	"testing"

	"forgejo.org/models/organization"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/test"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserGroupSharedAcrossOrganizations(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	admin := loginUser(t, "user1")
	member := loginUser(t, "user5")

	// org3/repo3 is private and only reachable through org3's teams
	member.MakeRequest(t, NewRequest(t, "GET", "/org3/repo3"), http.StatusNotFound)

	// a site administrator creates the group
	resp := admin.MakeRequest(t, NewRequestWithValues(t, "POST", "/admin/groups/new", map[string]string{
		"name":        "release-managers",
		"description": "People who cut releases",
	}), http.StatusSeeOther)
	group := unittest.AssertExistsAndLoadBean(t, &organization.UserGroup{LowerName: "release-managers"})
	groupURL := fmt.Sprintf("/admin/groups/%d", group.ID)
	assert.Equal(t, groupURL, test.RedirectURL(resp))

	// group names are unique
	resp = admin.MakeRequest(t, NewRequestWithValues(t, "POST", "/admin/groups/new", map[string]string{
		"name": "Release-Managers",
	}), http.StatusOK)
	assert.Contains(t, resp.Body.String(), "A user group with this name already exists.")

	// add a member; organizations are refused
	admin.MakeRequest(t, NewRequestWithValues(t, "POST", groupURL+"/members/add", map[string]string{"uname": "user5"}), http.StatusSeeOther)
	admin.MakeRequest(t, NewRequestWithValues(t, "POST", groupURL+"/members/add", map[string]string{"uname": "org3"}), http.StatusSeeOther)
	unittest.AssertExistsAndLoadBean(t, &organization.UserGroupUser{GroupID: group.ID, UID: 5})
	unittest.AssertNotExistsBean(t, &organization.UserGroupUser{GroupID: group.ID, UID: 3})

	// a user who is not an owner of the organization cannot link groups
	loginUser(t, "user4").MakeRequest(t, NewRequestWithValues(t, "POST", "/org/org3/teams/team1/action/group/link", map[string]string{
		"group_name": "release-managers",
	}), http.StatusNotFound)
	unittest.AssertNotExistsBean(t, &organization.UserGroupTeam{GroupID: group.ID})

	// link the same group to teams of two different organizations
	for _, teamURL := range []string{"/org/org3/teams/team1", "/org/org17/teams/test_team"} {
		resp = admin.MakeRequest(t, NewRequestWithValues(t, "POST", teamURL+"/action/group/link", map[string]string{
			"group_name": "Release-Managers",
		}), http.StatusSeeOther)
		assert.Equal(t, teamURL, test.RedirectURL(resp))
	}
	for _, teamID := range []int64{2, 8} {
		tu := unittest.AssertExistsAndLoadBean(t, &organization.TeamUser{TeamID: teamID, UID: 5})
		assert.Equal(t, organization.MembershipReasonAddedByUserGroup, tu.Reason)
	}

	// the member now has the access of the team
	member.MakeRequest(t, NewRequest(t, "GET", "/org3/repo3"), http.StatusOK)

	// the team page shows the linked group and where the membership comes from
	resp = admin.MakeRequest(t, NewRequest(t, "GET", "/org/org3/teams/team1"), http.StatusOK)
	assert.Contains(t, resp.Body.String(), "joined via a user group")
	doc := NewHTMLParser(t, resp.Body)
	assert.Contains(t, doc.Find("#team-user-groups + .segment + .segment").Text(), "release-managers")

	// the group page lists both teams
	resp = admin.MakeRequest(t, NewRequest(t, "GET", groupURL), http.StatusOK)
	doc = NewHTMLParser(t, resp.Body)
	doc.AssertElement(t, `a[href="/org/org3/teams/team1"]`, true)
	doc.AssertElement(t, `a[href="/org/org17/teams/test_team"]`, true)

	// removing a member that comes from a linked group is refused
	admin.MakeRequest(t, NewRequestWithValues(t, "POST", "/org/org3/teams/team1/action/remove", map[string]string{"uid": "5"}), http.StatusOK)
	unittest.AssertExistsAndLoadBean(t, &organization.TeamUser{TeamID: 2, UID: 5})

	// unlinking the group from org3 removes the access there only
	admin.MakeRequest(t, NewRequestWithValues(t, "POST", "/org/org3/teams/team1/action/group/unlink", map[string]string{
		"group_id": fmt.Sprint(group.ID),
	}), http.StatusSeeOther)
	unittest.AssertNotExistsBean(t, &organization.TeamUser{TeamID: 2, UID: 5})
	unittest.AssertNotExistsBean(t, &organization.OrgUser{OrgID: 3, UID: 5})
	unittest.AssertExistsAndLoadBean(t, &organization.TeamUser{TeamID: 8, UID: 5})
	member.MakeRequest(t, NewRequest(t, "GET", "/org3/repo3"), http.StatusNotFound)

	// deleting the group removes the remaining memberships
	admin.MakeRequest(t, NewRequest(t, "POST", groupURL+"/delete"), http.StatusOK)
	unittest.AssertNotExistsBean(t, &organization.UserGroup{ID: group.ID})
	unittest.AssertNotExistsBean(t, &organization.TeamUser{TeamID: 8, UID: 5})
}

func TestUserGroupAdminPages(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	admin := loginUser(t, "user1")

	resp := admin.MakeRequest(t, NewRequest(t, "GET", "/admin/groups"), http.StatusOK)
	assert.Contains(t, resp.Body.String(), "There are no user groups yet.")

	admin.MakeRequest(t, NewRequestWithValues(t, "POST", "/admin/groups/new", map[string]string{"name": "qa"}), http.StatusSeeOther)
	group := unittest.AssertExistsAndLoadBean(t, &organization.UserGroup{LowerName: "qa"})
	groupURL := fmt.Sprintf("/admin/groups/%d", group.ID)

	resp = admin.MakeRequest(t, NewRequest(t, "GET", "/admin/groups?q=q"), http.StatusOK)
	NewHTMLParser(t, resp.Body).AssertElement(t, fmt.Sprintf(`a[href="%s"]`, groupURL), true)

	// invalid names are rejected
	admin.MakeRequest(t, NewRequestWithValues(t, "POST", groupURL+"/edit", map[string]string{"name": "not valid!"}), http.StatusOK)
	assert.Equal(t, "qa", unittest.AssertExistsAndLoadBean(t, &organization.UserGroup{ID: group.ID}).Name)

	admin.MakeRequest(t, NewRequestWithValues(t, "POST", groupURL+"/edit", map[string]string{
		"name":        "Quality",
		"description": "QA engineers",
	}), http.StatusSeeOther)
	group = unittest.AssertExistsAndLoadBean(t, &organization.UserGroup{ID: group.ID})
	assert.Equal(t, "quality", group.LowerName)
	assert.Equal(t, "QA engineers", group.Description)

	// the pages are reserved to site administrators
	user := loginUser(t, "user2")
	user.MakeRequest(t, NewRequest(t, "GET", "/admin/groups"), http.StatusForbidden)
	user.MakeRequest(t, NewRequestWithValues(t, "POST", "/admin/groups/new", map[string]string{"name": "sneaky"}), http.StatusForbidden)
	unittest.AssertNotExistsBean(t, &organization.UserGroup{LowerName: "sneaky"})

	resp = admin.MakeRequest(t, NewRequest(t, "GET", "/admin/groups/9999"), http.StatusNotFound)
	require.NotNil(t, resp)
}
