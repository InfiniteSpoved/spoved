// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package structs

import "time"

// Dependency is a package that a manifest of the repository's default branch declares.
// swagger:model
type Dependency struct {
	// The ecosystem, named as OSV names it: Go, npm, PyPI, Maven, NuGet, crates.io, RubyGems or Packagist
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	// The version, empty if the manifest doesn't fix it
	Version string `json:"version"`
	// The package URL (purl) of the dependency
	PURL string `json:"purl"`
	// The path of the manifest in the repository
	ManifestPath string `json:"manifest_path"`
	// Whether the project itself declares it, not only another dependency
	Direct bool `json:"direct"`
	// The license as the manifest states it, empty if it doesn't
	License string `json:"license"`
}

// SkippedManifest is a manifest that was not read.
// swagger:model
type SkippedManifest struct {
	Path string `json:"path"`
	// parse_error, manifest_too_large, too_many_manifests, too_many_dependencies, too_many_entries, invalid_path or read_error
	Code string `json:"code"`
}

// DependencyScan is the state of the last scan of a repository.
// swagger:model
type DependencyScan struct {
	// pending (never scanned), ok or failed
	Status string `json:"status"`
	// The reason of a failed last scan: timeout, tree_too_large, git_error or internal_error. The dependencies of
	// the last good scan are kept.
	Error string `json:"error,omitempty"`
	// The commit of the default branch the dependencies were read from
	CommitSHA string `json:"commit_sha,omitempty"`
	// swagger:strfmt date-time
	ScannedAt        *time.Time        `json:"scanned_at,omitempty"`
	ManifestCount    int               `json:"manifest_count"`
	DependencyCount  int               `json:"dependency_count"`
	SkippedManifests []SkippedManifest `json:"skipped_manifests"`
}

// DependencyList is a page of the dependencies of a repository and the state of its scan.
// swagger:model
type DependencyList struct {
	Scan         DependencyScan `json:"scan"`
	Dependencies []*Dependency  `json:"dependencies"`
}
