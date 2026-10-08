// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"forgejo.org/models/auth"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/structs"
	"forgejo.org/modules/test"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
)

// brandAllowed are visible phrases that legitimately name Forgejo, the upstream project.
var brandAllowed = []string{
	// group-map placeholders of the upstream LDAP/OAuth2 auth source templates
	"MyForgejoOrganization", "MyForgejoTeam1", "MyForgejoTeam2",
	// the push mirror branch filter help links to Forgejo's documentation
	"Forgejo documentation",
	// CodeHat does not build its own runner: the setup pages name the upstream Forgejo Runner and its command
	"Forgejo Runner", "forgejo-runner",
	// the "Help us translate" line on the appearance page names the upstream translation project
	"Help us translate Forgejo",
}

func assertNoBrandMentions(t *testing.T, session *TestSession, url string) {
	t.Helper()
	req := NewRequest(t, "GET", url)
	var body string
	if session == nil {
		body = MakeRequest(t, req, http.StatusOK).Body.String()
	} else {
		body = session.MakeRequest(t, req, http.StatusOK).Body.String()
	}
	for _, phrase := range brandAllowed {
		body = strings.ReplaceAll(body, phrase, "")
	}
	assert.Empty(t, test.BrandMentions(body), url)
}

func TestBranding(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	t.Run("Signed out", func(t *testing.T) {
		for _, url := range []string{"/", "/user/login"} {
			assertNoBrandMentions(t, nil, url)
		}

		page := NewHTMLParser(t, MakeRequest(t, NewRequest(t, "GET", "/"), http.StatusOK).Body)
		assert.Contains(t, page.Find("title").Text(), "CodeHat")
		assert.Contains(t, page.Find("footer").Text(), "Powered by CodeHat")
		page.AssertElement(t, `footer a.codehat-credit[href="https://forgejo.org"]`, true)
		og, _ := page.Find(`meta[property="og:description"]`).Attr("content")
		assert.Contains(t, og, "CodeHat")
	})

	t.Run("Site admin", func(t *testing.T) {
		source := addAuthSource(t, map[string]string{
			"type":            fmt.Sprintf("%d", auth.OAuth2),
			"name":            "branding",
			"is_active":       "on",
			"oauth2_provider": "gitlab",
		})
		session := loginUser(t, "user1")
		for _, url := range []string{"/admin", "/admin/webhook-types", "/admin/auths/new", fmt.Sprintf("/admin/auths/%d", source.ID), "/user/settings/keys"} {
			assertNoBrandMentions(t, session, url)
		}
	})

	t.Run("Repo admin", func(t *testing.T) {
		session := loginUser(t, "user2")
		for _, url := range []string{
			"/user2/repo1/settings",
			"/user2/repo1/settings/branches",
			"/user2/repo1/settings/hooks",
			"/user2/repo1/settings/hooks/forgejo/new",
			"/user2/repo1/settings/hooks/slack/new",
			"/user2/repo1/settings/hooks/discord/new",
			"/user2/repo1/settings/hooks/packagist/new",
		} {
			assertNoBrandMentions(t, session, url)
		}
	})

	t.Run("Runner setup", func(t *testing.T) {
		// the setup page is the response to creating a runner, in every scope
		for _, c := range []struct{ user, url string }{
			{"user2", "/user2/repo1/settings/actions/runners/new"},
			{"user2", "/user/settings/actions/runners/new"},
			{"user2", "/org/org3/settings/actions/runners/new"},
			{"user1", "/admin/actions/runners/new"},
		} {
			req := NewRequestWithValues(t, "POST", c.url, map[string]string{"runner_name": "branding"})
			body := loginUser(t, c.user).MakeRequest(t, req, http.StatusOK).Body.String()
			assert.Contains(t, body, "codehat:", c.url)
			assert.Contains(t, body, "https://code.forgejo.org/forgejo/runner/releases", c.url)
			assert.Contains(t, body, "forgejo-runner daemon", c.url)
			assert.NotContains(t, body, "forgejo:", c.url) // BrandMentions is case-sensitive
			for _, phrase := range brandAllowed {
				body = strings.ReplaceAll(body, phrase, "")
			}
			assert.Empty(t, test.BrandMentions(body), c.url)
		}
	})

	t.Run("API versions unchanged", func(t *testing.T) {
		var version structs.ServerVersion
		DecodeJSON(t, MakeRequest(t, NewRequest(t, "GET", "/api/v1/version"), http.StatusOK), &version)
		assert.Equal(t, setting.AppVer, version.Version)

		resp := MakeRequest(t, NewRequest(t, "GET", "/api/forgejo/v1/version"), http.StatusOK)
		assert.JSONEq(t, `{"version":"1.0.0"}`, resp.Body.String())
	})
}
