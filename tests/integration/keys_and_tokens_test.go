// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/modules/translation"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
)

func TestKeysAndTokensPage(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	session := loginUser(t, "user2")

	body := session.MakeRequest(t, NewRequest(t, "GET", "/user/settings/keys"), http.StatusOK).Body.String()
	sshAt := strings.Index(body, "Manage SSH keys")
	gpgAt := strings.Index(body, "Manage GPG keys")
	tokensAt := strings.Index(body, "Access tokens")
	assert.Greater(t, sshAt, 0)
	assert.Greater(t, gpgAt, sshAt)
	assert.Greater(t, tokensAt, gpgAt)
	assert.Contains(t, body, `href="/user/settings/keys/tokens/new"`)
	assert.Contains(t, body, "Keys and tokens")

	// Applications is OAuth2 only
	apps := session.MakeRequest(t, NewRequest(t, "GET", "/user/settings/applications"), http.StatusOK).Body.String()
	assert.NotContains(t, apps, "/tokens/new")
	assert.NotContains(t, apps, `id="delete-token"`)
	assert.Contains(t, apps, translation.NewLocale("en-US").TrString("settings.authorized_oauth2_applications"))
}

func TestKeysAndTokensLifecycle(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	session := loginUser(t, "user2")

	createApplicationSettingsToken(t, session, "keys-page-token", auth_model.AccessTokenScopeReadUser)
	token := assertAccessToken(t, session)
	name, id := findLatestTokenID(t, session)
	assert.Equal(t, "keys-page-token", name)
	MakeRequest(t, NewRequest(t, "GET", "/api/v1/user").AddTokenAuth(token), http.StatusOK)

	form := map[string]string{"id": strconv.Itoa(id)}
	resp := session.MakeRequest(t, NewRequestWithValues(t, "POST", "/user/settings/keys/tokens/regenerate", form), http.StatusOK)
	assert.Contains(t, resp.Body.String(), "/user/settings/keys")
	MakeRequest(t, NewRequest(t, "GET", "/api/v1/user").AddTokenAuth(token), http.StatusUnauthorized)

	session.MakeRequest(t, NewRequestWithValues(t, "POST", "/user/settings/keys/tokens/delete", form), http.StatusOK)
	name, _ = findLatestTokenID(t, session)
	assert.NotEqual(t, "keys-page-token", name)
}

func TestOldTokenRoutesGone(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	session := loginUser(t, "user2")
	for _, path := range []string{"new", "delete", "regenerate"} {
		session.MakeRequest(t, NewRequestWithValues(t, "POST", "/user/settings/applications/tokens/"+path, map[string]string{"name": "x"}), http.StatusNotFound)
	}
	session.MakeRequest(t, NewRequest(t, "GET", "/user/settings/applications/tokens/new"), http.StatusNotFound)
}

func TestKeysPostErrorKeepsTokens(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	session := loginUser(t, "user2")
	createApplicationSettingsToken(t, session, "kept-token", auth_model.AccessTokenScopeReadUser)

	content, _ := newTestSSHKey(t)
	form := map[string]string{"type": "ssh", "title": "dup-key", "content": content}
	session.MakeRequest(t, NewRequestWithValues(t, "POST", "/user/settings/keys", form), http.StatusSeeOther)
	form["title"] = "dup-key-2"
	resp := session.MakeRequest(t, NewRequestWithValues(t, "POST", "/user/settings/keys", form), http.StatusOK)
	body := resp.Body.String()
	assert.Contains(t, body, "kept-token")
	assert.Contains(t, body, `href="/user/settings/keys/tokens/new"`)
}
