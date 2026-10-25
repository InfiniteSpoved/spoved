// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repo

import (
	"errors"
	"net/http"

	"forgejo.org/modules/setting"
	"forgejo.org/routers/common"
	"forgejo.org/services/context"
	dependency_service "forgejo.org/services/dependency"
)

// MustEnableDependencyGraph answers 404 while [dependency_graph] is off.
func MustEnableDependencyGraph(ctx *context.Context) {
	if !setting.DependencyGraph.Enabled {
		ctx.NotFound("MustEnableDependencyGraph", nil)
	}
}

// DependenciesSBOM downloads the SBOM of the repository (?format=spdx|cyclonedx), made from the stored dependencies.
func DependenciesSBOM(ctx *context.Context) {
	format, ok := dependency_service.ParseFormat(ctx.FormString("format"))
	if !ok {
		ctx.Error(http.StatusBadRequest, "format must be spdx or cyclonedx")
		return
	}
	repo := ctx.Repo.Repository
	sbom, err := dependency_service.LoadSBOM(ctx, repo)
	if err != nil {
		ctx.ServerError("LoadSBOM", err)
		return
	}
	if sbom == nil {
		dependency_service.Enqueue(repo.ID)
		ctx.Error(http.StatusConflict, "the repository has not been scanned yet")
		return
	}
	if !common.ServeSBOM(ctx.Base, repo, sbom, format) {
		ctx.ServerError("ServeSBOM", errors.New("the SBOM could not be made"))
	}
}
