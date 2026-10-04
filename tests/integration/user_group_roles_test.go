// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	"forgejo.org/models/organization"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	api "forgejo.org/modules/structs"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assertUserRoles(t *testing.T, uid int64, admin, createOrg bool) {
	t.Helper()
	u := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: uid})
	assert.Equal(t, admin, u.IsAdmin, "IsAdmin")
	assert.Equal(t, createOrg, u.AllowCreateOrganization, "AllowCreateOrganization")
}

// setUser1Admin sets the admin flag of user1, the only site administrator of the fixtures.
func setUser1Admin(t *testing.T, admin bool) {
	t.Helper()
	u := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	u.IsAdmin = admin
	require.NoError(t, user_model.UpdateUserCols(db.DefaultContext, u, "is_admin"))
}

func TestUserGroupRolesWeb(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	admin := loginUser(t, "user1")
	member := loginUser(t, "user5")
	assertUserRoles(t, 5, false, false)
	member.MakeRequest(t, NewRequest(t, "GET", "/admin/groups"), http.StatusForbidden)

	admin.MakeRequest(t, NewRequestWithValues(t, "POST", "/admin/groups/new", map[string]string{
		"name":                       "platform-admins",
		"grants_site_admin":          "on",
		"grants_create_organization": "on",
	}), http.StatusSeeOther)
	group := unittest.AssertExistsAndLoadBean(t, &organization.UserGroup{LowerName: "platform-admins"})
	assert.True(t, group.GrantsSiteAdmin)
	assert.True(t, group.GrantsCreateOrganization)
	groupURL := fmt.Sprintf("/admin/groups/%d", group.ID)

	resp := admin.MakeRequest(t, NewRequest(t, "GET", groupURL), http.StatusOK)
	htmlDoc := NewHTMLParser(t, resp.Body)
	htmlDoc.AssertElement(t, `input[name="grants_site_admin"][checked]`, true)
	htmlDoc.AssertElement(t, `input[name="grants_create_organization"][checked]`, true)

	// a member gets both roles, and the admin-only pages open for them
	admin.MakeRequest(t, NewRequestWithValues(t, "POST", groupURL+"/members/add", map[string]string{"uname": "user5"}), http.StatusSeeOther)
	assertUserRoles(t, 5, true, true)
	member.MakeRequest(t, NewRequest(t, "GET", "/admin/groups"), http.StatusOK)

	// the user list and the user edit page name the group
	resp = admin.MakeRequest(t, NewRequest(t, "GET", "/admin/users?q=user5"), http.StatusOK)
	assert.Contains(t, resp.Body.String(), "via group platform-admins")
	resp = admin.MakeRequest(t, NewRequest(t, "GET", "/admin/users/5/edit"), http.StatusOK)
	assert.Equal(t, 2, strings.Count(resp.Body.String(), "via group platform-admins"))

	// taking a role off the group takes it off the members
	admin.MakeRequest(t, NewRequestWithValues(t, "POST", groupURL+"/edit", map[string]string{
		"name":              "platform-admins",
		"grants_site_admin": "on",
	}), http.StatusSeeOther)
	assertUserRoles(t, 5, true, false)

	// the last site administrator cannot be removed
	setUser1Admin(t, false)
	member.MakeRequest(t, NewRequestWithValues(t, "POST", groupURL+"/members/remove", map[string]string{"uid": "5"}), http.StatusSeeOther)
	resp = member.MakeRequest(t, NewRequest(t, "GET", groupURL), http.StatusOK)
	assert.Contains(t, resp.Body.String(), "You cannot remove the last admin.")
	member.MakeRequest(t, NewRequestWithValues(t, "POST", groupURL+"/edit", map[string]string{"name": "platform-admins"}), http.StatusSeeOther)
	member.MakeRequest(t, NewRequest(t, "POST", groupURL+"/delete"), http.StatusOK)
	assert.True(t, unittest.AssertExistsAndLoadBean(t, &organization.UserGroup{ID: group.ID}).GrantsSiteAdmin)
	unittest.AssertExistsAndLoadBean(t, &organization.UserGroupUser{GroupID: group.ID, UID: 5})
	assertUserRoles(t, 5, true, false)

	// with another administrator, deleting the group revokes the role
	setUser1Admin(t, true)
	admin.MakeRequest(t, NewRequest(t, "POST", groupURL+"/delete"), http.StatusOK)
	unittest.AssertNotExistsBean(t, &organization.UserGroup{ID: group.ID})
	assertUserRoles(t, 5, false, false)
	member.MakeRequest(t, NewRequest(t, "GET", "/admin/groups"), http.StatusForbidden)
}

func TestUserGroupRolesAPI(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	token := getUserToken(t, "user1", auth_model.AccessTokenScopeWriteAdmin)

	resp := MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/admin/groups", &api.CreateUserGroupOption{
		Name:            "api-admins",
		GrantsSiteAdmin: true,
	}).AddTokenAuth(token), http.StatusCreated)
	var group api.UserGroup
	DecodeJSON(t, resp, &group)
	assert.True(t, group.GrantsSiteAdmin)
	assert.False(t, group.GrantsCreateOrganization)
	groupURL := "/api/v1/admin/groups/api-admins"

	MakeRequest(t, NewRequest(t, "PUT", groupURL+"/members/user5").AddTokenAuth(token), http.StatusNoContent)
	assertUserRoles(t, 5, true, false)

	yes, no := true, false
	resp = MakeRequest(t, NewRequestWithJSON(t, "PATCH", groupURL, &api.EditUserGroupOption{GrantsCreateOrganization: &yes}).AddTokenAuth(token), http.StatusOK)
	DecodeJSON(t, resp, &group)
	assert.True(t, group.GrantsSiteAdmin)
	assert.True(t, group.GrantsCreateOrganization)
	assertUserRoles(t, 5, true, true)

	resp = MakeRequest(t, NewRequest(t, "GET", groupURL).AddTokenAuth(token), http.StatusOK)
	DecodeJSON(t, resp, &group)
	assert.True(t, group.GrantsSiteAdmin)

	// the last site administrator cannot be removed
	// (user5 is now an administrator through the group)
	memberToken := getUserToken(t, "user5", auth_model.AccessTokenScopeWriteAdmin)
	setUser1Admin(t, false)
	MakeRequest(t, NewRequestWithJSON(t, "PATCH", groupURL, &api.EditUserGroupOption{GrantsSiteAdmin: &no}).AddTokenAuth(memberToken), http.StatusConflict)
	MakeRequest(t, NewRequest(t, "DELETE", groupURL+"/members/user5").AddTokenAuth(memberToken), http.StatusConflict)
	MakeRequest(t, NewRequest(t, "DELETE", groupURL).AddTokenAuth(memberToken), http.StatusConflict)
	assertUserRoles(t, 5, true, true)

	// a manual admin flag is kept when the group goes
	setUser1Admin(t, true)
	MakeRequest(t, NewRequestWithJSON(t, "PATCH", groupURL, &api.EditUserGroupOption{GrantsSiteAdmin: &no}).AddTokenAuth(token), http.StatusOK)
	assertUserRoles(t, 5, false, true)
	MakeRequest(t, NewRequest(t, "DELETE", groupURL).AddTokenAuth(token), http.StatusNoContent)
	assertUserRoles(t, 5, false, false)
	assert.True(t, unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1}).IsAdmin)
}
