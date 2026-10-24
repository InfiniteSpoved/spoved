// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2024 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package user

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"forgejo.org/models/auth"
	"forgejo.org/models/db"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/structs"
)

// IsSystem returns true if the user has a fixed
// negative ID, is never stored in the database and
// is generated on the fly when needed.
func (u *User) IsSystem() bool {
	return u.IsGhost() || u.IsActions() || u.IsAccessTokenBot()
}

const (
	GhostUserID        = -1
	GhostUserName      = "Ghost"
	GhostUserLowerName = "ghost"
)

// NewGhostUser creates and returns a fake user for someone has deleted their account.
func NewGhostUser() *User {
	return &User{
		ID:        GhostUserID,
		Name:      GhostUserName,
		LowerName: GhostUserLowerName,
	}
}

// IsGhost check if user is fake user for a deleted account
func (u *User) IsGhost() bool {
	if u == nil {
		return false
	}
	return u.ID == GhostUserID && u.Name == GhostUserName
}

// NewReplaceUser creates and returns a fake user for external user
func NewReplaceUser(name string) *User {
	return &User{
		ID:        0,
		Name:      name,
		LowerName: strings.ToLower(name),
	}
}

const (
	ActionsUserID   = -2
	ActionsUserName = "forgejo-actions"
	ActionsFullName = "Forgejo Actions"
	ActionsEmail    = "noreply@forgejo.org"
)

// NewActionsUser creates and returns a fake user for running the actions.
func NewActionsUser() *User {
	return &User{
		ID:                      ActionsUserID,
		Name:                    ActionsUserName,
		LowerName:               ActionsUserName,
		IsActive:                true,
		FullName:                ActionsFullName,
		Email:                   ActionsEmail,
		KeepEmailPrivate:        true,
		LoginName:               ActionsUserName,
		Type:                    UserTypeIndividual,
		AllowCreateOrganization: true,
		Visibility:              structs.VisibleTypePublic,
	}
}

func (u *User) IsActions() bool {
	return u != nil && u.ID == ActionsUserID
}

const (
	APServerActorUserID   = -3
	APServerActorUserName = "actor"
	APServerActorEmail    = "noreply@forgejo.org"
)

func NewAPServerActor() *User {
	return &User{
		ID:               APServerActorUserID,
		Name:             APServerActorUserName,
		LowerName:        APServerActorUserName,
		IsActive:         true,
		Email:            APServerActorEmail,
		KeepEmailPrivate: true,
		LoginName:        APServerActorUserName,
		Type:             UserTypeIndividual,
		Visibility:       structs.VisibleTypePublic,
	}
}

func APServerActorID() string {
	path, _ := url.JoinPath(setting.AppURL, "/api/v1/activitypub/actor")
	return path
}

func (u *User) IsAPServerActor() bool {
	return u != nil && u.ID == APServerActorUserID
}

// accessTokenBotIDBase: the identity of the access token of an organization, a project or a repository has the user
// ID accessTokenBotIDBase - token ID, which is never stored as a user and far below the fixed system IDs above.
const accessTokenBotIDBase = -(1 << 32)

// AccessTokenBotID is the user ID of the identity of the token with the ID.
func AccessTokenBotID(tokenID int64) int64 {
	return accessTokenBotIDBase - tokenID
}

// IsAccessTokenBotID reports whether the user ID is the identity of an access token (see AccessTokenBotID).
func IsAccessTokenBotID(id int64) bool {
	return id < accessTokenBotIDBase
}

// WithoutTokenIDs drops the token identities from a user allow-list.
func WithoutTokenIDs(ids []int64) []int64 {
	return slices.DeleteFunc(slices.Clone(ids), IsAccessTokenBotID)
}

// TokenIDsOnly keeps the token identities of an allow-list.
func TokenIDsOnly(ids []int64) []int64 {
	return slices.DeleteFunc(slices.Clone(ids), func(id int64) bool { return !IsAccessTokenBotID(id) })
}

// NewAccessTokenBot returns the identity the token of an organization, a project or a repository acts as. It is not
// a user account: no login, no membership, no team, and its permission comes from the token only (BotToken).
func NewAccessTokenBot(t *auth.AccessToken) *User {
	// "+" is not allowed in user names, so neither the name nor the no-reply address can be a real user's
	name := fmt.Sprintf("token+%d", t.ID)
	return &User{
		ID:               AccessTokenBotID(t.ID),
		Name:             name,
		LowerName:        name,
		FullName:         t.Name + " [bot]",
		Email:            name + "@" + setting.Service.NoReplyAddress,
		KeepEmailPrivate: true,
		LoginName:        name,
		IsActive:         t.IsUsable(),
		Type:             UserTypeBot,
		Visibility:       structs.VisibleTypePublic,
		BotToken:         t,
	}
}

// GetAccessTokenBot returns the identity with the user ID (see AccessTokenBotID), also of a revoked token (for its
// name in history; it grants nothing), or ErrUserNotExist once its token was deleted with its owner.
func GetAccessTokenBot(ctx context.Context, id int64) (*User, error) {
	t, err := auth.GetOwnedAccessTokenByID(ctx, accessTokenBotIDBase-id)
	if auth.IsErrAccessTokenNotExist(err) {
		return nil, ErrUserNotExist{UID: id}
	} else if err != nil {
		return nil, err
	}
	return NewAccessTokenBot(t), nil
}

// GetAccessTokenBots returns the identities with the user IDs (see AccessTokenBotID) by user ID, in one query; the
// identities of deleted tokens are left out.
func GetAccessTokenBots(ctx context.Context, ids []int64) (map[int64]*User, error) {
	tokenIDs := make([]int64, 0, len(ids))
	for _, id := range ids {
		tokenIDs = append(tokenIDs, accessTokenBotIDBase-id)
	}
	tokens := make([]*auth.AccessToken, 0, len(tokenIDs))
	for chunk := range slices.Chunk(tokenIDs, db.DefaultMaxInSize) {
		if err := db.GetEngine(ctx).In("id", chunk).And("owner_type <> ?", auth.AccessTokenOwnerUser).Find(&tokens); err != nil {
			return nil, err
		}
	}
	bots := make(map[int64]*User, len(tokens))
	for _, t := range tokens {
		bots[AccessTokenBotID(t.ID)] = NewAccessTokenBot(t)
	}
	return bots, nil
}

// GetAccessTokenBotNames returns the token names of the identities with the user IDs (see AccessTokenBotID), in one
// query; the identities of deleted tokens are left out.
func GetAccessTokenBotNames(ctx context.Context, ids []int64) ([]string, error) {
	tokenIDs := make([]int64, 0, len(ids))
	for _, id := range ids {
		tokenIDs = append(tokenIDs, accessTokenBotIDBase-id)
	}
	var names []string
	err := db.GetEngine(ctx).Table("access_token").In("id", tokenIDs).Asc("id").Cols("name").Find(&names)
	return names, err
}

// IsAccessTokenBot reports whether the user is the identity of an organization, project or repository token.
func (u *User) IsAccessTokenBot() bool {
	return u != nil && u.BotToken != nil
}
