// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package admin

import (
	"errors"
	"math"
	"net/http"

	quota_model "forgejo.org/models/quota"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/base"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"
	"forgejo.org/services/context"
	repo_service "forgejo.org/services/repository"
)

const (
	tplPersonalRepos base.TplName = "admin/personal_repos"

	maxPersonalRepoViolators = 100
)

// PersonalRepos shows the site policy for personal repositories and the users whose repositories break it
func PersonalRepos(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("admin.personal_repos")
	ctx.Data["PageIsAdminPersonalRepos"] = true
	s := setting.Config().PersonalRepos.Value(ctx)
	mode := s.EffectiveMode()
	ctx.Data["Mode"] = mode
	ctx.Data["Modes"] = []string{setting.PersonalReposAllowed, setting.PersonalReposPrivateOnly, setting.PersonalReposDisabled}
	if s.UpdatedUnix > 0 {
		ctx.Data["UpdatedUnix"] = timeutil.TimeStamp(s.UpdatedUnix)
		ctx.Data["UpdatedBy"] = "-"
		if u, err := user_model.GetPossibleUserByID(ctx, s.UpdatedBy); err == nil {
			ctx.Data["UpdatedBy"] = u.Name
		}
	}
	violators, total, err := repo_model.FindPersonalRepoViolators(ctx, mode, maxPersonalRepoViolators)
	if err != nil {
		ctx.ServerError("FindPersonalRepoViolators", err)
		return
	}
	ctx.Data["Violators"] = violators
	ctx.Data["ViolatorsTotal"] = total
	sizeLimit := s.EffectiveSizeLimit()
	ctx.Data["SizeLimit"] = sizeLimit
	ctx.Data["SizeLimitMiB"] = sizeLimit / (1 << 20)
	overLimit, overLimitTotal, err := quota_model.FindPersonalRepoOverLimit(ctx, maxPersonalRepoViolators)
	if err != nil {
		ctx.ServerError("FindPersonalRepoOverLimit", err)
		return
	}
	ctx.Data["OverLimit"] = overLimit
	ctx.Data["OverLimitTotal"] = overLimitTotal
	ctx.HTML(http.StatusOK, tplPersonalRepos)
}

// PersonalReposPost stores the policy
func PersonalReposPost(ctx *context.Context) {
	mib := ctx.FormInt64("size_limit_mib")
	if mib < 0 || mib > math.MaxInt64>>20 {
		ctx.Error(http.StatusBadRequest, "invalid size limit")
		return
	}
	err := repo_service.SetPersonalRepoPolicy(ctx, ctx.Doer, ctx.FormString("mode"), mib<<20)
	if errors.Is(err, util.ErrInvalidArgument) {
		ctx.Error(http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		ctx.ServerError("SetPersonalRepoPolicy", err)
		return
	}
	ctx.Flash.Success(ctx.Tr("admin.personal_repos.saved"))
	ctx.Redirect(setting.AppSubURL + "/admin/personal-repos")
}
