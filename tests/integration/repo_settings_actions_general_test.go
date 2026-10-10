// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"net/http"
	"testing"

	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
)

func TestRepoSettingsActionsGeneral(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	const link = "/user5/repo4/settings/actions/general"
	stored := func(t *testing.T) string {
		t.Helper()
		return unittest.AssertExistsAndLoadBean(t, &repo_model.RepoUnit{RepoID: 4, Type: unit.TypeActions}).ActionsConfig().DefaultTokenPermissions
	}
	session := loginUser(t, "user5")

	t.Run("instance default", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Actions.DefaultTokenPermissions, "read")()
		doc := NewHTMLParser(t, session.MakeRequest(t, NewRequest(t, "GET", link), http.StatusOK).Body)
		doc.AssertElementChecked(t, "#default_token_permissions_instance")
		assert.Contains(t, doc.Find(".repo-setting-content").Text(), "Currently read-only.")
		doc.AssertElement(t, `.menu a.active.item[href="/user5/repo4/settings/actions/general"]`, true)
	})

	t.Run("save", func(t *testing.T) {
		session.MakeRequest(t, NewRequestWithValues(t, "POST", link, map[string]string{"default_token_permissions": "read"}), http.StatusSeeOther)
		assert.Equal(t, "read", stored(t))
		doc := NewHTMLParser(t, session.MakeRequest(t, NewRequest(t, "GET", link), http.StatusOK).Body)
		doc.AssertElementChecked(t, "#default_token_permissions_read")

		session.MakeRequest(t, NewRequestWithValues(t, "POST", link, map[string]string{"default_token_permissions": "banana"}), http.StatusBadRequest)
		assert.Equal(t, "read", stored(t))

		session.MakeRequest(t, NewRequestWithValues(t, "POST", link, map[string]string{"default_token_permissions": ""}), http.StatusSeeOther)
		assert.Empty(t, stored(t))
	})

	t.Run("not an admin", func(t *testing.T) {
		other := loginUser(t, "user2")
		other.MakeRequest(t, NewRequest(t, "GET", link), http.StatusNotFound)
		other.MakeRequest(t, NewRequestWithValues(t, "POST", link, map[string]string{"default_token_permissions": "write"}), http.StatusNotFound)
		assert.Empty(t, stored(t))
	})

	t.Run("actions disabled", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Actions.Enabled, false)()
		session.MakeRequest(t, NewRequest(t, "GET", link), http.StatusNotFound)
	})
}
