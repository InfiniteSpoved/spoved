// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package permissions

import (
	"net/http"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/perm"
	access_model "forgejo.org/models/perm/access"
	user_model "forgejo.org/models/user"
	"forgejo.org/services/auth"
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
		// Creating an issue or a comment only needs read: a token with permissions writes only where they grant write.
		if task.TokenPermissions != "" && !ctx.IsReadOnlyMethod() {
			for t, mode := range permission.UnitsMode {
				if mode < perm.AccessModeWrite {
					delete(permission.UnitsMode, t)
				}
			}
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
		ctx.SetPermission(&permission)
	}

	if !ctx.Permission().HasAccess() {
		ctx.NotFound()
		return
	}

	// Every repository of an archived organization is read-only, whatever the repository's own settings say.
	if owner := ctx.Repository().Owner; !ctx.IsReadOnlyMethod() && owner.IsOrganization() && owner.IsArchived {
		ctx.Error(http.StatusForbidden, "OrgArchived", "the organization is archived and read-only")
	}
}
