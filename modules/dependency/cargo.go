// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import (
	"context"
	"strings"
)

func init() {
	register(&Format{Ecosystem: Cargo, match: func(b string) bool { return b == "Cargo.lock" }, parse: parseCargoLock})
}

type cargoPackage struct {
	name, version, source string
	deps                  []string
}

// parseCargoLock reads the [[package]] tables. A package without a `source` belongs to the project itself (the root
// or a workspace member, or a path dependency) and is not a dependency; what such packages depend on is direct.
func parseCargoLock(ctx context.Context, data []byte, _ Finder) ([]Dependency, error) {
	var pkgs []*cargoPackage
	var cur *cargoPackage
	inDeps := false
	sc := newScanner(ctx, data)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "[[package]]":
			cur, inDeps = &cargoPackage{}, false
			pkgs = append(pkgs, cur)
		case cur == nil || line == "" || line[0] == '#':
		case line[0] == '[':
			cur = nil // [metadata], [[patch.unused]]
		case inDeps:
			if strings.HasPrefix(line, "]") {
				inDeps = false
				continue
			}
			if s := strings.Trim(strings.TrimSuffix(line, ","), `"`); s != "" {
				cur.deps = append(cur.deps, s)
			}
		default:
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			switch strings.TrimSpace(k) {
			case "name":
				cur.name = tomlString(v)
			case "version":
				cur.version = tomlString(v)
			case "source":
				cur.source = tomlString(v)
			case "dependencies":
				inDeps = strings.TrimSpace(v) == "[" // a one-line array is written on one line only when empty
			}
		}
	}
	if sc.Err() != nil {
		return nil, parseErr("Cargo.lock")
	}
	direct := map[string]bool{} // "name" or "name version"
	for _, p := range pkgs {
		if p.source != "" {
			continue
		}
		for _, d := range p.deps {
			f := strings.Fields(d)
			if len(f) >= 2 {
				direct[f[0]+" "+f[1]] = true
			} else if len(f) == 1 {
				direct[f[0]] = true
			}
		}
	}
	var deps []Dependency
	for _, p := range pkgs {
		if p.source == "" || p.name == "" {
			continue
		}
		deps = append(deps, Dependency{Name: p.name, Version: p.version, Direct: direct[p.name] || direct[p.name+" "+p.version]})
	}
	return deps, nil
}
