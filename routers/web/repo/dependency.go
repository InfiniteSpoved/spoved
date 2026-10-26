// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repo

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/base"
	"forgejo.org/modules/dependency"
	"forgejo.org/modules/optional"
	"forgejo.org/routers/common"
	"forgejo.org/services/context"
	dependency_service "forgejo.org/services/dependency"
)

// MustEnableDependencyGraph answers 404 while the dependency graph is off for the repository (site, organization
// or repository switch), as if the page didn't exist.
func MustEnableDependencyGraph(ctx *context.Context) {
	if !dependency_service.Enabled(ctx, ctx.Repo.Repository) {
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
		dependency_service.EnqueueIfUnscanned(ctx, repo.ID)
		ctx.Error(http.StatusConflict, "the repository has not been scanned yet")
		return
	}
	if !common.ServeSBOM(ctx.Base, repo, sbom, format) {
		ctx.ServerError("ServeSBOM", errors.New("the SBOM could not be made"))
	}
}

// dependencyPageSize is the number of rows on a page of the Dependencies page.
const dependencyPageSize = 50

// unpinnedGroup is the manifests of one ecosystem that were read without a lock file, with the lock files that
// would pin them.
type unpinnedGroup struct {
	Paths string
	Count int
	Locks string
}

func unpinnedManifests(ctx *context.Context, manifests []repo_model.DependencyFacet) []*unpinnedGroup {
	var out []*unpinnedGroup
	byEcosystem := map[string]*unpinnedGroup{}
	for _, m := range manifests {
		f := dependency.FormatForPath(m.Value)
		if f == nil || !f.Unpinned {
			continue
		}
		g := byEcosystem[f.Ecosystem]
		if g == nil {
			locks := f.Locks[len(f.Locks)-1]
			if n := len(f.Locks); n > 1 {
				locks = ctx.Locale.TrString("repo.dependencies.locks_or", strings.Join(f.Locks[:n-1], ", "), locks)
			}
			g = &unpinnedGroup{Locks: locks}
			byEcosystem[f.Ecosystem] = g
			out = append(out, g)
		} else {
			g.Paths += ", "
		}
		g.Paths += m.Value
		g.Count++
	}
	return out
}

// SetDependencyGraphNav tells the Activity menu whether to show the Dependencies item.
func SetDependencyGraphNav(ctx *context.Context) {
	ctx.Data["DependencyGraphEnabled"] = dependency_service.Enabled(ctx, ctx.Repo.Repository)
}

// Dependencies renders the list of the packages of the default branch, with the filters and the SBOM export.
func Dependencies(ctx *context.Context) {
	repo := ctx.Repo.Repository
	ctx.Data["Title"] = ctx.Tr("repo.dependencies.title")
	ctx.Data["PageIsActivity"] = true
	ctx.Data["PageIsDependencies"] = true
	ctx.Data["SBOMSPDXName"] = dependency_service.SBOMFileName(repo, dependency_service.FormatSPDX)
	ctx.Data["SBOMCycloneDXName"] = dependency_service.SBOMFileName(repo, dependency_service.FormatCycloneDX)
	ctx.Data["SkipCodes"] = repo_model.DependencySkipCodes

	scan, err := repo_model.GetRepoDependencyScan(ctx, repo.ID)
	if err != nil {
		ctx.ServerError("GetRepoDependencyScan", err)
		return
	}
	if scan == nil {
		// the first read queues the scan (see the API); the page says it is running
		dependency_service.Enqueue(repo.ID)
		ctx.HTML(http.StatusOK, tplActivity)
		return
	}
	ctx.Data["Scan"] = scan
	ctx.Data["ScanFailed"] = scan.Status == repo_model.DependencyScanFailed
	ctx.Data["ScanNeverRead"] = scan.CommitSHA == ""
	var skipped, partial []repo_model.SkippedManifest
	for _, m := range scan.SkippedManifests() {
		if m.Code == repo_model.DependencySkipPartial {
			partial = append(partial, m)
		} else {
			skipped = append(skipped, m)
		}
	}
	ctx.Data["Skipped"] = skipped
	ctx.Data["Partial"] = partial
	ctx.Data["ScannedTime"] = scan.ScannedUnix
	if scan.CommitSHA != "" {
		ctx.Data["ShortSHA"] = base.ShortSha(scan.CommitSHA)
		// "rescanning" is true: the scan is queued again (the unique queue dedupes) unless the last one failed
		if head, err := ctx.Repo.GitRepo.GetBranchCommitID(repo.DefaultBranch); err == nil && head != scan.CommitSHA && scan.Status == repo_model.DependencyScanOK {
			ctx.Data["StaleHead"] = base.ShortSha(head)
			dependency_service.Enqueue(repo.ID)
		}
	}
	if scan.CommitSHA == "" {
		ctx.HTML(http.StatusOK, tplActivity)
		return
	}

	facets, err := repo_model.GetDependencyFacets(ctx, repo.ID)
	if err != nil {
		ctx.ServerError("GetDependencyFacets", err)
		return
	}
	ctx.Data["Facets"] = facets
	ctx.Data["TotalPackages"] = facets.Direct + facets.Transitive
	ctx.Data["ManifestsWithPackages"] = len(facets.Manifests)
	ctx.Data["UnpinnedManifests"] = unpinnedManifests(ctx, facets.Manifests)

	opts := &repo_model.DependencySearchOptions{
		ListOptions: db.ListOptions{Page: max(ctx.FormInt("page"), 1), PageSize: dependencyPageSize},
		RepoID:      repo.ID,
	}
	if q := ctx.FormTrim("q"); len(q) <= repo_model.MaxDependencyQuery {
		opts.Q = q
	}
	if eco := ctx.FormString("ecosystem"); eco != "" {
		if !dependency.IsEcosystem(eco) { // as the API does
			ctx.Error(http.StatusUnprocessableEntity, "unknown ecosystem")
			return
		}
		opts.Ecosystem = eco
	}
	relationship := ctx.FormString("relationship")
	switch relationship {
	case "direct":
		opts.Direct = optional.Some(true)
	case "transitive":
		opts.Direct = optional.Some(false)
	default:
		relationship = ""
	}
	if m := ctx.FormString("manifest"); slices.ContainsFunc(facets.Manifests, func(f repo_model.DependencyFacet) bool { return f.Value == m }) {
		opts.Manifest = m
	}
	ctx.Data["Query"], ctx.Data["Ecosystem"], ctx.Data["Relationship"], ctx.Data["Manifest"] = opts.Q, opts.Ecosystem, relationship, opts.Manifest
	ctx.Data["HasFilter"] = opts.Q != "" || opts.Ecosystem != "" || relationship != "" || opts.Manifest != ""

	rows, total, err := db.FindAndCount[repo_model.Dependency](ctx, opts)
	if err != nil {
		ctx.ServerError("FindAndCount", err)
		return
	}
	ctx.Data["Dependencies"] = rows
	ctx.Data["Total"] = total
	pager := context.NewPagination(int(total), dependencyPageSize, opts.Page, 5)
	for k, v := range map[string]string{"q": opts.Q, "ecosystem": opts.Ecosystem, "relationship": relationship, "manifest": opts.Manifest} {
		if v != "" {
			pager.AddParamString(k, v)
		}
	}
	ctx.Data["Page"] = pager
	ctx.HTML(http.StatusOK, tplActivity)
}
