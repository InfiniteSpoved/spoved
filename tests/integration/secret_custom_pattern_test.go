// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"net/http"
	"strings"
	"testing"

	audit_model "forgejo.org/models/audit"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	secretscan_model "forgejo.org/models/secretscan"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/util"
	secretscan_service "forgejo.org/services/secretscan"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSecretAlertsOfCustomPatterns: the lists, the repository pages and the API name the alerts of a custom
// pattern by the pattern, label a deleted one, and show the name of a deleted one only to its owners.
func TestSecretAlertsOfCustomPatterns(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	f := newSecretListFixture(t)
	sys := audit_model.WithSystem(db.DefaultContext, "test")
	admin := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	org := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 3})
	input := func(name string) secretscan_service.PatternInput {
		return secretscan_service.PatternInput{
			Name: name, Pattern: `acme-key-[a-z0-9]{20}`, Keywords: []string{"acme-key"}, Sample: "K=acme-" + "key-" + strings.Repeat("a1", 10),
		}
	}
	create := func(owner *user_model.User, name string) *secretscan_model.CustomPattern {
		p, err := secretscan_service.CreatePattern(sys, admin, owner, input(name))
		require.NoError(t, err)
		return p
	}
	live, off := create(org, "org live"), create(nil, "site off")
	orgGone, siteGone := create(org, "org gone"), create(nil, "site gone")
	require.NoError(t, secretscan_service.SetPatternEnabled(sys, nil, off.ID, false))
	require.NoError(t, secretscan_service.DeletePattern(sys, org, orgGone.ID))
	require.NoError(t, secretscan_service.DeletePattern(sys, nil, siteGone.ID))
	for i, p := range []*secretscan_model.CustomPattern{live, off, orgGone, siteGone} {
		seedSecretAlert(t, f.root, p.RuleID(), "custom", secretscan_model.KindRepo, "custom/"+string(rune('a'+i))+".env")
	}
	seedSecretAlert(t, f.root, "custom-99999", "custom", secretscan_model.KindRepo, "custom/z.env") // its row is gone

	custom := func(rows []string) []string {
		var out []string
		for _, r := range rows {
			if strings.Contains(r, "custom/") {
				out = append(out, r)
			}
		}
		return out
	}
	row := func(label, path string) string { return "org3/sl-root | " + label + " | custom/" + path + ":3 | Open" }

	t.Run("an organization's owners", func(t *testing.T) {
		got := custom(secretListRows(t, secGet(t, loginUser(t, "user2"), slOrg, http.StatusOK)))
		assert.Equal(t, []string{
			row("Deleted pattern", "z.env"), // newest first
			row("Deleted pattern", "d.env"), // a site pattern: its name is not for an organization
			row("Deleted pattern (org gone)", "c.env"),
			row("site off (disabled)", "b.env"),
			row("org live", "a.env"),
		}, got)
	})
	t.Run("a member narrowed to their repositories", func(t *testing.T) {
		got := custom(secretListRows(t, secGet(t, loginUser(t, "user4"), slOrg, http.StatusOK)))
		assert.Equal(t, []string{
			row("Deleted pattern", "z.env"), row("Deleted pattern", "d.env"), row("Deleted pattern", "c.env"),
			row("site off (disabled)", "b.env"), row("org live", "a.env"),
		}, got)
	})
	t.Run("the instance list", func(t *testing.T) {
		got := custom(secretListRows(t, secGet(t, loginUser(t, "user1"), slAdmin, http.StatusOK)))
		assert.Equal(t, []string{
			row("Deleted pattern", "z.env"), row("Deleted pattern (site gone)", "d.env"), row("Deleted pattern", "c.env"),
			row("site off (disabled)", "b.env"), row("org live", "a.env"),
		}, got)
	})
	t.Run("the repository page and the API", func(t *testing.T) {
		resp := secGet(t, loginUser(t, "user2"), "/org3/sl-root/security/secrets", http.StatusOK)
		body := resp.Body.String()
		assert.Contains(t, body, "org live")
		assert.Contains(t, body, "site off (disabled)")
		assert.Contains(t, body, "Deleted pattern")
		assert.NotContains(t, body, "org gone")
		assert.NotContains(t, body, "site gone")

		token := getUserToken(t, "user2", auth_model.AccessTokenScopeReadRepository)
		var list []*api.SecretScanningAlert
		DecodeJSON(t, MakeRequest(t, NewRequest(t, "GET", "/api/v1/repos/org3/sl-root/secret-scanning/alerts?state=open").AddTokenAuth(token), http.StatusOK), &list)
		names := map[string]string{}
		for _, a := range list {
			names[a.SecretType] = a.SecretTypeDisplayName
		}
		assert.Equal(t, "org live", names[live.RuleID()])
		assert.Equal(t, "site off", names[off.RuleID()])
		assert.Equal(t, "Deleted pattern", names[orgGone.RuleID()])
		assert.Equal(t, "Deleted pattern", names[siteGone.RuleID()])
		assert.Equal(t, "Deleted pattern", names["custom-99999"])
	})
	t.Run("the CSV names them the same way", func(t *testing.T) {
		resp := slPost(t, loginUser(t, "user4"), slOrg, map[string]string{"q": "sl-root"}, http.StatusOK)
		assert.Contains(t, resp.Body.String(), "org live")
		assert.NotContains(t, resp.Body.String(), "org gone")
		assert.NotContains(t, resp.Body.String(), "site gone")
	})
}

// The labels of custom patterns cost one query per list, however many patterns the page holds.
func TestSecretAlertListsQueryCountCustom(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	f := newSecretListFixture(t)
	sys := audit_model.WithSystem(db.DefaultContext, "test")
	admin := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	org := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 3})
	n := 0
	add := func(owner *user_model.User, deleted bool) {
		n++
		p, err := secretscan_service.CreatePattern(sys, admin, owner, secretscan_service.PatternInput{
			Name: "p" + string(rune('a'+n)), Pattern: `acme-key-[a-z0-9]{20}`, Keywords: []string{"acme-key"}, Sample: "K=acme-" + "key-" + strings.Repeat("a1", 10),
		})
		require.NoError(t, err)
		seedSecretAlert(t, f.root, p.RuleID(), "custom", secretscan_model.KindRepo, "c/"+p.Name)
		if deleted {
			require.NoError(t, secretscan_service.DeletePattern(sys, owner, p.ID))
		}
	}
	add(org, false)
	cases := map[string]struct {
		s    *TestSession
		path string
		post bool
	}{
		"owner": {loginUser(t, "user2"), slOrg, false}, "member": {loginUser(t, "user4"), slOrg, false},
		"admin": {loginUser(t, "user1"), slAdmin, false}, "csv": {loginUser(t, "user2"), slOrg, true},
	}
	count := func() map[string]int64 {
		out := map[string]int64{}
		for name, c := range cases {
			request := func() {
				if c.post {
					c.s.MakeRequest(t, NewRequestWithValues(t, "POST", c.path+"/export", map[string]string{}), http.StatusOK)
				} else {
					c.s.MakeRequest(t, NewRequest(t, "GET", c.path), http.StatusOK)
				}
			}
			request()
			out[name] = stableQueryCount(t, request)
		}
		return out
	}
	few := count()
	for i := range 12 {
		add(util.Iif(i%2 == 0, org, nil), i%3 == 0)
	}
	assert.Equal(t, few, count())
}
