// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import (
	"golang.org/x/mod/modfile"
)

func init() {
	register(&Format{Ecosystem: Go, Small: true, match: func(b string) bool { return b == "go.mod" }, parse: parseGoMod})
}

// parseGoMod lists the requirements; `// indirect` marks the transitive ones. A replace directive is not followed.
func parseGoMod(data []byte, _ Finder) ([]Dependency, error) {
	f, err := modfile.ParseLax("go.mod", data, nil)
	if err != nil {
		return nil, parseErr("go.mod")
	}
	deps := make([]Dependency, 0, len(f.Require))
	for _, r := range f.Require {
		deps = append(deps, Dependency{Name: r.Mod.Path, Version: r.Mod.Version, Direct: !r.Indirect})
	}
	return deps, nil
}
