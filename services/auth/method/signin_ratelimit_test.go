// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package method

import (
	"testing"

	"forgejo.org/models/db"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/ratelimit"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserLimitKey(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	defer test.MockVariableValue(&setting.RateLimit.Enabled, true)()
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	uid := ratelimit.UserIDKey(user2.ID)

	// a username and an activated email address of one account share a budget, however they are written
	for _, id := range []string{"user2", " User2 ", "user2@example.com", " USER2@Example.com "} {
		assert.Equal(t, uid, UserLimitKey(t.Context(), id), id)
	}

	// everything else keeps the per-identifier key: unknown names, an unactivated email address, empty input
	for _, id := range []string{"nobody", "nobody@example.com", "user11@example.com", "", "  "} {
		assert.Equal(t, ratelimit.UserKey(id), UserLimitKey(t.Context(), id), id)
	}

	// inactive and prohibit-login accounts don't alias either, by username or email
	for _, col := range []string{"is_active", "prohibit_login"} {
		_, err := db.GetEngine(t.Context()).Exec("UPDATE `user` SET is_active = ?, prohibit_login = ? WHERE id = 2", col != "is_active", col == "prohibit_login")
		require.NoError(t, err)
		for _, id := range []string{"user2", "user2@example.com"} {
			assert.Equal(t, ratelimit.UserKey(id), UserLimitKey(t.Context(), id), col+" "+id)
		}
	}
}

func TestUserLimitKeyDisabled(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	defer test.MockVariableValue(&setting.RateLimit.Enabled, false)()
	assert.Equal(t, ratelimit.UserKey("user2"), UserLimitKey(t.Context(), "user2"))
}
