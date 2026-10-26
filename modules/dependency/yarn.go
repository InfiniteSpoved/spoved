// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import (
	"strings"
)

func init() {
	register(&Format{Ecosystem: NPM, match: func(b string) bool { return b == "yarn.lock" }, parse: parseYarnLock})
}

// parseYarnLock reads yarn.lock version 1 (`version "1.2.3"`) and the berry format (`version: 1.2.3`). The
// lock file doesn't say which packages the project asks for, so the direct ones come from the package.json next to
// it: a package is direct if one of its descriptors is exactly what the package.json asks for (`name@range`, in
// berry `name@npm:range`), so another version of the same package is not. An alias descriptor
// (`alias@npm:real@range`) names the real package. Workspaces and packages from the disk (link:, portal:, file:,
// workspace:) are not listed.
func parseYarnLock(s *sink, data []byte, find Finder) error {
	asked := s.npmPackageJSON(find("package.json", false))
	var name string
	var direct, local bool
	var version string
	flush := func() error {
		var err error
		if name != "" && !local {
			err = s.emit(Dependency{Name: name, Version: version, Direct: direct})
		}
		name, version, direct, local = "", "", false, false
		return err
	}
	sc := newScanner(s, data)
	bad := func() error { return parseErr("yarn.lock") }
	prev := 0 // the indentation of the previous line
	haveVersion := false
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)
		// only the shapes yarn writes: a header `a@^1, "b@^2":` (v1) or `"a@npm:^1, a@npm:^2":` (berry) at column 0,
		// fields at 2 spaces and nested maps below them; no tabs, flow collections, comments or trailing spaces
		if strings.ContainsRune(line[:indent], '\t') || strings.HasPrefix(trimmed, "\t") || indent%2 != 0 || indent > prev+2 ||
			line != strings.TrimRight(line, " \t") || !yarnQuotesOK(trimmed) {
			return bad()
		}
		prev = indent
		if indent == 0 {
			if err := flush(); err != nil {
				return err
			}
			haveVersion = false
			header, ok := strings.CutSuffix(line, ":")
			if !ok || header == "" || strings.ContainsAny(header, "{}[]") {
				return bad()
			}
			if strings.HasPrefix(line, "__metadata") {
				continue
			}
			for desc := range strings.SplitSeq(header, ",") {
				desc = strings.Trim(strings.TrimSpace(desc), `"`)
				alias, rng, ok := splitYarnDescriptor(desc)
				if !ok {
					continue
				}
				real := alias
				if r, found := strings.CutPrefix(rng, "npm:"); found {
					if i := strings.LastIndex(r, "@"); i > 0 {
						real = r[:i] // alias@npm:real@range
					}
				}
				if name == "" {
					name = real
				}
				if yarnLocalRange(rng) {
					local = true
				}
				if want, ok := asked[alias]; ok && (want == rng || "npm:"+want == rng) {
					direct = true
				}
			}
			continue
		}
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") || strings.HasSuffix(trimmed, "}") || strings.HasSuffix(trimmed, "]") {
			return bad() // a flow collection
		}
		if indent == 2 {
			if v, ok := strings.CutPrefix(trimmed, "version"); ok && (v == "" || v[0] == ' ' || v[0] == ':') {
				if haveVersion {
					return bad() // the later one would win in yarn's parser
				}
				haveVersion = true
				version = strings.Trim(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), ":")), `"`)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return scanErr(err, "yarn.lock")
	}
	return flush()
}

// yarnQuotesOK reports whether the double quotes of a line pair up (a backslash escapes the next character inside
// them) and no single quote starts a scalar. Descriptors, resolutions and checksums contain no other quotes.
func yarnQuotesOK(l string) bool {
	in := false
	for i := 0; i < len(l); i++ {
		switch c := l[i]; {
		case in && c == '\\':
			i++
		case c == '"':
			in = !in
		case !in && c == '\'' && (i == 0 || l[i-1] == ' ' || l[i-1] == ','):
			return false
		}
	}
	return !in
}

// splitYarnDescriptor splits `name@range` (the name may start with @scope/).
func splitYarnDescriptor(desc string) (name, rng string, ok bool) {
	if len(desc) < 2 {
		return "", "", false
	}
	i := strings.IndexByte(desc[1:], '@')
	if i < 0 {
		return "", "", false
	}
	return desc[:i+1], desc[i+2:], true
}

// yarnLocalRange tells the ranges that point at the disk or at the project itself.
func yarnLocalRange(r string) bool {
	r = strings.TrimPrefix(r, "npm:")
	for _, p := range []string{"workspace:", "link:", "portal:", "file:"} {
		if strings.HasPrefix(r, p) {
			return true
		}
	}
	return false
}
