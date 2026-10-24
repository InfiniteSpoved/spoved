// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package user_test

import (
	"testing"

	"forgejo.org/models/auth"
	"forgejo.org/models/db"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAccessTokenBotLink(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	token := &auth.AccessToken{Name: "ci", TokenHash: "bot-link", OwnerType: auth.AccessTokenOwnerOrg, OwnerID: 3, Mode: 1}
	require.NoError(t, db.Insert(t.Context(), token))
	id := user_model.AccessTokenBotID(token.ID)

	// the lookup the permission checks use needs no link
	bot, err := user_model.GetAccessTokenBot(t.Context(), id)
	require.NoError(t, err)
	assert.Empty(t, bot.HomeLink())

	// the places that show the identity get the page that manages the token
	bot, err = user_model.GetPossibleUserByID(t.Context(), id)
	require.NoError(t, err)
	assert.Equal(t, "/org/org3/settings/keys", bot.HomeLink())
	bots, err := user_model.GetPossibleUserByIDs(t.Context(), []int64{id})
	require.NoError(t, err)
	require.Len(t, bots, 1)
	assert.Equal(t, "/org/org3/settings/keys", bots[0].HomeLink())

	names, err := user_model.GetAccessTokenBotNames(t.Context(), []int64{id, user_model.AccessTokenBotID(token.ID + 1000)})
	require.NoError(t, err)
	assert.Equal(t, []string{"ci"}, names)
}
