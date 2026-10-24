// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	audit_model "forgejo.org/models/audit"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	perm_model "forgejo.org/models/perm"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/setting"
	api "forgejo.org/modules/structs"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Revoking a token stays possible while its organization is archived and in read-only maintenance mode; nothing else
// about tokens does.
func TestOwnedAccessTokenRevokeWhenLocked(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	session := loginUser(t, "user2")
	owner := getUserToken(t, "user2", auth_model.AccessTokenScopeWriteRepository, auth_model.AccessTokenScopeWriteOrganization, auth_model.AccessTokenScopeWriteUser)
	revoked := func(t *testing.T, tok *auth_model.AccessToken) {
		t.Helper()
		assert.NotZero(t, unittest.AssertExistsAndLoadBean(t, &auth_model.AccessToken{ID: tok.ID}).RevokedUnix)
	}
	create := api.CreateOwnedAccessTokenOption{Name: "while-locked", Scopes: []string{"read:repository"}, Permission: "read"}

	t.Run("archived organization", func(t *testing.T) {
		_, err := db.GetEngine(db.DefaultContext).Exec("UPDATE `user` SET is_archived = ? WHERE id = 3", true)
		require.NoError(t, err)
		defer func() {
			_, err := db.GetEngine(db.DefaultContext).Exec("UPDATE `user` SET is_archived = ? WHERE id = 3", false)
			require.NoError(t, err)
		}()

		repoWeb := newOwnedAccessToken(t, auth_model.AccessTokenOwnerRepo, 3, 0, perm_model.AccessModeRead, "read:repository")
		session.MakeRequest(t, NewRequestWithValues(t, "POST", "/org3/repo3/settings/keys/tokens/delete", map[string]string{"id": fmt.Sprint(repoWeb.ID)}), http.StatusOK)
		revoked(t, repoWeb)
		orgWeb := newOwnedAccessToken(t, auth_model.AccessTokenOwnerOrg, 3, 0, perm_model.AccessModeRead, "read:repository")
		session.MakeRequest(t, NewRequestWithValues(t, "POST", "/org/org3/settings/keys/tokens/delete", map[string]string{"id": fmt.Sprint(orgWeb.ID)}), http.StatusOK)
		revoked(t, orgWeb)

		repoAPI := newOwnedAccessToken(t, auth_model.AccessTokenOwnerRepo, 3, 0, perm_model.AccessModeRead, "read:repository")
		MakeRequest(t, NewRequest(t, "DELETE", fmt.Sprintf("/api/v1/repos/org3/repo3/tokens/%d", repoAPI.ID)).AddTokenAuth(owner), http.StatusNoContent)
		revoked(t, repoAPI)
		orgAPI := newOwnedAccessToken(t, auth_model.AccessTokenOwnerOrg, 3, 0, perm_model.AccessModeRead, "read:repository")
		MakeRequest(t, NewRequest(t, "DELETE", fmt.Sprintf("/api/v1/orgs/org3/tokens/%d", orgAPI.ID)).AddTokenAuth(owner), http.StatusNoContent)
		revoked(t, orgAPI)

		// creating and rotating are still refused
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/org3/repo3/tokens", create).AddTokenAuth(owner), http.StatusForbidden)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/orgs/org3/tokens", create).AddTokenAuth(owner), http.StatusForbidden)
		live := newOwnedAccessToken(t, auth_model.AccessTokenOwnerOrg, 3, 0, perm_model.AccessModeRead, "read:repository")
		MakeRequest(t, NewRequest(t, "POST", fmt.Sprintf("/api/v1/orgs/org3/tokens/%d/rotate", live.ID)).AddTokenAuth(owner), http.StatusForbidden)
		session.MakeRequest(t, NewRequestWithValues(t, "POST", "/org/org3/settings/keys/tokens/rotate", map[string]string{"id": fmt.Sprint(live.ID)}), http.StatusForbidden)
	})

	t.Run("read-only maintenance", func(t *testing.T) {
		defer setMaintenance(t, setting.MaintenanceReadOnly, time.Time{})()

		repoAPI := newOwnedAccessToken(t, auth_model.AccessTokenOwnerRepo, 3, 0, perm_model.AccessModeRead, "read:repository")
		MakeRequest(t, NewRequest(t, "DELETE", fmt.Sprintf("/api/v1/repos/org3/repo3/tokens/%d", repoAPI.ID)).AddTokenAuth(owner), http.StatusNoContent)
		revoked(t, repoAPI)
		orgWeb := newOwnedAccessToken(t, auth_model.AccessTokenOwnerOrg, 3, 0, perm_model.AccessModeRead, "read:repository")
		session.MakeRequest(t, NewRequestWithValues(t, "POST", "/org/org3/settings/keys/tokens/delete", map[string]string{"id": fmt.Sprint(orgWeb.ID)}), http.StatusOK)
		revoked(t, orgWeb)

		personal := &auth_model.AccessToken{UID: 2, Name: "personal-while-locked", Scope: "read:repository", ResourceAllRepos: true}
		require.NoError(t, auth_model.NewAccessToken(audit_model.WithSystem(db.DefaultContext, "test"), personal, nil))
		session.MakeRequest(t, NewRequestWithValues(t, "POST", "/user/settings/keys/tokens/delete", map[string]string{"id": fmt.Sprint(personal.ID)}), http.StatusOK)
		unittest.AssertNotExistsBean(t, &auth_model.AccessToken{ID: personal.ID})

		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/org3/repo3/tokens", create).AddTokenAuth(owner), http.StatusServiceUnavailable)
	})
}
