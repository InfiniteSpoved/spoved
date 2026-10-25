// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import (
	"bytes"
	"context"
	"strings"

	"forgejo.org/modules/json"
)

func init() {
	register(&Format{Ecosystem: Packagist, match: func(b string) bool { return b == "composer.lock" }, parse: parseComposerLock})
}

type composerPackage struct {
	Name    string   `json:"name"`
	Version string   `json:"version"`
	License []string `json:"license"`
	Dist    struct {
		Type string `json:"type"`
	} `json:"dist"`
}

// parseComposerLock reads packages and packages-dev. A license array lists alternatives, so it becomes an SPDX
// "A OR B" expression. composer.lock doesn't say what the project requires: the direct packages are the ones the
// composer.json next to it requires (require and require-dev). Without a composer.json none is marked direct.
func parseComposerLock(ctx context.Context, data []byte, find Finder) ([]Dependency, error) {
	var lock struct {
		Packages    []composerPackage `json:"packages"`
		PackagesDev []composerPackage `json:"packages-dev"`
	}
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&lock); err != nil {
		return nil, parseErr("composer.lock")
	}
	direct := map[string]bool{}
	if cj := find("composer.json", false); cj != nil {
		var c struct {
			Require    map[string]npmIgnored `json:"require"`
			RequireDev map[string]npmIgnored `json:"require-dev"`
		}
		if json.Unmarshal(cj, &c) == nil {
			for _, m := range []map[string]npmIgnored{c.Require, c.RequireDev} {
				for n := range m {
					direct[strings.ToLower(n)] = true
				}
			}
		}
	}
	var deps []Dependency
	for _, list := range [][]composerPackage{lock.Packages, lock.PackagesDev} {
		for _, p := range list {
			if p.Dist.Type == "path" {
				continue // a package from a path repository is part of the project, not a dependency
			}
			deps = append(deps, Dependency{Name: p.Name, Version: p.Version, License: strings.Join(p.License, " OR "), Direct: direct[strings.ToLower(p.Name)]})
		}
	}
	return deps, nil
}
