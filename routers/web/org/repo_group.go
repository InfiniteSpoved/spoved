// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package org

import (
	"net/http"

	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/base"
	"forgejo.org/services/context"
)

const tplRepoGroupHome base.TplName = "org/repo_group/home"

// RepoGroupHome renders the home of a project, `/{org}/{project}`.
func RepoGroupHome(ctx *context.Context) {
	group := ctx.Data["RepoGroup"].(*repo_model.RepoGroup)
	ctx.Data["Title"] = ctx.Org.Organization.Name + "/" + group.Name
	ctx.HTML(http.StatusOK, tplRepoGroupHome)
}
