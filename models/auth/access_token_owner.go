// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"fmt"

	"forgejo.org/models/db"
	"forgejo.org/models/perm"

	"xorm.io/builder"
)

// AccessTokenOwnerType is what an access token belongs to: a user (personal access token), or an organization, a
// project or a repository (a token that acts as itself, not as a person).
type AccessTokenOwnerType int

const (
	AccessTokenOwnerUser    AccessTokenOwnerType = iota // UID is the user
	AccessTokenOwnerOrg                                 // OwnerID is the organization
	AccessTokenOwnerProject                             // OwnerID is the organization, GroupID the project
	AccessTokenOwnerRepo                                // OwnerID is the repository
)

// IsOwnedByUser reports whether the token is a personal access token; any other token has no user (UID is 0).
func (t *AccessToken) IsOwnedByUser() bool {
	return t.OwnerType == AccessTokenOwnerUser
}

// ErrOwnedTokenScope is the refusal of a scope that a token of an organization, a project or a repository can't have.
var ErrOwnedTokenScope = errors.New("scope not allowed for an organization, project or repository token")

// ownedTokenScopeBits are the scopes a token that isn't a person's may hold; anything about a user, the instance or
// writing an organization is refused when it is created and when it is used.
const ownedTokenScopeBits = accessTokenScopeWriteRepositoryBits | accessTokenScopeWriteIssueBits |
	accessTokenScopeWritePackageBits | accessTokenScopeReadOrganizationBits

// ValidateOwnedTokenScope returns ErrOwnedTokenScope unless every scope is allowed for an organization, project or
// repository token. An empty scope grants no API access and is allowed.
func ValidateOwnedTokenScope(scope AccessTokenScope) error {
	bitmap, err := scope.parse()
	if err != nil {
		return err
	}
	if bitmap&^ownedTokenScopeBits != 0 {
		return ErrOwnedTokenScope
	}
	return nil
}

// ValidateOwnedTokenMode returns an error unless mode is read, write or admin.
func ValidateOwnedTokenMode(mode perm.AccessMode) error {
	if mode < perm.AccessModeRead || mode > perm.AccessModeAdmin {
		return fmt.Errorf("invalid access token permission: %v", mode)
	}
	return nil
}

// CoversRepo reports whether a token of an organization, a project or a repository applies to the repository with
// the given ID, owner and project (0 = at the owner's root), as they are now: a repository token to its repository
// only, a project token to every repository in the project, an organization token to every repository of the
// organization or only to its selected ones. A personal access token covers nothing here.
func (t *AccessToken) CoversRepo(ctx context.Context, repoID, ownerID, groupID int64) (bool, error) {
	switch t.OwnerType {
	case AccessTokenOwnerRepo:
		return t.OwnerID == repoID, nil
	case AccessTokenOwnerProject:
		return t.OwnerID == ownerID && t.GroupID != 0 && t.GroupID == groupID, nil
	case AccessTokenOwnerOrg:
		if t.OwnerID != ownerID {
			return false, nil
		}
		if t.ResourceAllRepos {
			return true, nil
		}
		return db.GetEngine(ctx).Table("access_token_resource_repo").Where("token_id = ? AND repo_id = ?", t.ID, repoID).Exist()
	default:
		return false, nil
	}
}

// GetOwnedAccessTokenByID returns the token of an organization, a project or a repository with the ID.
func GetOwnedAccessTokenByID(ctx context.Context, id int64) (*AccessToken, error) {
	t := &AccessToken{}
	has, err := db.GetEngine(ctx).Where("id = ? AND owner_type <> ?", id, AccessTokenOwnerUser).Get(t)
	if err != nil {
		return nil, err
	} else if !has {
		return nil, ErrAccessTokenNotExist{}
	}
	return t, nil
}

// DeleteOwnedAccessTokens deletes the tokens of the owner: with AccessTokenOwnerOrg the organization's and its
// projects', with AccessTokenOwnerProject one project's (groupID), with AccessTokenOwnerRepo one repository's.
func DeleteOwnedAccessTokens(ctx context.Context, ownerType AccessTokenOwnerType, ownerID, groupID int64) error {
	var cond builder.Cond
	switch ownerType {
	case AccessTokenOwnerOrg:
		cond = builder.In("owner_type", AccessTokenOwnerOrg, AccessTokenOwnerProject).And(builder.Eq{"owner_id": ownerID})
	case AccessTokenOwnerProject:
		cond = builder.Eq{"owner_type": AccessTokenOwnerProject, "owner_id": ownerID, "group_id": groupID}
	case AccessTokenOwnerRepo:
		cond = builder.Eq{"owner_type": AccessTokenOwnerRepo, "owner_id": ownerID}
	default:
		return fmt.Errorf("DeleteOwnedAccessTokens: invalid owner type %d", ownerType)
	}
	return db.WithTx(ctx, func(ctx context.Context) error {
		if _, err := db.GetEngine(ctx).Where(builder.In("token_id", builder.Select("id").From("access_token").Where(cond))).Delete(&AccessTokenResourceRepo{}); err != nil {
			return err
		}
		_, err := db.GetEngine(ctx).Where(cond).Delete(&AccessToken{})
		return err
	})
}

// DropOwnedAccessTokensOfTransferredRepo deletes the repository's own tokens and the repository from the selections of
// organization tokens, for a repository leaving its owner: moving it back must not restore them.
func DropOwnedAccessTokensOfTransferredRepo(ctx context.Context, repoID int64) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		ownedTokens := builder.Select("id").From("access_token").Where(builder.Neq{"owner_type": AccessTokenOwnerUser})
		if _, err := db.GetEngine(ctx).Where(builder.Eq{"repo_id": repoID}.And(builder.In("token_id", ownedTokens))).Delete(&AccessTokenResourceRepo{}); err != nil {
			return err
		}
		return DeleteOwnedAccessTokens(ctx, AccessTokenOwnerRepo, repoID, 0)
	})
}
