// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import (
	"regexp"
	"slices"
	"strings"
)

// The manifests read when no lock file covers them (Format.Unpinned): they list only the direct dependencies, each
// with the range it declares as its Requirement and no version. Nothing is resolved or evaluated: a Gemfile is
// read line by line for plain `gem "name", "range"` calls, the TOML files by the statement reader in toml.go.

func init() {
	register(&Format{
		Ecosystem: NPM, Small: true, Unpinned: true, Locks: []string{"package-lock.json", "yarn.lock", "pnpm-lock.yaml"}, LocksAbove: true,
		match: func(b string) bool { return b == "package.json" }, parse: parsePackageJSON,
	})
	register(&Format{
		Ecosystem: Cargo, Small: true, Unpinned: true, Locks: []string{"Cargo.lock"}, LocksAbove: true,
		match: func(b string) bool { return b == "Cargo.toml" }, parse: parseCargoToml,
	})
	register(&Format{
		Ecosystem: RubyGems, Small: true, Unpinned: true, Locks: []string{"Gemfile.lock"},
		match: func(b string) bool { return b == "Gemfile" }, parse: parseGemfile,
	})
	register(&Format{
		Ecosystem: Packagist, Small: true, Unpinned: true, Locks: []string{"composer.lock"},
		match: func(b string) bool { return b == "composer.json" }, parse: parseComposerJSON,
	})
	register(&Format{
		Ecosystem: PyPI, Small: true, Unpinned: true, Locks: []string{"poetry.lock", "uv.lock"},
		match: func(b string) bool { return b == "pyproject.toml" }, parse: parsePyproject,
	})
}

// IsLockName reports whether a file of that base name is a lock file that covers an unpinned manifest.
func IsLockName(base string) bool {
	for _, f := range formats {
		if slices.Contains(f.Locks, base) {
			return true
		}
	}
	return false
}

// Covered reports whether a lock file covers the manifest at p (has tells whether a path is in the tree): one of
// its Locks next to it, or with LocksAbove in a directory above it.
func (f *Format) Covered(p string, has func(string) bool) bool {
	for dir := pathDir(p); ; dir = pathDir(dir) {
		for _, l := range f.Locks {
			if has(pathJoin(dir, l)) {
				return true
			}
		}
		if !f.LocksAbove || dir == "" {
			return false
		}
	}
}

func pathDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}

func pathJoin(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// npmFallbackKeys are the sections of a package.json read without a lock file (peer dependencies are the
// consumer's to install).
var npmFallbackKeys = map[string]bool{"dependencies": true, "devDependencies": true, "optionalDependencies": true}

// parsePackageJSON reads the dependency sections of a package.json. A local range (workspace:, file:, link:,
// a path) is the project's own and left out; an alias (`npm:real@^1`) is the real package; a git or URL range
// keeps the row without a requirement.
func parsePackageJSON(s *sink, data []byte, _ Finder) error {
	j := newJSONStream(s, data)
	ok, err := j.object(func(key string) error {
		if !npmFallbackKeys[key] {
			return j.skip()
		}
		ok, err := j.object(func(name string) error {
			r, err := j.str()
			if err != nil {
				return err
			}
			d, keep := npmRequirement(name, r)
			if !keep {
				return nil
			}
			return s.emit(d)
		})
		if err == nil && !ok {
			return parseErr("package.json: %s is no object", key)
		}
		return err
	})
	if err == nil && !ok {
		return parseErr("package.json")
	}
	return scanErr(err, "package.json")
}

func npmRequirement(name, r string) (Dependency, bool) {
	r = strings.TrimSpace(r)
	if yarnLocalRange(r) || strings.HasPrefix(r, ".") || strings.HasPrefix(r, "/") || strings.HasPrefix(r, "~/") {
		return Dependency{}, false
	}
	if rest, alias := strings.CutPrefix(r, "npm:"); alias {
		name, r = rest, ""
		if at := strings.LastIndexByte(rest, '@'); at > 0 {
			name, r = rest[:at], rest[at+1:]
		}
	}
	if strings.Contains(r, ":") || strings.Contains(r, "/") { // git+https://..., github:x/y, x/y, a tarball URL
		r = ""
	}
	return Dependency{Name: name, Requirement: r, Direct: true}, true
}

// parseComposerJSON reads require and require-dev; platform packages (php, ext-*, lib-*, composer-plugin-api)
// have no vendor and are left out.
func parseComposerJSON(s *sink, data []byte, _ Finder) error {
	j := newJSONStream(s, data)
	ok, err := j.object(func(key string) error {
		if key != "require" && key != "require-dev" {
			return j.skip()
		}
		ok, err := j.object(func(name string) error {
			r, err := j.str()
			if err != nil || !strings.Contains(name, "/") {
				return err
			}
			return s.emit(Dependency{Name: strings.ToLower(name), Requirement: r, Direct: true})
		})
		if err == nil && !ok {
			return parseErr("composer.json: %s is no object", key)
		}
		return err
	})
	if err == nil && !ok {
		return parseErr("composer.json")
	}
	return scanErr(err, "composer.json")
}

// tomlDep is a dependency of a TOML dependency table: its value (`name = "1.0"`) or its fields (an inline table,
// a [deps.name] table or dotted keys).
type tomlDep struct {
	name   string
	value  string
	fields map[string]string
}

// tomlDeps reads the dependency tables of a TOML manifest. table returns the length of the key path that names a
// dependency table (0 for none): the next key is a dependency, and one more a field of it. A dependency or field
// given twice is a parse error, as it is for cargo and poetry. The arrays whose dotted key path keepArray accepts
// are returned in file order.
func tomlDeps(s *sink, data []byte, table func(path []string) int, keepArray func(key string) bool) ([]*tomlDep, [][2]string, error) {
	data, err := prepass(data, false)
	if err != nil {
		return nil, nil, err
	}
	var deps []*tomlDep
	byKey := map[string]*tomlDep{}
	var arrays [][2]string // key path, raw value
	dep := func(path []string, k int) (*tomlDep, error) {
		key := strings.Join(path[:k+1], "\x00")
		if d := byKey[key]; d != nil {
			return d, nil
		}
		if err := s.entry(); err != nil {
			return nil, err
		}
		d := &tomlDep{name: path[k]}
		byKey[key] = d
		deps = append(deps, d)
		return d, nil
	}
	r := &tomlReader{s: s, data: data}
	var header []string
	for {
		st, ok, err := r.next()
		if err != nil {
			return nil, nil, scanErr(err, "toml")
		}
		if !ok {
			return deps, arrays, nil
		}
		if st.Header {
			header = st.Key
			if k := table(header); k > 0 && len(header) == k+1 { // [dependencies.serde]
				d, err := dep(header, k)
				if err != nil {
					return nil, nil, err
				}
				if d.value != "" || d.fields != nil {
					return nil, nil, parseErr("toml: dependency defined twice")
				}
				d.fields = map[string]string{}
			}
			continue
		}
		full := append(append([]string(nil), header...), st.Key...)
		k := table(full)
		switch {
		case k == 0:
			if key := strings.Join(full, "."); keepArray != nil && keepArray(key) {
				if err := s.entry(); err != nil {
					return nil, nil, err
				}
				arrays = append(arrays, [2]string{key, st.Value})
			}
			continue
		case len(full) == k:
			return nil, nil, parseErr("toml: a dependency table as one value") // `dependencies = {...}`: not written by cargo or poetry
		}
		d, err := dep(full, k)
		if err != nil {
			return nil, nil, err
		}
		switch rest := full[k+1:]; {
		case len(rest) == 0:
			if d.value != "" || d.fields != nil {
				return nil, nil, parseErr("toml: dependency defined twice")
			}
			if strings.HasPrefix(st.Value, "{") {
				if d.fields, ok = tomlTable(s, st.Value); !ok {
					return nil, nil, parseErr("toml: inline table")
				}
			} else {
				d.value = st.Value
			}
		case len(rest) == 1:
			if d.value != "" {
				return nil, nil, parseErr("toml: dependency defined twice")
			}
			if d.fields == nil {
				d.fields = map[string]string{}
			}
			if _, dup := d.fields[rest[0]]; dup {
				return nil, nil, parseErr("toml: field defined twice")
			}
			if err := s.entry(); err != nil {
				return nil, nil, err
			}
			d.fields[rest[0]] = st.Value
		}
	}
}

// tomlStr is the string a raw value holds, or "" for any other value.
func tomlStr(raw string) string {
	if raw == "" || (raw[0] != '"' && raw[0] != '\'') {
		return ""
	}
	v, _ := tomlUnquote(raw)
	return v
}

var cargoDepTables = map[string]bool{"dependencies": true, "dev-dependencies": true, "build-dependencies": true, "dev_dependencies": true, "build_dependencies": true}

// parseCargoToml reads [dependencies], [dev-dependencies], [build-dependencies], their [target.X.*] forms and
// [workspace.dependencies]. A path dependency is the project's own; `package` renames; `workspace = true` takes
// the range from the workspace root, so the row has none.
func parseCargoToml(s *sink, data []byte, _ Finder) error {
	deps, _, err := tomlDeps(s, data, func(p []string) int {
		switch {
		case len(p) >= 1 && cargoDepTables[p[0]]:
			return 1
		case len(p) >= 2 && p[0] == "workspace" && p[1] == "dependencies":
			return 2
		case len(p) >= 3 && p[0] == "target" && cargoDepTables[p[2]]:
			return 3
		}
		return 0
	}, nil)
	if err != nil {
		return err
	}
	for _, d := range deps {
		row := Dependency{Name: d.name, Direct: true}
		if d.fields == nil {
			if row.Requirement = tomlStr(d.value); row.Requirement == "" {
				return parseErr("Cargo.toml: dependency value")
			}
		} else {
			if _, local := d.fields["path"]; local {
				continue
			}
			row.Requirement = tomlStr(d.fields["version"])
			if p := tomlStr(d.fields["package"]); p != "" {
				row.Name = p
			}
		}
		if err := s.emit(row); err != nil {
			return err
		}
	}
	return nil
}

// parsePyproject reads the PEP 621 arrays (project.dependencies, project.optional-dependencies, PEP 735
// dependency-groups) and poetry's dependency tables (tool.poetry.dependencies, dev-dependencies and groups),
// leaving out python itself and path dependencies.
func parsePyproject(s *sink, data []byte, _ Finder) error {
	deps, arrays, err := tomlDeps(s, data, func(p []string) int {
		switch {
		case len(p) >= 3 && p[0] == "tool" && p[1] == "poetry" && (p[2] == "dependencies" || p[2] == "dev-dependencies"):
			return 3
		case len(p) >= 5 && p[0] == "tool" && p[1] == "poetry" && p[2] == "group" && p[4] == "dependencies":
			return 5
		}
		return 0
	}, func(key string) bool {
		return key == "project.dependencies" || strings.HasPrefix(key, "project.optional-dependencies.") || strings.HasPrefix(key, "dependency-groups.")
	})
	if err != nil {
		return err
	}
	for _, a := range arrays {
		items, err := tomlArray(s, a[1])
		if err != nil {
			return err
		}
		for _, item := range items {
			req := tomlStr(item)
			if req == "" {
				continue // {include-group = "x"}
			}
			d, ok := pyRequirement(strings.TrimSpace(req))
			if !ok {
				continue
			}
			d.Version = ""
			if err := s.emit(d); err != nil {
				return err
			}
		}
	}
	for _, d := range deps {
		if strings.EqualFold(d.name, "python") {
			continue
		}
		row := Dependency{Name: d.name, Direct: true}
		switch {
		case d.fields != nil:
			if _, local := d.fields["path"]; local {
				continue
			}
			row.Requirement = tomlStr(d.fields["version"])
		case strings.HasPrefix(d.value, "["): // several constraints, by python version
		default:
			if row.Requirement = tomlStr(d.value); row.Requirement == "" {
				return parseErr("pyproject.toml: dependency value")
			}
		}
		if err := s.emit(row); err != nil {
			return err
		}
	}
	return nil
}

var (
	gemName     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	gemPathOpt  = regexp.MustCompile(`(?:^|[\s,{(])(?::path\s*=>|path:)`)
	gemCallHead = regexp.MustCompile(`^gem[ \t]+`)
)

// rubyString reads a string literal at the start of t: single-quoted or double-quoted without any escape or
// interpolation (anything else is no plain literal). It returns the content and the rest.
func rubyString(t string) (string, string, bool) {
	if t == "" || (t[0] != '"' && t[0] != '\'') {
		return "", "", false
	}
	end := strings.IndexByte(t[1:], t[0])
	if end < 0 {
		return "", "", false
	}
	v := t[1 : 1+end]
	if strings.ContainsRune(v, '\\') || strings.Contains(v, "#{") {
		return "", "", false
	}
	return v, t[2+end:], true
}

// parseGemfile reads the lines `gem "name"[, "range"...][, option: value...]`, wherever they are (groups and
// conditions are not evaluated, so a gem of any group or branch is listed). Every other line is skipped, and so is
// a gem with a path option (the project's own).
func parseGemfile(s *sink, data []byte, _ Finder) error {
	sc := newScanner(s, data)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		head := gemCallHead.FindString(line)
		if head == "" {
			continue
		}
		name, rest, ok := rubyString(line[len(head):])
		if !ok || !gemName.MatchString(name) {
			continue
		}
		var ranges []string
		plain := true
		for {
			rest = strings.TrimLeft(rest, " \t")
			if rest == "" || rest[0] == '#' {
				break
			}
			if rest[0] != ',' {
				plain = false // `gem "x" if ...`, a block, a call: not a plain line
				break
			}
			rest = strings.TrimLeft(rest[1:], " \t")
			r, after, ok := rubyString(rest)
			if !ok {
				// options: `require: false`, `git: "..."`; a path is local
				plain = !gemPathOpt.MatchString(" " + rest)
				break
			}
			ranges, rest = append(ranges, strings.TrimSpace(r)), after
		}
		if !plain {
			continue
		}
		if err := s.emit(Dependency{Name: name, Requirement: strings.Join(ranges, ", "), Direct: true}); err != nil {
			return err
		}
	}
	return scanErr(sc.Err(), "Gemfile")
}
