// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import "strings"

func init() {
	register(&Format{Ecosystem: NPM, match: func(b string) bool { return b == "yarn.lock" }, parse: parseYarnLock})
}

// parseYarnLock reads yarn.lock version 1 (`version "1.2.3"`) and the berry format (`version: 1.2.3`). The
// lock file doesn't say which packages the project asks for, so the direct ones come from the package.json next to it.
func parseYarnLock(data []byte, find Finder) ([]Dependency, error) {
	direct := npmPackageJSONDeps(find("package.json", false))
	var deps []Dependency
	var names []string
	var version string
	var workspace bool
	flush := func() {
		if len(names) > 0 && !workspace {
			d := Dependency{Name: names[0], Version: version}
			for _, n := range names {
				d.Direct = d.Direct || direct[n]
			}
			deps = append(deps, d)
		}
		names, version, workspace = nil, "", false
	}
	sc := newScanner(data)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#"):
			continue
		case line[0] != ' ':
			flush()
			if !strings.HasSuffix(line, ":") || strings.HasPrefix(line, "__metadata") {
				continue
			}
			for desc := range strings.SplitSeq(strings.TrimSuffix(line, ":"), ",") {
				desc = strings.Trim(strings.TrimSpace(desc), `"`)
				if len(desc) > 1 {
					if i := strings.IndexByte(desc[1:], '@'); i >= 0 {
						names = append(names, desc[:i+1])
					}
				}
			}
			if len(names) > 0 && strings.Contains(line, "@workspace:") {
				workspace = true
			}
		case len(line) > 2 && line[1] == ' ' && line[2] != ' ':
			rest := strings.TrimSpace(line)
			if v, ok := strings.CutPrefix(rest, "version"); ok {
				version = strings.Trim(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), ":")), `"`)
			}
		}
	}
	if sc.Err() != nil {
		return nil, parseErr("yarn.lock")
	}
	flush()
	return deps, nil
}
