// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/test"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testScopePolicy = setting.AccessTokenScopePolicy{
	Allowed:                []string{"write:repository", "read:user"},
	RequireRepoRestriction: []string{"write:repository"},
}

func TestAccessTokenScopePolicyAPI(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	// A token created before the policy keeps working (no change to existing tokens).
	existing := getUserToken(t, "user2", auth_model.AccessTokenScopeReadIssue, auth_model.AccessTokenScopeReadUser)
	adminToken := getUserToken(t, "user1", auth_model.AccessTokenScopeWriteAdmin)
	defer test.MockVariableValue(&setting.AccessTokenScopes, testScopePolicy)()

	create := func(t *testing.T, url, user, token string, opt api.CreateAccessTokenOption, status int) string {
		t.Helper()
		opt.Name = fmt.Sprintf("scope-policy-%d", tokenCounter.Add(1))
		req := NewRequestWithJSON(t, "POST", url, opt)
		if token != "" {
			req.AddTokenAuth(token)
		} else {
			req.AddBasicAuth(user)
		}
		return MakeRequest(t, req, status).Body.String()
	}
	repo1 := []*api.RepoTargetOption{{Owner: "user2", Name: "repo1"}}

	t.Run("non-admin", func(t *testing.T) {
		body := create(t, "/api/v1/users/user2/tokens", "user2", "", api.CreateAccessTokenOption{Scopes: []string{"read:issue"}}, http.StatusUnprocessableEntity)
		assert.Contains(t, body, "read:issue")
		assert.Contains(t, body, "allowed scopes: read:repository, write:repository, read:user")

		create(t, "/api/v1/users/user2/tokens", "user2", "", api.CreateAccessTokenOption{Scopes: []string{"all"}}, http.StatusUnprocessableEntity)
		body = create(t, "/api/v1/users/user2/tokens", "user2", "", api.CreateAccessTokenOption{Scopes: []string{"write:repository"}}, http.StatusUnprocessableEntity)
		assert.Contains(t, body, "write:repository")
		create(t, "/api/v1/users/user2/tokens", "user2", "", api.CreateAccessTokenOption{Scopes: []string{"read:user"}}, http.StatusCreated)
		create(t, "/api/v1/users/user2/tokens", "user2", "", api.CreateAccessTokenOption{Scopes: []string{"read:repository"}, Repositories: repo1}, http.StatusCreated)
		create(t, "/api/v1/users/user2/tokens", "user2", "", api.CreateAccessTokenOption{Scopes: []string{"public-only", "read:user"}}, http.StatusCreated)
	})

	t.Run("existing token still works", func(t *testing.T) {
		MakeRequest(t, NewRequest(t, "GET", "/api/v1/repos/issues/search").AddTokenAuth(existing), http.StatusOK)
	})

	t.Run("site admin is exempt", func(t *testing.T) {
		create(t, "/api/v1/users/user1/tokens", "user1", "", api.CreateAccessTokenOption{Scopes: []string{"all"}}, http.StatusCreated)
	})

	t.Run("admin API: the target user's admin flag decides", func(t *testing.T) {
		create(t, "/api/v1/admin/users/user2/tokens", "", adminToken, api.CreateAccessTokenOption{Scopes: []string{"all"}}, http.StatusUnprocessableEntity)
		create(t, "/api/v1/admin/users/user2/tokens", "", adminToken, api.CreateAccessTokenOption{Scopes: []string{"read:user"}}, http.StatusCreated)

		user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
		user4.IsAdmin = true
		require.NoError(t, user_model.UpdateUserCols(t.Context(), user4, "is_admin"))
		defer func() {
			user4.IsAdmin = false
			require.NoError(t, user_model.UpdateUserCols(t.Context(), user4, "is_admin"))
		}()
		create(t, "/api/v1/admin/users/user4/tokens", "", adminToken, api.CreateAccessTokenOption{Scopes: []string{"all"}}, http.StatusCreated)
	})
}

func TestAccessTokenScopePolicyWeb(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	defer test.MockVariableValue(&setting.AccessTokenScopes, testScopePolicy)()

	post := func(t *testing.T, session *TestSession, name, resource string, status int, scopes ...string) string {
		t.Helper()
		values := url.Values{"name": {name}, "resource": {resource}, "scope": scopes}
		if resource == "repo-specific" {
			values.Set("selected_repo", "user2/repo1")
		}
		return session.MakeRequest(t, NewRequestWithURLValues(t, "POST", "/user/settings/applications/tokens/new", values), status).Body.String()
	}

	t.Run("non-admin", func(t *testing.T) {
		session := loginUser(t, "user2")

		resp := session.MakeRequest(t, NewRequest(t, "GET", "/user/settings/applications/tokens/new"), http.StatusOK)
		doc := NewHTMLParser(t, resp.Body)
		assert.True(t, doc.Find(`#access-token-scope-issue option[value="read:issue"]`).Is("[disabled]"))
		assert.True(t, doc.Find(`#access-token-scope-user option[value="write:user"]`).Is("[disabled]"))
		assert.False(t, doc.Find(`#access-token-scope-user option[value="read:user"]`).Is("[disabled]"))
		assert.False(t, doc.Find(`#access-token-scope-repository option[value="write:repository"]`).Is("[disabled]"))

		body := post(t, session, "web-disallowed", "all", http.StatusOK, "read:issue")
		assert.Contains(t, body, "The scope read:issue is not allowed.")
		unittest.AssertNotExistsBean(t, &auth_model.AccessToken{UID: 2, Name: "web-disallowed"})

		body = post(t, session, "web-unrestricted", "all", http.StatusOK, "write:repository")
		assert.Contains(t, body, "Access tokens with the write:repository scope must be limited to specified repositories.")
		unittest.AssertNotExistsBean(t, &auth_model.AccessToken{UID: 2, Name: "web-unrestricted"})

		post(t, session, "web-restricted", "repo-specific", http.StatusSeeOther, "write:repository")
		unittest.AssertExistsAndLoadBean(t, &auth_model.AccessToken{UID: 2, Name: "web-restricted", ResourceAllRepos: false})
		post(t, session, "web-allowed", "all", http.StatusSeeOther, "read:user")
		unittest.AssertExistsAndLoadBean(t, &auth_model.AccessToken{UID: 2, Name: "web-allowed"})
	})

	t.Run("site admin is exempt", func(t *testing.T) {
		session := loginUser(t, "user1")

		resp := session.MakeRequest(t, NewRequest(t, "GET", "/user/settings/applications/tokens/new"), http.StatusOK)
		assert.Equal(t, 0, NewHTMLParser(t, resp.Body).Find(".access-token-select option[disabled]").Length())

		post(t, session, "web-admin", "all", http.StatusSeeOther, "write:admin", "write:issue")
		unittest.AssertExistsAndLoadBean(t, &auth_model.AccessToken{UID: 1, Name: "web-admin"})
	})
}

func TestAccessTokenScopePolicyAuthorizedIntegrationWeb(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	defer test.MockVariableValue(&setting.AccessTokenScopes, testScopePolicy)()
	session := loginUser(t, "user2")

	post := func(t *testing.T, name, scope string, status int) string {
		t.Helper()
		req := NewRequestWithValues(t, "POST", "/user/settings/authorized-integrations/forgejo-actions-local/new", map[string]string{
			"name": name, "resource": "all", "scope": scope, "source_repo": "user2/repo1",
		})
		return session.MakeRequest(t, req, status).Body.String()
	}

	body := post(t, "ai-disallowed", "read:issue", http.StatusOK)
	assert.Contains(t, body, "The scope read:issue is not allowed.")
	unittest.AssertNotExistsBean(t, &auth_model.AuthorizedIntegration{UserID: 2, Name: "ai-disallowed"})

	body = post(t, "ai-unrestricted", "write:repository", http.StatusOK)
	assert.Contains(t, body, "Access tokens with the write:repository scope must be limited to specified repositories.")
	unittest.AssertNotExistsBean(t, &auth_model.AuthorizedIntegration{UserID: 2, Name: "ai-unrestricted"})

	post(t, "ai-allowed", "read:user", http.StatusSeeOther)
	unittest.AssertExistsAndLoadBean(t, &auth_model.AuthorizedIntegration{UserID: 2, Name: "ai-allowed"})
}
