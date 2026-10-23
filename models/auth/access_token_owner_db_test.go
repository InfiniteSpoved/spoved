// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package auth_test

import (
	"fmt"
	"testing"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	"forgejo.org/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeleteOwnedAccessTokens(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	n := 0
	insert := func(ownerType auth_model.AccessTokenOwnerType, ownerID, groupID int64, repoIDs ...int64) int64 {
		token := &auth_model.AccessToken{OwnerType: ownerType, OwnerID: ownerID, GroupID: groupID, TokenHash: fmt.Sprintf("owned-%d", n)}
		n++
		require.NoError(t, db.Insert(t.Context(), token))
		for _, repoID := range repoIDs {
			require.NoError(t, db.Insert(t.Context(), &auth_model.AccessTokenResourceRepo{TokenID: token.ID, RepoID: repoID}))
		}
		return token.ID
	}
	exists := func(id int64) bool {
		has, err := db.GetEngine(t.Context()).ID(id).Exist(&auth_model.AccessToken{})
		require.NoError(t, err)
		return has
	}

	org := insert(auth_model.AccessTokenOwnerOrg, 3, 0, 3, 5)
	project := insert(auth_model.AccessTokenOwnerProject, 3, 1)
	otherProject := insert(auth_model.AccessTokenOwnerProject, 3, 2)
	repo := insert(auth_model.AccessTokenOwnerRepo, 3, 0)
	otherOrg := insert(auth_model.AccessTokenOwnerOrg, 6, 0)

	require.NoError(t, auth_model.DeleteOwnedAccessTokens(t.Context(), auth_model.AccessTokenOwnerProject, 3, 1))
	assert.False(t, exists(project))
	assert.True(t, exists(otherProject))

	require.NoError(t, auth_model.DeleteOwnedAccessTokens(t.Context(), auth_model.AccessTokenOwnerOrg, 3, 0))
	assert.False(t, exists(org))
	assert.False(t, exists(otherProject))
	unittest.AssertNotExistsBean(t, &auth_model.AccessTokenResourceRepo{TokenID: org})
	assert.True(t, exists(repo), "repository 3's token is not organization 3's")
	assert.True(t, exists(otherOrg))
	assert.True(t, exists(1), "personal access tokens stay")

	require.NoError(t, auth_model.DeleteOwnedAccessTokens(t.Context(), auth_model.AccessTokenOwnerRepo, 3, 0))
	assert.False(t, exists(repo))
	require.Error(t, auth_model.DeleteOwnedAccessTokens(t.Context(), auth_model.AccessTokenOwnerUser, 1, 0))
	assert.True(t, exists(1))
}
