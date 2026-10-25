// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import (
	"context"
	"sort"
	"strings"

	"forgejo.org/modules/json"
)

func init() {
	register(&Format{Ecosystem: NPM, match: func(b string) bool { return b == "package-lock.json" }, parse: parseNPMLock})
}

// npmLicense is the "license" of a package-lock entry: a string, or in old files an object or a list.
type npmLicense string

func (l *npmLicense) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*l = npmLicense(s)
		return nil
	}
	var o struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(b, &o) == nil {
		*l = npmLicense(o.Type)
		return nil
	}
	var a []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(b, &a) == nil {
		var ts []string
		for _, x := range a {
			if x.Type != "" {
				ts = append(ts, x.Type)
			}
		}
		*l = npmLicense(strings.Join(ts, " OR "))
	}
	return nil // the license is a hint, a strange one is not an error
}

// npmIgnored is a JSON value that is checked but not kept: only the keys of the dependency maps are used, and a
// hostile file must not cost more memory than its size.
type npmIgnored struct{}

func (*npmIgnored) UnmarshalJSON([]byte) error { return nil }

type npmDeps map[string]npmIgnored

type npmPackage struct {
	Name                 string     `json:"name"`
	Version              string     `json:"version"`
	License              npmLicense `json:"license"`
	Link                 bool       `json:"link"`
	Dependencies         npmDeps    `json:"dependencies"`
	DevDependencies      npmDeps    `json:"devDependencies"`
	OptionalDependencies npmDeps    `json:"optionalDependencies"`
	PeerDependencies     npmDeps    `json:"peerDependencies"`
}

// npmV1 is an entry of the "dependencies" tree of lockfileVersion 1 (and of 2, which holds both).
type npmV1 struct {
	Version      string            `json:"version"`
	Dependencies map[string]*npmV1 `json:"dependencies"`
}

type npmLockfile struct {
	Packages     map[string]*npmPackage `json:"packages"`
	Dependencies map[string]*npmV1      `json:"dependencies"`
}

// parseNPMLock reads lockfileVersion 2 and 3 from "packages" (the root, the empty key, says which ones are direct)
// and version 1 from the "dependencies" tree (the direct ones come from the package.json next to the file, if any).
func parseNPMLock(ctx context.Context, data []byte, find Finder) ([]Dependency, error) {
	var lock npmLockfile
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, parseErr("package-lock.json")
	}
	var deps []Dependency
	if len(lock.Packages) > 0 {
		// the root project and the workspace members (entries outside node_modules) ask for their dependencies
		direct := map[string]bool{}
		for k, p := range lock.Packages {
			if p == nil || (k != "" && strings.Contains(k, "node_modules/")) {
				continue
			}
			for _, m := range []npmDeps{p.Dependencies, p.DevDependencies, p.OptionalDependencies, p.PeerDependencies} {
				for n := range m {
					direct[n] = true
				}
			}
		}
		keys := make([]string, 0, len(lock.Packages))
		for k := range lock.Packages {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := lock.Packages[k]
			if k == "" || p == nil || p.Link {
				continue // the root, and workspace links (their target has its own entry)
			}
			i := strings.LastIndex(k, "node_modules/")
			if i < 0 {
				continue // a workspace member, not a registry package
			}
			installed := k[i+len("node_modules/"):]
			name := installed
			if p.Name != "" {
				name = p.Name // an alias: the entry is installed under another name
			}
			nested := strings.Count(k, "node_modules/") > 1
			deps = append(deps, Dependency{Name: name, Version: p.Version, License: string(p.License), Direct: !nested && (direct[installed] || direct[name])})
		}
		return deps, nil
	}
	direct := npmPackageJSONDeps(find("package.json", false))
	var walk func(m map[string]*npmV1, top bool)
	walk = func(m map[string]*npmV1, top bool) {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, n := range keys {
			d := m[n]
			if d == nil {
				continue
			}
			deps = append(deps, Dependency{Name: n, Version: d.Version, Direct: top && direct[n]})
			walk(d.Dependencies, false)
		}
	}
	walk(lock.Dependencies, true)
	return deps, nil
}

// npmPackageJSONDeps returns the names a package.json declares; nil data gives an empty set.
func npmPackageJSONDeps(data []byte) map[string]bool {
	out := map[string]bool{}
	if data == nil {
		return out
	}
	var p npmPackage
	if json.Unmarshal(data, &p) != nil {
		return out
	}
	for _, m := range []npmDeps{p.Dependencies, p.DevDependencies, p.OptionalDependencies, p.PeerDependencies} {
		for n := range m {
			out[n] = true
		}
	}
	return out
}

// npmRange is a version range of a package.json; a value that isn't a string reads as empty.
type npmRange string

func (r *npmRange) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*r = npmRange(s)
	}
	return nil
}

// npmPackageJSONRanges returns what a package.json asks for: dependency name to range (the last one wins when
// several sections name it). nil data gives an empty map.
func npmPackageJSONRanges(data []byte) map[string]string {
	out := map[string]string{}
	if data == nil {
		return out
	}
	var p struct {
		Dependencies         map[string]npmRange `json:"dependencies"`
		DevDependencies      map[string]npmRange `json:"devDependencies"`
		OptionalDependencies map[string]npmRange `json:"optionalDependencies"`
		PeerDependencies     map[string]npmRange `json:"peerDependencies"`
	}
	if json.Unmarshal(data, &p) != nil {
		return out
	}
	for _, m := range []map[string]npmRange{p.Dependencies, p.DevDependencies, p.OptionalDependencies, p.PeerDependencies} {
		for n, r := range m {
			out[n] = string(r)
		}
	}
	return out
}
