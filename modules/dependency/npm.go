// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import (
	"bytes"
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

type npmDeps map[string]any

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
func parseNPMLock(data []byte, find Finder) ([]Dependency, error) {
	var lock npmLockfile
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&lock); err != nil {
		return nil, parseErr("package-lock.json")
	}
	var deps []Dependency
	if len(lock.Packages) > 0 {
		direct := map[string]bool{}
		if root := lock.Packages[""]; root != nil {
			for _, m := range []npmDeps{root.Dependencies, root.DevDependencies, root.OptionalDependencies, root.PeerDependencies} {
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
			name := k[i+len("node_modules/"):]
			if p.Name != "" {
				name = p.Name // an alias: the entry is installed under another name
			}
			nested := strings.Count(k, "node_modules/") > 1
			deps = append(deps, Dependency{Name: name, Version: p.Version, License: string(p.License), Direct: !nested && direct[name]})
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
