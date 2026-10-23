// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package authz

import (
	"strings"
	"testing"
	"time"

	"forgejo.org/models/auth"
	"forgejo.org/models/perm"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetAuthorizationReducerForAccessToken(t *testing.T) {
	defer unittest.OverrideFixtures("services/authz/TestGetAuthorizationReducerForAccessToken")()
	require.NoError(t, unittest.PrepareTestDatabase())

	t.Run("all access", func(t *testing.T) {
		token := unittest.AssertExistsAndLoadBean(t, &auth.AccessToken{ID: 5})
		reducer, err := GetAuthorizationReducerForAccessToken(t.Context(), token)
		require.NoError(t, err)
		assert.IsType(t, &AllAccessAuthorizationReducer{}, reducer)
	})

	t.Run("public resources only", func(t *testing.T) {
		token := unittest.AssertExistsAndLoadBean(t, &auth.AccessToken{ID: 6})
		reducer, err := GetAuthorizationReducerForAccessToken(t.Context(), token)
		require.NoError(t, err)
		assert.IsType(t, &PublicReposAuthorizationReducer{}, reducer)
	})

	t.Run("specific repos only", func(t *testing.T) {
		token := unittest.AssertExistsAndLoadBean(t, &auth.AccessToken{ID: 7})
		reducer, err := GetAuthorizationReducerForAccessToken(t.Context(), token)
		require.NoError(t, err)

		specific, ok := reducer.(*SpecificReposAuthorizationReducer)
		require.True(t, ok)
		require.NotNil(t, specific)

		require.Len(t, specific.ResourceRepos, 1)
		assert.EqualValues(t, 1, specific.ResourceRepos[0].GetTargetRepoID())
	})
}

func TestValidateAccessToken(t *testing.T) {
	t.Run("valid - all access", func(t *testing.T) {
		token := &auth.AccessToken{
			ResourceAllRepos: true,
			Scope:            auth.AccessTokenScopeReadRepository,
		}
		err := ValidateAccessToken(&user_model.User{}, token, nil)
		require.NoError(t, err)
	})

	t.Run("valid - specified repos", func(t *testing.T) {
		token := &auth.AccessToken{
			ResourceAllRepos: false,
			Scope:            auth.AccessTokenScopeReadRepository,
		}
		resources := []*auth.AccessTokenResourceRepo{{RepoID: 12}}
		err := ValidateAccessToken(&user_model.User{}, token, resources)
		require.NoError(t, err)
	})

	t.Run("invalid - no specified repos", func(t *testing.T) {
		token := &auth.AccessToken{
			ResourceAllRepos: false,
			Scope:            auth.AccessTokenScopeReadRepository,
		}
		resources := []*auth.AccessTokenResourceRepo{}
		err := ValidateAccessToken(&user_model.User{}, token, resources)
		require.ErrorIs(t, err, ErrSpecifiedReposNone)
	})

	t.Run("invalid - specified repos & public-only", func(t *testing.T) {
		token := &auth.AccessToken{
			ResourceAllRepos: false,
			Scope:            auth.AccessTokenScope(strings.Join([]string{string(auth.AccessTokenScopePublicOnly), string(auth.AccessTokenScopeReadRepository)}, ",")),
		}
		resources := []*auth.AccessTokenResourceRepo{{RepoID: 12}}
		err := ValidateAccessToken(&user_model.User{}, token, resources)
		require.ErrorIs(t, err, ErrSpecifiedReposNoPublicOnly)
	})

	t.Run("invalid - specified repos unsupported scopes", func(t *testing.T) {
		token := &auth.AccessToken{
			ResourceAllRepos: false,
			Scope:            auth.AccessTokenScopeReadAdmin,
		}
		resources := []*auth.AccessTokenResourceRepo{{RepoID: 12}}
		err := ValidateAccessToken(&user_model.User{}, token, resources)
		require.ErrorIs(t, err, ErrSpecifiedReposInvalidScope)
		require.ErrorContains(t, err, string(auth.AccessTokenScopeReadAdmin))
	})
}

func TestValidateOwnedAccessTokenPackageScope(t *testing.T) {
	expires := timeutil.TimeStampNow().AddDuration(24 * time.Hour)
	for _, c := range []struct {
		name      string
		ownerType auth.AccessTokenOwnerType
		allRepos  bool
		ok        bool
	}{
		{"organization, every repository", auth.AccessTokenOwnerOrg, true, true},
		{"organization, selected repositories", auth.AccessTokenOwnerOrg, false, false},
		{"project", auth.AccessTokenOwnerProject, true, false},
		{"repository", auth.AccessTokenOwnerRepo, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			token := &auth.AccessToken{OwnerType: c.ownerType, ResourceAllRepos: c.allRepos, Mode: perm.AccessModeWrite, Scope: "read:package", ExpiresUnix: expires}
			err := ValidateOwnedAccessToken(token, 1)
			if c.ok {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, auth.ErrOwnedTokenPackageScope)
			}
			assert.Equal(t, c.ok, token.PackageAccessMode(token.OwnerID) == perm.AccessModeWrite)
		})
	}
}
