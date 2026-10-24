// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package permissions

import (
	"context"
	"net/http"
	"slices"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/perm"
	access_model "forgejo.org/models/perm/access"
	user_model "forgejo.org/models/user"
	"forgejo.org/services/auth"

	"github.com/go-chi/chi/v5"
)

func RepoAccess(ctx Context) {
	if ctx.Doer() != nil && ctx.Doer().ID == user_model.ActionsUserID && ctx.Authentication().ActionsTaskID().Has() {
		_, taskID := ctx.Authentication().ActionsTaskID().Get()
		task, err := actions_model.GetTaskByID(ctx.Context(), taskID)
		if err != nil {
			ctx.Error(http.StatusInternalServerError, "actions_model.GetTaskByID", err)
			return
		}
		if task.RepoID != ctx.Repository().ID {
			ctx.NotFound()
			return
		}

		permission, err := access_model.GetActionRepoPermission(ctx.Context(), ctx.Repository(), task)
		if err != nil {
			ctx.Error(http.StatusInternalServerError, "GetActionRepoPermission", err)
			return
		}
		if task.TokenPermissions != "" && !ctx.IsReadOnlyMethod() {
			dropReadOnlyUnits(&permission)
		}
		ctx.SetPermission(&permission)
	} else {
		getPermission := access_model.GetUserRepoPermissionWithReducer
		if ctx.IsReadOnlyMethod() && auth.IsHeldByUser(ctx.Authentication()) {
			getPermission = access_model.GetUserRepoReadPermission
		}
		permission, err := getPermission(ctx.Context(), ctx.Repository(), ctx.Doer(), ctx.Reducer())
		if err != nil {
			ctx.Error(http.StatusInternalServerError, "GetUserRepoPermissionWithReducer", err)
			return
		}
		// a read token of an organization, a project or a repository never writes
		if ctx.Doer().IsAccessTokenBot() && !ctx.IsReadOnlyMethod() {
			dropReadOnlyUnits(&permission)
		}
		ctx.SetPermission(&permission)
	}

	if !ctx.Permission().HasAccess() {
		// the request's audit actor lives in the API context itself, not in Context()
		auditCtx := ctx.Context()
		if c, ok := ctx.(context.Context); ok {
			auditCtx = c
		}
		auth.RecordTwoFactorBlockedMember(auditCtx, ctx.Doer(), ctx.Repository().Owner)
		ctx.NotFound()
		return
	}

	// Every repository of an archived organization is read-only, whatever the repository's own settings say.
	if owner := ctx.Repository().Owner; !ctx.IsReadOnlyMethod() && owner.IsOrganization() && owner.IsArchived && !isArchivedOrgOpenRoute(ctx) {
		ctx.Error(http.StatusForbidden, "OrgArchived", "the organization is archived and read-only")
	}
}

// dropReadOnlyUnits removes the units the permission only reads, for a write request of a token that writes only
// where it was granted write: creating an issue or a comment only needs read.
func dropReadOnlyUnits(permission *access_model.Permission) {
	units := permission.Units
	permission.Units = nil
	for _, u := range units {
		if permission.UnitsMode[u.Type] < perm.AccessModeWrite {
			delete(permission.UnitsMode, u.Type)
		} else {
			permission.Units = append(permission.Units, u)
		}
	}
}

// CredentialRevokeRoutes revoke access tokens, on the web and in the API. They stay open while an organization is
// archived (context.ArchivedOrgOpenRoutes) and in read-only maintenance mode (common.MaintenanceRevokeRoutes):
// taking a leaked credential away must always be possible. They are kept here, the lowest package that needs them.
var CredentialRevokeRoutes = []string{
	"POST /user/settings/keys/tokens/delete",
	"POST /org/{org}/settings/keys/tokens/delete",
	"POST /org/{org}/groups/{id}/settings/keys/tokens/delete",
	"POST /{username}/{reponame}/settings/keys/tokens/delete",
	"DELETE /api/v1/users/{username}/tokens/{id}",
	"DELETE /api/v1/orgs/{org}/tokens/{id}",
	"DELETE /api/v1/orgs/{org}/projects/{id}/tokens/{token_id}",
	"DELETE /api/v1/repos/{username}/{reponame}/tokens/{id}",
	"DELETE /api/v1/repos/{username}/{groupname}/{reponame}/-/tokens/{id}",
	"DELETE /api/v1/repositories/{repo_id}/tokens/{id}",
}

// isArchivedOrgOpenRoute reports whether the request's route stays writable in an archived organization (revoking a
// token). Every route is a DELETE there, so the pattern decides.
func isArchivedOrgOpenRoute(ctx Context) bool {
	rctx := chi.RouteContext(ctx.Context())
	return rctx != nil && slices.Contains(CredentialRevokeRoutes, http.MethodDelete+" "+rctx.RoutePattern())
}
