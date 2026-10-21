// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package misc

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultRobotsTxt(t *testing.T) {
	defer test.MockVariableValue(&setting.AppDocsVer, func() string { return "v0.0" })()

	robotsTxt := string(defaultRobotsTxt())
	assert.Contains(t, robotsTxt, "https://forgejo.org/docs/v0.0/admin/advanced/search-engines/") // derived from setting.AppDocsVer
	assert.Equal(t, robotsTxt, string(defaultRobotsTxt()), "subsequent call still works")
}

func TestRobotsTxtSitemap(t *testing.T) {
	defer test.MockVariableValue(&setting.AppURL, "https://example.com/")()
	defer test.MockVariableValue(&setting.CustomPath, t.TempDir())()
	defer test.MockVariableValue(&setting.Other.EnableSitemap, true)()
	defer test.MockVariableValue(&setting.Service.RequireSignInView, false)()
	defer test.MockVariableValue(&setting.Service.Explore.RequireSigninView, false)()

	get := func() string {
		w := httptest.NewRecorder()
		RobotsTxt(w, httptest.NewRequest("GET", "/robots.txt", nil))
		return w.Body.String()
	}
	assert.True(t, strings.HasSuffix(get(), "\nSitemap: https://example.com/sitemap.xml\n"))

	setting.Service.Explore.RequireSigninView = true
	assert.NotContains(t, get(), "Sitemap:")
	setting.Service.Explore.RequireSigninView = false
	setting.Other.EnableSitemap = false
	assert.NotContains(t, get(), "Sitemap:")
	assert.NotContains(t, string(defaultRobotsTxt()), "Sitemap:", "the cached default is never changed")
}

func TestLlmsTxt(t *testing.T) {
	defer test.MockVariableValue(&setting.AppURL, "https://example.com/")()
	defer test.MockVariableValue(&setting.AppDisplayName, "Example\nForge")()
	defer test.MockVariableValue(&setting.UI.Meta.Description, "Code for everyone")()
	defer test.MockVariableValue(&setting.CustomPath, t.TempDir())()
	defer test.MockVariableValue(&setting.Other.EnableSitemap, true)()
	defer test.MockVariableValue(&setting.API.EnableSwagger, true)()
	defer test.MockVariableValue(&setting.Service.RequireSignInView, false)()
	defer test.MockVariableValue(&setting.Service.Explore, setting.Service.Explore)()
	setting.Service.Explore.RequireSigninView = false
	setting.Service.Explore.DisableUsersPage = true
	setting.Service.Explore.DisableOrganizationsPage = false

	get := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		LlmsTxt(w, httptest.NewRequest("GET", "/llms.txt", nil))
		return w
	}

	w := get()
	assert.Equal(t, "text/markdown; charset=utf-8", w.Header().Get("Content-Type"))
	body := w.Body.String()
	assert.True(t, strings.HasPrefix(body, "# Example Forge\n\n> Code for everyone\n\n"))
	assert.Contains(t, body, "(https://example.com/explore/repos)")
	assert.NotContains(t, body, "explore/users")
	assert.Contains(t, body, "(https://example.com/explore/organizations)")
	assert.Contains(t, body, "(https://example.com/api/swagger)")
	assert.Contains(t, body, "(https://example.com/sitemap.xml)")

	setting.Service.RequireSignInView = true
	body = get().Body.String()
	assert.Contains(t, body, "Sign-in is required")
	assert.NotContains(t, body, "explore/")
	assert.NotContains(t, body, "sitemap")
	setting.Service.RequireSignInView = false

	require.NoError(t, os.MkdirAll(filepath.Join(setting.CustomPath, "public"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(setting.CustomPath, "public/llms.txt"), []byte("# Custom\n"), 0o644))
	assert.Equal(t, "# Custom\n", get().Body.String())
}
