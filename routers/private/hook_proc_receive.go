// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package private

import (
	"errors"
	"net/http"

	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/log"
	"forgejo.org/modules/private"
	"forgejo.org/modules/web"
	"forgejo.org/services/agit"
	app_context "forgejo.org/services/context"
)

// HookProcReceive proc-receive hook - only handles agit Proc-Receive requests at present
func HookProcReceive(ctx *app_context.PrivateContext) {
	opts := web.GetForm(ctx).(*private.HookOptions)

	results, err := agit.ProcReceive(ctx, ctx.Repo.Repository, ctx.Repo.GitRepo, opts)
	if err != nil {
		if errors.Is(err, agit.ErrPRTitleRefused) {
			recordPushRefused(ctx, opts, "push_rule", "")
		}
		if repo_model.IsErrUserDoesNotHaveAccessToRepo(err) {
			recordPushRefused(ctx, opts, "permission", "")
			ctx.Error(http.StatusBadRequest, "UserDoesNotHaveAccessToRepo", err.Error())
		} else {
			log.Error(err.Error())
			ctx.JSON(http.StatusInternalServerError, private.Response{
				Err: err.Error(),
			})
		}

		return
	}

	ctx.JSON(http.StatusOK, private.HookProcReceiveResult{
		Results: results,
	})
}
