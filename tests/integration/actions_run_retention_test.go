// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"net/http"
	"testing"

	actions_model "forgejo.org/models/actions"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func reloadedActionsConfig(t *testing.T) *repo_model.ActionsConfig {
	t.Helper()
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	u, err := repo.GetUnit(t.Context(), unit.TypeActions)
	require.NoError(t, err)
	return u.ActionsConfig()
}

func TestActionsRunRetentionRepoSettings(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	admin := loginUser(t, "user2")
	collaborator := loginUser(t, "user5") // write access, no admin
	postURL := "/user2/repo1/settings/actions/retention"
	days := func() int64 {
		return reloadedActionsConfig(t).RunRetentionDays
	}
	post := func(sess *TestSession, value string, status int) {
		sess.MakeRequest(t, NewRequestWithValues(t, "POST", postURL, map[string]string{"run_retention_days": value}), status)
	}

	t.Run("admin sets and clears it", func(t *testing.T) {
		post(admin, "7", http.StatusSeeOther)
		assert.EqualValues(t, 7, days())

		page := admin.MakeRequest(t, NewRequest(t, "GET", "/user2/repo1/settings/actions/general"), http.StatusOK).Body.String()
		assert.Contains(t, page, `name="run_retention_days"`)
		assert.Contains(t, page, `value="7"`)
		// the runs list tells everyone the effective period
		assert.Contains(t, admin.MakeRequest(t, NewRequest(t, "GET", "/user2/repo1/actions"), http.StatusOK).Body.String(), "Runs older than 7 days are deleted automatically.")

		post(admin, "", http.StatusSeeOther)
		assert.Zero(t, days())
	})

	t.Run("invalid values are refused", func(t *testing.T) {
		post(admin, "7", http.StatusSeeOther)
		for _, value := range []string{"abc", "-1", "36501", "1.5", "99999999999999999999"} {
			post(admin, value, http.StatusSeeOther)
			assert.EqualValues(t, 7, days(), value)
		}
	})

	t.Run("the general form keeps it", func(t *testing.T) {
		admin.MakeRequest(t, NewRequestWithValues(t, "POST", "/user2/repo1/settings/actions/general", map[string]string{"default_token_permissions": "read"}), http.StatusSeeOther)
		cfg := reloadedActionsConfig(t)
		assert.EqualValues(t, 7, cfg.RunRetentionDays)
		assert.Equal(t, "read", cfg.DefaultTokenPermissions)
	})

	t.Run("only repository admins", func(t *testing.T) {
		post(collaborator, "1", http.StatusNotFound)
		MakeRequest(t, NewRequestWithValues(t, "POST", postURL, map[string]string{"run_retention_days": "1"}), http.StatusSeeOther) // sign-in redirect
		assert.EqualValues(t, 7, days())
	})
}

func TestActionsRunRetentionOrgSettings(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	owner := loginUser(t, "user2")  // owner of org3
	member := loginUser(t, "user4") // member of org3, not an owner
	other := loginUser(t, "user8")
	pageURL := "/org/org3/settings/actions/retention"
	days := func() int64 {
		d, err := actions_model.GetOrgRunRetentionDays(t.Context(), 3)
		require.NoError(t, err)
		return d
	}
	post := func(sess *TestSession, value string, status int) {
		sess.MakeRequest(t, NewRequestWithValues(t, "POST", pageURL, map[string]string{"run_retention_days": value}), status)
	}

	t.Run("owner sets and clears it", func(t *testing.T) {
		assert.Contains(t, owner.MakeRequest(t, NewRequest(t, "GET", pageURL), http.StatusOK).Body.String(), `name="run_retention_days"`)

		post(owner, "14", http.StatusSeeOther)
		assert.EqualValues(t, 14, days())
		assert.Contains(t, owner.MakeRequest(t, NewRequest(t, "GET", pageURL), http.StatusOK).Body.String(), `value="14"`)

		post(owner, "", http.StatusSeeOther)
		assert.Zero(t, days())
	})

	t.Run("invalid values are refused", func(t *testing.T) {
		post(owner, "14", http.StatusSeeOther)
		for _, value := range []string{"abc", "-1", "36501"} {
			post(owner, value, http.StatusSeeOther)
			assert.EqualValues(t, 14, days(), value)
		}
	})

	t.Run("only owners see and change it", func(t *testing.T) {
		for _, sess := range []*TestSession{member, other} {
			sess.MakeRequest(t, NewRequest(t, "GET", pageURL), http.StatusNotFound)
			post(sess, "1", http.StatusNotFound)
		}
		MakeRequest(t, NewRequest(t, "GET", pageURL), http.StatusSeeOther) // sign-in redirect
		MakeRequest(t, NewRequestWithValues(t, "POST", pageURL, map[string]string{"run_retention_days": "1"}), http.StatusSeeOther)
		assert.EqualValues(t, 14, days())
	})
}
