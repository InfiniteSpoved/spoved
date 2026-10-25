// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import (
	"context"
	"strings"
)

func init() {
	register(&Format{Ecosystem: RubyGems, Small: true, match: func(b string) bool { return b == "Gemfile.lock" }, parse: parseGemfileLock})
}

// parseGemfileLock reads the specs of the GEM and GIT sections (PATH gems are the project's own); the names listed
// in DEPENDENCIES are direct.
func parseGemfileLock(ctx context.Context, data []byte, _ Finder) ([]Dependency, error) {
	var deps []Dependency
	section, inSpecs := "", false
	direct := map[string]bool{}
	sc := newScanner(ctx, data)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if line[0] != ' ' {
			section, inSpecs = strings.TrimSpace(line), false
			continue
		}
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))
		switch {
		case section == "DEPENDENCIES" && indent == 2:
			name, _, _ := strings.Cut(trimmed, " ")
			direct[strings.TrimSuffix(name, "!")] = true
		case (section == "GEM" || section == "GIT") && indent == 2:
			inSpecs = trimmed == "specs:"
		case (section == "GEM" || section == "GIT") && inSpecs && indent == 4:
			name, rest, ok := strings.Cut(trimmed, " (")
			if ok {
				version, _, _ := strings.Cut(strings.TrimSuffix(rest, ")"), "-") // 1.15.4-x86_64-linux: a platform build of 1.15.4
				deps = append(deps, Dependency{Name: name, Version: version})
			}
		}
	}
	if sc.Err() != nil {
		return nil, parseErr("Gemfile.lock")
	}
	for i := range deps {
		deps[i].Direct = direct[deps[i].Name]
	}
	return deps, nil
}
