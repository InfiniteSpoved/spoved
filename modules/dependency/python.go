// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import (
	"regexp"
	"strings"
)

func init() {
	register(&Format{Ecosystem: PyPI, Small: true, match: isRequirementsName, parse: parseRequirements})
	register(&Format{Ecosystem: PyPI, match: func(b string) bool { return b == "poetry.lock" }, parse: parsePoetryLock})
}

var pyName = `[A-Za-z0-9][A-Za-z0-9._-]*`

// a requirement: name, optional [extras], then nothing or a version specifier (markers and options are cut before)
var reqLine = regexp.MustCompile(`^(` + pyName + `)\s*(?:\[[^\]]*\])?\s*(.*)$`)

var reqPin = regexp.MustCompile(`^===?\s*([A-Za-z0-9][A-Za-z0-9._+!-]*)$`)

// isRequirementsName is requirements.txt, requirements-dev.txt, dev-requirements.txt and the like.
func isRequirementsName(b string) bool {
	return strings.HasSuffix(b, ".txt") && (strings.HasPrefix(b, "requirements") || strings.HasSuffix(b, "-requirements.txt"))
}

// parseRequirements reads the requirements of a pip file. A requirement pinned to one version (`name==1.2.3`, also
// with a marker or a --hash option on the line) is stored with that version; one with a range or a bare name is
// stored with an empty version, since the file doesn't fix it. Options (`-r`, `-c`, `--index-url`, ...), URLs and
// comments are ignored: nothing is followed or fetched.
func parseRequirements(s *sink, data []byte, _ Finder) error {
	sc := newScanner(s, data)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, " #"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), "\\"))
		if line == "" || line[0] == '#' || line[0] == '-' {
			continue
		}
		if i := strings.Index(line, ";"); i >= 0 { // environment marker
			line = strings.TrimSpace(line[:i])
		}
		if i := strings.Index(line, " --"); i >= 0 { // per-requirement options such as --hash
			line = strings.TrimSpace(line[:i])
		}
		m := reqLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		spec := strings.TrimSpace(m[2])
		d := Dependency{Name: m[1], Direct: true}
		switch {
		case spec == "":
		case spec[0] == '<' || spec[0] == '>' || spec[0] == '!' || spec[0] == '~' || spec[0] == '=':
			if p := reqPin.FindStringSubmatch(spec); p != nil && !strings.Contains(p[1], "*") {
				d.Version = p[1]
			}
		default:
			continue // `name @ url`, a path, a URL
		}
		if requirementIsFile(d.Name) {
			continue
		}
		if err := s.emit(d); err != nil {
			return err
		}
	}
	return scanErr(sc.Err(), "requirements.txt")
}

// requirementIsFile tells a local archive or wheel (pip accepts `foo-1.0-py3-none-any.whl` and `./x.tar.gz`) from a
// package name.
func requirementIsFile(name string) bool {
	l := strings.ToLower(name)
	for _, ext := range []string{".whl", ".tar.gz", ".tgz", ".zip", ".tar.bz2", ".tar.xz", ".egg"} {
		if strings.HasSuffix(l, ext) {
			return true
		}
	}
	return false
}

// parsePoetryLock reads the [[package]] tables of poetry.lock. The lock file doesn't say what the project asks for,
// so the direct packages come from the pyproject.toml next to it, if there is one.
func parsePoetryLock(s *sink, data []byte, find Finder) error {
	var direct map[string]bool
	if pp := find("pyproject.toml", false); pp != nil {
		direct, _ = helperParsed(s.ctx, "pyproject.toml", pp, func() (map[string]bool, int64) {
			m := pyprojectDeps(s, pp)
			return m, int64(len(m)) * 96
		})
	}
	var cur *Dependency
	inPackage, inSource, onDisk := false, false, false
	flush := func() error {
		var err error
		if cur != nil && cur.Name != "" && !onDisk {
			cur.Direct = direct[normalizePyName(cur.Name)]
			err = s.emit(*cur)
		}
		cur, onDisk = nil, false
		return err
	}
	sc := newScanner(s, data)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "[[package]]":
			if err := flush(); err != nil {
				return err
			}
			cur, inPackage, inSource = &Dependency{}, true, false
		case strings.HasPrefix(line, "["):
			inPackage, inSource = false, line == "[package.source]" // [package.dependencies], [metadata], ...
		case inSource && cur != nil:
			if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "type" {
				// a package from a directory or a file on the disk is part of the project, not a dependency
				onDisk = onDisk || tomlString(v) == "directory" || tomlString(v) == "file"
			}
		case inPackage && cur != nil:
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			switch strings.TrimSpace(k) {
			case "name":
				cur.Name = tomlString(v)
			case "version":
				cur.Version = tomlString(v)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return scanErr(err, "poetry.lock")
	}
	return flush()
}

func tomlString(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return ""
}

func normalizePyName(n string) string {
	return strings.ToLower(strings.NewReplacer("_", "-", ".", "-").Replace(n))
}

var pepArray = regexp.MustCompile(`^([A-Za-z0-9_.-]+)\s*=\s*\[(.*)$`)

// pyprojectDeps returns the normalized names a pyproject.toml asks for: the keys of [tool.poetry.dependencies] and
// of its groups, and the entries of the PEP 621 `dependencies` and `optional-dependencies` arrays.
func pyprojectDeps(s *sink, data []byte) map[string]bool {
	out := map[string]bool{}
	if data == nil {
		return out
	}
	section, inArray := "", false
	sc := newScanner(s, data)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		if line[0] == '[' {
			section, inArray = strings.Trim(line, "[] "), false
			continue
		}
		poetry := section == "tool.poetry.dependencies" || section == "tool.poetry.dev-dependencies" ||
			(strings.HasPrefix(section, "tool.poetry.group.") && strings.HasSuffix(section, ".dependencies"))
		switch {
		case poetry:
			if k, _, ok := strings.Cut(line, "="); ok {
				if k = strings.Trim(strings.TrimSpace(k), `"'`); k != "" && k != "python" {
					if len(out) < s.maxEntries {
						out[normalizePyName(k)] = true
					}
				}
			}
		case section == "project" || section == "project.optional-dependencies" || section == "dependency-groups":
			if !inArray {
				m := pepArray.FindStringSubmatch(line)
				if m == nil || (section == "project" && m[1] != "dependencies") {
					continue
				}
				inArray, line = true, m[2]
			}
			// {include-group = "test"} names a group of requirements, not a package
			for _, m := range pepAll.FindAllStringSubmatch(tomlInline.ReplaceAllString(line, ""), -1) {
				if len(out) < s.maxEntries {
					out[normalizePyName(m[1])] = true
				}
			}
			if strings.Contains(line, "]") {
				inArray = false
			}
		}
	}
	return out
}

var tomlInline = regexp.MustCompile(`\{[^}]*\}`)

var pepAll = regexp.MustCompile(`"(` + pyName + `)`)
