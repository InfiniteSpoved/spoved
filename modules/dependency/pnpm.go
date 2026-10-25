// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import (
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

func init() {
	register(&Format{Ecosystem: NPM, match: func(b string) bool { return b == "pnpm-lock.yaml" }, parse: parsePnpmLock})
}

type pnpmImporter struct {
	Dependencies         map[string]yaml.Node `yaml:"dependencies"`
	DevDependencies      map[string]yaml.Node `yaml:"devDependencies"`
	OptionalDependencies map[string]yaml.Node `yaml:"optionalDependencies"`
}

type pnpmLockfile struct {
	LockfileVersion any                     `yaml:"lockfileVersion"`
	Importers       map[string]pnpmImporter `yaml:"importers"`
	pnpmImporter    `yaml:",inline"`        // lockfile v5 has the root project's dependencies at the top
	Packages        map[string]struct {
		Name    string `yaml:"name"`
		Version string `yaml:"version"`
	} `yaml:"packages"`
}

// parsePnpmLock reads lockfile versions 5, 6 and 9. A package is direct if any importer (the root project or a
// workspace project) lists it.
func parsePnpmLock(data []byte, _ Finder) ([]Dependency, error) {
	var lock pnpmLockfile
	if err := yaml.Unmarshal(data, &lock); err != nil {
		return nil, parseErr("pnpm-lock.yaml")
	}
	oldKeys := strings.HasPrefix(fmt.Sprint(lock.LockfileVersion), "5") // name/version instead of name@version
	direct := map[string]bool{}
	imp := func(i pnpmImporter) {
		for _, m := range []map[string]yaml.Node{i.Dependencies, i.DevDependencies, i.OptionalDependencies} {
			for n := range m {
				direct[n] = true
			}
		}
	}
	imp(lock.pnpmImporter)
	for _, i := range lock.Importers {
		imp(i)
	}
	keys := make([]string, 0, len(lock.Packages))
	for k := range lock.Packages {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var deps []Dependency
	for _, k := range keys {
		name, version := pnpmKey(k, oldKeys)
		if p := lock.Packages[k]; p.Name != "" {
			name = p.Name
		}
		if p := lock.Packages[k]; p.Version != "" {
			version = p.Version
		}
		deps = append(deps, Dependency{Name: name, Version: version, Direct: direct[name]})
	}
	return deps, nil
}

// pnpmKey splits a key of "packages": `/name@1.2.3(peer@1)`, `name@1.2.3` or, in v5, `/@scope/name/1.2.3_peer`.
func pnpmKey(k string, old bool) (name, version string) {
	k = strings.TrimPrefix(k, "/")
	if old {
		i := strings.LastIndex(k, "/")
		if i < 0 {
			return k, ""
		}
		version, _, _ = strings.Cut(k[i+1:], "_")
		return k[:i], version
	}
	k, _, _ = strings.Cut(k, "(")
	if i := strings.LastIndex(k, "@"); i > 0 {
		return k[:i], k[i+1:]
	}
	return k, ""
}
