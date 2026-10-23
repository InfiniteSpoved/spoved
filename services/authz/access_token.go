// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package authz

import (
	"context"
	"errors"
	"fmt"
	"slices"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"
)

func GetAuthorizationReducerForAccessToken(ctx context.Context, token *auth_model.AccessToken) (AuthorizationReducer, error) {
	if token.ResourceAllRepos {
		if publicOnly, err := token.Scope.PublicOnly(); err != nil {
			return nil, fmt.Errorf("PublicOnly: %w", err)
		} else if publicOnly {
			return &PublicReposAuthorizationReducer{}, nil
		}
		return &AllAccessAuthorizationReducer{}, nil
	}

	repos, err := auth_model.GetRepositoriesAccessibleWithToken(ctx, token.ID)
	if err != nil {
		return nil, fmt.Errorf("GetRepositoriesAccessibleWithToken: %w", err)
	}
	// Cast slice into []RepoGetter
	iface := make([]RepoGetter, len(repos))
	for i, r := range repos {
		iface[i] = r
	}
	return &SpecificReposAuthorizationReducer{ResourceRepos: iface}, nil
}

// Validate that an access token's state is valid for creation.  For example, that it doesn't have a conflicting set of
// resources (public-only and specific repositories), and other similar checks.  The owner's admin status decides
// whether the scope policy applies.
func ValidateAccessToken(owner *user_model.User, token *auth_model.AccessToken, repoResources []*auth_model.AccessTokenResourceRepo) error {
	if err := ValidateExpiry(setting.AccessTokenExpiry, token.ExpiresUnix); err != nil {
		return err
	}
	if err := ValidateScopePolicy(owner, token.Scope, token.ResourceAllRepos); err != nil {
		return err
	}
	return ValidateRepositoryResource(token.ResourceAllRepos, token.Scope, len(repoResources))
}

// ErrOwnedTokenNoScope refuses a token of an organization, a project or a repository without a scope: it could do
// nothing.
var ErrOwnedTokenNoScope = errors.New("access token must have a scope")

// ValidateOwnedAccessToken checks a new token of an organization, a project or a repository: the scopes and
// permission such a token may have, the expiry policy and the instance's scope policy (no site admin is exempt: the
// token acts as itself), and for an organization token limited to selected repositories, numRepos > 0 and
// repository-only scopes, as for a personal one.
func ValidateOwnedAccessToken(t *auth_model.AccessToken, numRepos int) error {
	if t.Scope == "" {
		return ErrOwnedTokenNoScope
	}
	if err := auth_model.ValidateOwnedTokenScope(t.Scope); err != nil {
		return err
	}
	if err := auth_model.ValidateOwnedTokenMode(t.Mode); err != nil {
		return err
	}
	if err := ValidateExpiry(setting.AccessTokenExpiry, t.ExpiresUnix); err != nil {
		return err
	}
	if err := ValidateScopePolicy(&user_model.User{}, t.Scope, t.ReachesAllRepos()); err != nil {
		return err
	}
	return ValidateRepositoryResource(t.ResourceAllRepos, t.Scope, numRepos)
}

// ErrOwnedTokenNameTaken refuses a second token of the same owner with the same name.
var (
	ErrOwnedTokenNameTaken    = errors.New("access token name has been used already")
	ErrOwnedTokenRepoNotInOrg = errors.New("a selected repository is not a repository of the organization")
)

// CreateOwnedAccessToken checks and stores a new token of an organization, a project or a repository (t names the
// owner), covering repoIDs when it is an organization token limited to selected repositories. Callers check who may
// create it; the web pages and the API share this.
func CreateOwnedAccessToken(ctx context.Context, t *auth_model.AccessToken, repoIDs []int64) error {
	exist, err := db.Exist[auth_model.AccessToken](ctx, auth_model.FindOwnedAccessTokensOptions{
		OwnerType: t.OwnerType, OwnerID: t.OwnerID, GroupID: t.GroupID, Name: t.Name,
	}.ToConds())
	if err != nil {
		return err
	} else if exist {
		return ErrOwnedTokenNameTaken
	}
	slices.Sort(repoIDs)
	repoIDs = slices.Compact(repoIDs)
	if err := ValidateOwnedAccessToken(t, len(repoIDs)); err != nil {
		return err
	}
	if len(repoIDs) > 0 {
		// only an organization token selects repositories, and only live ones of the organization
		n, err := db.GetEngine(ctx).Table("repository").In("id", repoIDs).
			And("owner_id = ? AND trashed_unix = 0", t.OwnerID).Count()
		if err != nil {
			return err
		} else if t.OwnerType != auth_model.AccessTokenOwnerOrg || n != int64(len(repoIDs)) {
			return ErrOwnedTokenRepoNotInOrg
		}
	}
	resources := make([]*auth_model.AccessTokenResourceRepo, 0, len(repoIDs))
	for _, id := range repoIDs {
		resources = append(resources, &auth_model.AccessTokenResourceRepo{RepoID: id})
	}
	return auth_model.NewAccessToken(ctx, t, resources)
}

var (
	ErrSpecifiedReposNone         = errors.New("specified repository access token: must have at least one repository")
	ErrSpecifiedReposNoPublicOnly = errors.New("specified repository access token: cannot be combined with public-only scope")
	ErrSpecifiedReposInvalidScope = errors.New("specified repository access token: invalid scope")
)

func ValidateRepositoryResource(resourceAllRepos bool, scope auth_model.AccessTokenScope, numRepoResources int) error {
	// Access tokens with broad access to all resources don't have any relevant validation rules to apply.
	if resourceAllRepos {
		return nil
	}

	// Repo-specific access token must have at least one repository.
	if numRepoResources == 0 {
		return ErrSpecifiedReposNone
	}

	// Can't have public-only and specified repos -- that's a combination that doesn't make sense.
	if publicOnly, err := scope.PublicOnly(); err != nil {
		return err
	} else if publicOnly {
		return ErrSpecifiedReposNoPublicOnly
	}

	// Repo-specific access tokens are only effective at restricting permissions if they are limited to the scopes that
	// support repositories as a resource.  For example, if you had a repo-specific token but then gave it
	// `write:organization`, it would be able to do operations like delete an organization -- permission checks on the
	// repository resources wouldn't be applicable to the organization resources.
	for _, scope := range scope.StringSlice() {
		switch auth_model.AccessTokenScope(scope) {
		case auth_model.AccessTokenScopeReadIssue,
			auth_model.AccessTokenScopeWriteIssue,
			auth_model.AccessTokenScopeReadRepository,
			auth_model.AccessTokenScopeWriteRepository:
			continue
		default:
			return fmt.Errorf("%w: cannot be combined with scope %s", ErrSpecifiedReposInvalidScope, scope)
		}
	}

	return nil
}
