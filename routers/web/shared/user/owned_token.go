// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package user

import (
	"errors"
	"slices"
	"strings"
	"time"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	"forgejo.org/models/perm"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
	"forgejo.org/services/authz"
	"forgejo.org/services/context"
)

// ownedTokensPageSize is how many tokens the Keys and tokens page lists at once; above it the list pages and a
// name filter shows.
const ownedTokensPageSize = 20

// OwnedTokenScopeCategories are the scope categories a token of an organization, a project or a repository may hold,
// in the order the form offers them; organization offers read only.
var OwnedTokenScopeCategories = []string{"repository", "issue", "package", "organization"}

// OwnedTokenRow is one token in the list of the Keys and tokens page.
type OwnedTokenRow struct {
	Token       *auth_model.AccessToken
	CreatedBy   *user_model.User
	Repos       []*repo_model.Repository // the selected repositories of an organization token
	ExpiresSoon bool                     // expires within 7 days
}

// OwnedTokenLevel names the owner of the tokens a Keys and tokens page manages.
type OwnedTokenLevel struct {
	Opts     auth_model.FindOwnedAccessTokensOptions // OwnerType, OwnerID and GroupID
	Link     string                                  // the Keys and tokens page
	CloneURL string                                  // shown in the usage of a new token
}

func newOwnedTokenRows(ctx *context.Context, tokens []*auth_model.AccessToken) ([]*OwnedTokenRow, error) {
	creatorIDs := make([]int64, 0, len(tokens))
	for _, t := range tokens {
		creatorIDs = append(creatorIDs, t.CreatorID)
	}
	creators, err := user_model.GetPossibleUserByIDs(ctx, creatorIDs)
	if err != nil {
		return nil, err
	}
	reposByToken, err := repo_model.BulkGetRepositoriesForAccessTokens(ctx, tokens, nil)
	if err != nil {
		return nil, err
	}
	soon := timeutil.TimeStampNow().AddDuration(7 * 24 * time.Hour)
	rows := make([]*OwnedTokenRow, 0, len(tokens))
	for _, t := range tokens {
		row := &OwnedTokenRow{Token: t, Repos: reposByToken[t.ID], ExpiresSoon: t.ExpiresUnix != 0 && !t.IsExpired() && t.ExpiresUnix <= soon}
		if i := slices.IndexFunc(creators, func(u *user_model.User) bool { return u.ID == t.CreatorID }); i >= 0 {
			row.CreatedBy = creators[i]
		} else {
			row.CreatedBy = user_model.NewGhostUser()
		}
		slices.SortFunc(row.Repos, func(a, b *repo_model.Repository) int { return strings.Compare(a.FullName(), b.FullName()) })
		rows = append(rows, row)
	}
	return rows, nil
}

// LoadOwnedTokens sets the page data of the access tokens section: one page of the owner's tokens (newest first,
// filtered by the q parameter) and the create form's defaults.
func LoadOwnedTokens(ctx *context.Context, level OwnedTokenLevel) bool {
	opts := level.Opts
	opts.Keyword = strings.TrimSpace(ctx.FormString("q"))
	opts.ListOptions = db.ListOptions{Page: max(ctx.FormInt("page"), 1), PageSize: ownedTokensPageSize}
	tokens, count, err := db.FindAndCount[auth_model.AccessToken](ctx, opts)
	if err != nil {
		ctx.ServerError("FindOwnedAccessTokens", err)
		return false
	}
	rows, err := newOwnedTokenRows(ctx, tokens)
	if err != nil {
		ctx.ServerError("newOwnedTokenRows", err)
		return false
	}
	ctx.Data["Link"] = level.Link
	ctx.Data["OwnedTokens"] = rows
	ctx.Data["OwnedTokenCloneURL"] = level.CloneURL
	ctx.Data["OwnedTokensLink"] = level.Link + "/tokens"
	ctx.Data["OwnedTokensKeyword"] = opts.Keyword
	ctx.Data["OwnedTokenLevel"] = int(level.Opts.OwnerType)
	ctx.Data["OwnedTokenScopeCategories"] = OwnedTokenScopeCategories
	if count > ownedTokensPageSize || opts.Keyword != "" {
		ctx.Data["OwnedTokensFilter"] = true
	}
	pager := context.NewPagination(int(count), ownedTokensPageSize, opts.Page, 5)
	pager.AddParamString("q", opts.Keyword)
	ctx.Data["Page"] = pager
	if _, ok := ctx.Data["token_permission"]; !ok { // the defaults of a new form
		ctx.Data["token_permission"] = "read"
		ctx.Data["token_scopes"] = []string{"read:repository"}
		ctx.Data["TokenScopeAll"] = true
	}
	ctx.Data["TokenExpiryRequired"] = setting.AccessTokenExpiry.Required || setting.AccessTokenExpiry.MaxLifetimeDays > 0
	ctx.Data["TokenExpiryMaxDate"] = authz.MaxExpiryDate(setting.AccessTokenExpiry)
	ctx.Data["TokenExpiryMaxDays"] = setting.AccessTokenExpiry.MaxLifetimeDays
	if v, _ := ctx.Data["token_expires_at"].(string); v == "" {
		ctx.Data["token_expires_at"] = authz.DefaultExpiryDate(setting.AccessTokenExpiry)
	}
	return true
}

// LoadCoveringTokens sets the organization and project tokens that cover the repository of the page, for the
// read-only list on its Keys and tokens page.
func LoadCoveringTokens(ctx *context.Context, repo *repo_model.Repository) bool {
	if !repo.Owner.IsOrganization() {
		return true
	}
	tokens, err := auth_model.FindOwnedAccessTokensCoveringRepo(ctx, repo.ID, repo.OwnerID, repo.GroupID)
	if err != nil {
		ctx.ServerError("FindOwnedAccessTokensCoveringRepo", err)
		return false
	}
	ctx.Data["CoveringTokens"] = tokens
	return true
}

// OwnedTokenForm is what the create form posted, kept to show it again after a refusal.
type OwnedTokenForm struct {
	Name       string
	Permission string
	Scopes     []string
	ExpiresAt  string
}

// ReadOwnedTokenForm reads the create form and puts it back into the page data.
func ReadOwnedTokenForm(ctx *context.Context) OwnedTokenForm {
	f := OwnedTokenForm{
		Name:       strings.TrimSpace(ctx.FormString("token_name")),
		Permission: ctx.FormString("token_permission"),
		Scopes:     slices.DeleteFunc(ctx.Req.Form["token_scopes"], func(s string) bool { return s == "" }),
		ExpiresAt:  ctx.FormString("token_expires_at"),
	}
	ctx.Data["token_name"] = f.Name
	ctx.Data["token_permission"] = f.Permission
	ctx.Data["token_scopes"] = f.Scopes
	ctx.Data["token_expires_at"] = f.ExpiresAt
	ctx.Data["HasTokenError"] = true // keeps the form open until it succeeds
	return f
}

// CreateOwnedToken creates the token the form describes for level, covering repoIDs for an organization token
// limited to selected repositories (allRepos false). On success it returns the token, whose secret the caller shows
// once (NewOwnedToken); on a refusal it sets TokenError and returns nil. Only the page's own owners reach it (the routes check).
func CreateOwnedToken(ctx *context.Context, level OwnedTokenLevel, f OwnedTokenForm, allRepos bool, repoIDs []int64) *auth_model.AccessToken {
	refuse := func(msg string) *auth_model.AccessToken {
		ctx.Data["TokenError"] = msg
		return nil
	}
	if f.Name == "" || len(f.Name) > 255 {
		ctx.Data["Err_TokenName"] = true
		return refuse(ctx.Locale.TrString("owned_token.error.name"))
	}
	mode := perm.ParseAccessMode(f.Permission)
	if f.Permission != "read" && f.Permission != "write" && f.Permission != "admin" {
		return refuse(ctx.Locale.TrString("owned_token.error.permission"))
	}
	scope, err := auth_model.AccessTokenScope(strings.Join(f.Scopes, ",")).Normalize()
	if err != nil || scope == "" {
		ctx.Data["Err_TokenScopes"] = true
		return refuse(ctx.Locale.TrString("settings.at_least_one_permission"))
	}
	expires, expiryErr := ParseExpiryForm(ctx.Base, setting.AccessTokenExpiry, f.ExpiresAt)
	if has, msg := expiryErr.Get(); has {
		ctx.Data["Err_TokenExpiresAt"] = true
		return refuse(msg)
	}
	if level.Opts.OwnerType != auth_model.AccessTokenOwnerOrg {
		allRepos, repoIDs = true, nil
	}
	t := &auth_model.AccessToken{
		Name:             f.Name,
		Scope:            scope,
		OwnerType:        level.Opts.OwnerType,
		OwnerID:          level.Opts.OwnerID,
		GroupID:          level.Opts.GroupID,
		Mode:             mode,
		CreatorID:        ctx.Doer.ID,
		ExpiresUnix:      expires,
		ResourceAllRepos: allRepos,
	}
	if err := authz.CreateOwnedAccessToken(ctx, t, repoIDs); err != nil {
		switch {
		case errors.Is(err, authz.ErrOwnedTokenNameTaken):
			ctx.Data["Err_TokenName"] = true
			return refuse(ctx.Locale.TrString("owned_token.error.name_taken", f.Name))
		case errors.Is(err, authz.ErrOwnedTokenNoScope):
			ctx.Data["Err_TokenScopes"] = true
			return refuse(ctx.Locale.TrString("settings.at_least_one_permission"))
		case errors.Is(err, auth_model.ErrOwnedTokenScope):
			ctx.Data["Err_TokenScopes"] = true
			return refuse(ctx.Locale.TrString("owned_token.error.scope"))
		case errors.Is(err, authz.ErrSpecifiedReposNone):
			return refuse(ctx.Locale.TrString("org.settings.deploy_key_no_repos"))
		case errors.Is(err, authz.ErrOwnedTokenRepoNotInOrg):
			return refuse(ctx.Locale.TrString("org.settings.deploy_key_invalid_repos"))
		}
		if has, msg := TranslateAccessTokenValidationError(ctx.Base, err).Get(); has {
			return refuse(msg)
		}
		ctx.ServerError("CreateOwnedAccessToken", err)
		return nil
	}
	// the value is shown once, in this answer; the form starts over
	for _, k := range []string{"HasTokenError", "token_name", "token_permission", "token_scopes", "token_expires_at", "TokenScopeAll", "TokenScopeRepoIDs"} {
		delete(ctx.Data, k)
	}
	ctx.Data["NewOwnedToken"] = t
	return t
}

// DeleteOwnedToken revokes the token of the form's id of level; a token of another owner is not found.
func DeleteOwnedToken(ctx *context.Context, level OwnedTokenLevel) {
	if err := auth_model.DeleteOwnedAccessToken(ctx, level.Opts, ctx.FormInt64("id")); err != nil {
		if auth_model.IsErrAccessTokenNotExist(err) {
			ctx.Flash.Error(ctx.Tr("error.not_found"))
		} else {
			ctx.ServerError("DeleteOwnedAccessToken", err)
			return
		}
	} else {
		ctx.Flash.Success(ctx.Tr("owned_token.revoked"))
	}
	ctx.JSONRedirect(level.Link)
}
