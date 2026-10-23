// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package utils

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	audit_model "forgejo.org/models/audit"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	"forgejo.org/models/perm"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/web"
	auth_service "forgejo.org/services/auth"
	"forgejo.org/services/authz"
	"forgejo.org/services/context"
	"forgejo.org/services/convert"
)

// OwnedTokenOwner names the organization, project or repository whose access tokens a request manages.
type OwnedTokenOwner struct {
	Type    auth_model.AccessTokenOwnerType
	ID      int64  // the organization (also of a project), or the repository
	GroupID int64  // the project
	OrgName string // the organization, to find the selected repositories of an organization token
}

func (o OwnedTokenOwner) opts() auth_model.FindOwnedAccessTokensOptions {
	return auth_model.FindOwnedAccessTokensOptions{OwnerType: o.Type, OwnerID: o.ID, GroupID: o.GroupID}
}

// maxOwnedTokenRepos caps the repositories an organization token selects in one request.
const maxOwnedTokenRepos = 1000

// mayManageOwnedTokens refuses what must not manage credentials: a token of an organization, a project or a
// repository, the Actions user, a token limited to public resources, a credential another party holds for the user
// (an OAuth2 grant or an authorized integration), and an HTTP signature (an SSH key or certificate, whose expiry the
// request doesn't carry); these could otherwise mint a token that outlives them.
func mayManageOwnedTokens(ctx *context.APIContext) bool {
	if ctx.Doer().IsAccessTokenBot() || ctx.Doer().IsActions() || ctx.PublicOnly() || !auth_service.IsHeldByUser(ctx.Authentication()) ||
		ctx.Authentication().Credential().Type == audit_model.CredentialSSHKey {
		ctx.Error(http.StatusForbidden, "", "this credential can't manage access tokens")
		return false
	}
	return true
}

func toOwnedAccessToken(t *auth_model.AccessToken, createdBy string, repos []*repo_model.Repository) *api.AccessToken {
	out := &api.AccessToken{
		ID:             t.ID,
		Name:           t.Name,
		Token:          t.Token,
		TokenLastEight: t.TokenLastEight,
		Scopes:         t.Scope.StringSlice(),
		Created:        t.CreatedUnix.AsTime(),
		Expires:        convert.OptionalTime(t.ExpiresUnix),
		Permission:     t.Mode.String(),
		CreatedBy:      createdBy,
	}
	slices.SortFunc(repos, func(a, b *repo_model.Repository) int { return cmp.Compare(a.ID, b.ID) })
	for _, repo := range repos {
		out.Repositories = append(out.Repositories, &api.RepositoryMeta{
			ID:       repo.ID,
			Name:     repo.Name,
			Owner:    repo.OwnerName,
			FullName: repo.FullName(),
		})
	}
	return out
}

// ListOwnedAccessTokens lists the access tokens of an organization, a project or a repository, never their values.
func ListOwnedAccessTokens(ctx *context.APIContext, owner OwnedTokenOwner) {
	if !mayManageOwnedTokens(ctx) {
		return
	}
	opts := owner.opts()
	opts.ListOptions = GetListOptions(ctx)
	tokens, count, err := db.FindAndCount[auth_model.AccessToken](ctx, opts)
	if err != nil {
		ctx.InternalServerError(err)
		return
	}
	// the selections of an organization token are repositories of the organization: transfers drop them
	reposByTokenID, err := repo_model.BulkGetRepositoriesForAccessTokens(ctx, tokens, nil)
	if err != nil {
		ctx.InternalServerError(err)
		return
	}
	creatorIDs := make([]int64, 0, len(tokens))
	for _, t := range tokens {
		creatorIDs = append(creatorIDs, t.CreatorID)
	}
	creators, err := user_model.GetPossibleUserByIDs(ctx, creatorIDs)
	if err != nil {
		ctx.InternalServerError(err)
		return
	}
	creatorNames := make(map[int64]string, len(creators))
	for _, u := range creators {
		creatorNames[u.ID] = u.Name
	}

	apiTokens := make([]*api.AccessToken, 0, len(tokens))
	for _, t := range tokens {
		t.Token = "" // never shown again
		createdBy, ok := creatorNames[t.CreatorID]
		if !ok {
			createdBy = user_model.NewGhostUser().Name
		}
		apiTokens = append(apiTokens, toOwnedAccessToken(t, createdBy, reposByTokenID[t.ID]))
	}
	ctx.SetTotalCountHeader(count)
	ctx.JSON(http.StatusOK, &apiTokens)
}

// ownedTokenRepos resolves the repositories an organization token selects ("repo" or "project/repo"); a name outside
// the organization is refused.
func ownedTokenRepos(ctx *context.APIContext, orgName string, names []string) ([]*repo_model.Repository, bool) {
	if len(names) > maxOwnedTokenRepos {
		ctx.Error(http.StatusUnprocessableEntity, "", fmt.Sprintf("at most %d repositories", maxOwnedTokenRepos))
		return nil, false
	}
	repos := make([]*repo_model.Repository, 0, len(names))
	for _, name := range names {
		project, leaf, ok := strings.Cut(name, "/")
		if !ok {
			project, leaf = "", name
		}
		repo, err := repo_model.GetRepositoryByRef(ctx, orgName, project, leaf)
		if err != nil {
			if repo_model.IsErrRepoNotExist(err) {
				ctx.Error(http.StatusUnprocessableEntity, "", "repository not found in the organization: "+name)
			} else {
				ctx.Error(http.StatusInternalServerError, "GetRepositoryByRef", err)
			}
			return nil, false
		}
		if !slices.ContainsFunc(repos, func(r *repo_model.Repository) bool { return r.ID == repo.ID }) {
			repos = append(repos, repo)
		}
	}
	return repos, true
}

// CreateOwnedAccessToken creates an access token of an organization, a project or a repository. A token creating it
// must hold every scope it grants and must not expire before it; the new token's value is shown only in this answer.
func CreateOwnedAccessToken(ctx *context.APIContext, owner OwnedTokenOwner) {
	if !mayManageOwnedTokens(ctx) {
		return
	}
	form := web.GetForm(ctx).(*api.CreateOwnedAccessTokenOption)

	scope, err := auth_model.AccessTokenScope(strings.Join(form.Scopes, ",")).Normalize()
	if err != nil {
		ctx.Error(http.StatusUnprocessableEntity, "", fmt.Sprintf("invalid access token scope provided: %v", err))
		return
	}
	t := &auth_model.AccessToken{
		Name:             form.Name,
		Scope:            scope,
		OwnerType:        owner.Type,
		OwnerID:          owner.ID,
		GroupID:          owner.GroupID,
		Mode:             perm.ParseAccessMode(form.Permission),
		CreatorID:        ctx.Doer().ID,
		ExpiresUnix:      ExpiryTimeStamp(form.ExpiresAt),
		ResourceAllRepos: true,
	}

	var repos []*repo_model.Repository
	if form.Repositories != nil {
		if owner.Type != auth_model.AccessTokenOwnerOrg {
			ctx.Error(http.StatusUnprocessableEntity, "", "only an organization token selects repositories")
			return
		}
		var ok bool
		if repos, ok = ownedTokenRepos(ctx, owner.OrgName, form.Repositories); !ok {
			return
		}
		t.ResourceAllRepos = false
	}

	opts := owner.opts()
	opts.Name = t.Name
	if exist, err := db.Exist[auth_model.AccessToken](ctx, opts.ToConds()); err != nil {
		ctx.InternalServerError(err)
		return
	} else if exist {
		ctx.Error(http.StatusUnprocessableEntity, "", "access token name has been used already")
		return
	}

	if err := authz.ValidateOwnedAccessToken(t, len(repos)); err != nil {
		if IsExpiryError(err) || errors.Is(err, authz.ErrScopeNotAllowed) || errors.Is(err, authz.ErrScopeNeedsRepoRestriction) ||
			errors.Is(err, authz.ErrOwnedTokenNoScope) || errors.Is(err, auth_model.ErrOwnedTokenScope) ||
			errors.Is(err, authz.ErrSpecifiedReposNone) || errors.Is(err, authz.ErrSpecifiedReposNoPublicOnly) ||
			errors.Is(err, authz.ErrSpecifiedReposInvalidScope) {
			ctx.Error(http.StatusUnprocessableEntity, "", err.Error())
			return
		}
		ctx.InternalServerError(err)
		return
	}

	if has, reqScope := ctx.Authentication().Scope().Get(); has {
		var granted []auth_model.AccessTokenScope
		for _, s := range scope.StringSlice() {
			granted = append(granted, auth_model.AccessTokenScope(s))
		}
		if ok, err := reqScope.HasScope(granted...); err != nil {
			ctx.InternalServerError(err)
			return
		} else if !ok {
			ctx.Error(http.StatusForbidden, "", "the token must hold every scope it grants")
			return
		}
	}
	if has, expires := ctx.Authentication().ExpiresAt().Get(); has && (t.ExpiresUnix == 0 || t.ExpiresUnix > expires) {
		ctx.Error(http.StatusUnprocessableEntity, "", "the token must not expire after the credential that creates it")
		return
	}

	resources := make([]*auth_model.AccessTokenResourceRepo, 0, len(repos))
	for _, repo := range repos {
		resources = append(resources, &auth_model.AccessTokenResourceRepo{RepoID: repo.ID})
	}
	if err := auth_model.NewAccessToken(ctx, t, resources); err != nil {
		ctx.InternalServerError(err)
		return
	}
	ctx.JSON(http.StatusCreated, toOwnedAccessToken(t, ctx.Doer().Name, repos))
}

// DeleteOwnedAccessToken deletes (revokes) an access token of an organization, a project or a repository; a token of
// another owner is not found.
func DeleteOwnedAccessToken(ctx *context.APIContext, owner OwnedTokenOwner, id int64) {
	if !mayManageOwnedTokens(ctx) {
		return
	}
	if err := auth_model.DeleteOwnedAccessToken(ctx, owner.opts(), id); err != nil {
		if auth_model.IsErrAccessTokenNotExist(err) {
			ctx.NotFound()
		} else {
			ctx.InternalServerError(err)
		}
		return
	}
	ctx.Status(http.StatusNoContent)
}
