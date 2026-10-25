// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import (
	"context"
	"slices"
	"sort"
	"strings"
)

func init() {
	register(&Format{Ecosystem: NPM, match: func(b string) bool { return b == "pnpm-lock.yaml" }, parse: parsePnpmLock})
}

// maxPnpmDocuments bounds the documents of one file; pnpm 10+ writes two.
const maxPnpmDocuments = 8

// pnpmDoc is what is read of one YAML document of a lockfile.
type pnpmDoc struct {
	version   string
	seen      bool
	importers int                        // the number of importers
	direct    map[string]map[string]bool // name -> the versions an importer resolves it to ("" if unknown)
	packages  []pnpmPackage
}

type pnpmPackage struct {
	key, name, version string
}

// parsePnpmLock reads lockfile versions 5, 6 and 9. pnpm writes plain block-style YAML, so the file is read line
// by line: a YAML library builds a node tree of the whole document first, which costs about a hundred times the
// size of a hostile file (flow sequences such as `[0,0,0,...]` under any key). The line reader keeps only the keys
// it needs, so its memory is bounded by the number of lines.
//
// A package is direct if an importer (the root project or a workspace project) lists it, with the version where the
// lockfile names one. pnpm 10+ can write several YAML documents: a first one for the tooling pnpm itself needs and
// then the lockfile of the project. All documents are read, except that the tooling document is left out when a
// later one has importers. Packages that are files or links on the disk are not listed.
func parsePnpmLock(ctx context.Context, data []byte, _ Finder) ([]Dependency, error) {
	sc := newScanner(ctx, data)
	var docs []*pnpmDoc
	cur := newPnpmDoc()
	docs = append(docs, cur)

	section := ""         // the top-level key: importers, packages, dependencies, ...
	var importerDeps bool // inside dependencies/devDependencies/optionalDependencies of an importer
	var depName string    // the dependency whose specifier/version lines follow
	var pkg *pnpmPackage  // the package whose name/version lines follow
	rootDeps := false     // v5: the dependency maps at the top level belong to the root project

	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "---") && strings.TrimSpace(line) == "---" {
			if cur.seen { // a leading `---` starts the first document
				if len(docs) >= maxPnpmDocuments {
					return nil, parseErr("pnpm-lock.yaml")
				}
				cur = newPnpmDoc()
				docs = append(docs, cur)
			}
			section, importerDeps, depName, pkg, rootDeps = "", false, "", nil, false
			continue
		}
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == "" || trimmed[0] == '#' || strings.TrimSpace(trimmed) == "" {
			continue
		}
		indent := len(line) - len(trimmed)
		cur.seen = true
		key, rest, ok := yamlKey(trimmed)
		if !ok {
			continue
		}
		switch {
		case indent == 0:
			section, importerDeps, depName, pkg = key, false, "", nil
			rootDeps = pnpmDepSection(key)
			if key == "lockfileVersion" {
				cur.version = unquoteYAML(rest)
			}
		case section == "importers":
			switch indent {
			case 2:
				cur.importers++
				importerDeps, depName = false, ""
			case 4:
				importerDeps, depName = pnpmDepSection(key), ""
			case 6:
				if importerDeps {
					depName = key
					if rest != "" {
						cur.addDirect(depName, rest) // v5: `name: 1.2.3`
					}
				}
			case 8:
				if importerDeps && depName != "" && key == "version" {
					cur.addDirect(depName, rest)
				}
			}
		case rootDeps && indent == 2:
			depName = key
			if rest != "" {
				cur.addDirect(key, rest) // v5: the root project's dependencies at the top level
			}
		case rootDeps && indent == 4 && key == "version" && depName != "":
			cur.addDirect(depName, rest) // v6: `version:` under the name
		case section == "packages":
			switch indent {
			case 2:
				cur.packages = append(cur.packages, pnpmPackage{key: key})
				pkg = &cur.packages[len(cur.packages)-1]
			case 4:
				if pkg != nil && key == "name" {
					pkg.name = unquoteYAML(rest)
				} else if pkg != nil && key == "version" {
					pkg.version = unquoteYAML(rest)
				}
			}
		}
	}
	if sc.Err() != nil {
		return nil, parseErr("pnpm-lock.yaml")
	}

	if len(docs) > 1 && slices.ContainsFunc(docs[1:], func(d *pnpmDoc) bool { return d.importers > 0 }) {
		docs = docs[1:]
	}
	var all []Dependency
	for _, d := range docs {
		all = append(all, d.dependencies()...)
	}
	return all, nil
}

func newPnpmDoc() *pnpmDoc { return &pnpmDoc{direct: map[string]map[string]bool{}} }

func pnpmDepSection(k string) bool {
	return k == "dependencies" || k == "devDependencies" || k == "optionalDependencies"
}

// addDirect records that an importer resolves name to value (`1.2.3`, `1.2.3(react@18)`, `link:../x`, `other@1.0.0`).
func (d *pnpmDoc) addDirect(name, value string) {
	value = unquoteYAML(value)
	if pnpmLocal(value) {
		return
	}
	version := strings.TrimPrefix(value, "/")
	if i := strings.IndexAny(version, "(_"); i >= 0 {
		version = version[:i]
	}
	if i := strings.LastIndex(version, "@"); i > 0 { // an alias: the real package is `other@1.0.0`
		name, version = version[:i], version[i+1:]
	}
	if d.direct[name] == nil {
		d.direct[name] = map[string]bool{}
	}
	d.direct[name][version] = true
}

// pnpmLocal tells a file, link or workspace reference from a registry version.
func pnpmLocal(s string) bool {
	return strings.HasPrefix(s, "link:") || strings.HasPrefix(s, "file:") || strings.HasPrefix(s, "workspace:") || strings.Contains(s, "@file:") || strings.Contains(s, "@link:")
}

func (d *pnpmDoc) dependencies() []Dependency {
	oldKeys := strings.HasPrefix(d.version, "5") // name/version instead of name@version
	pkgs := slices.Clone(d.packages)
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].key < pkgs[j].key })
	var deps []Dependency
	for _, p := range pkgs {
		if pnpmLocal(p.key) || pnpmLocal(p.version) || strings.HasPrefix(strings.TrimPrefix(p.key, "/"), "file:") {
			continue
		}
		name, version := pnpmKey(p.key, oldKeys)
		if p.name != "" {
			name = p.name
		}
		if p.version != "" {
			version = p.version
		}
		v := d.direct[name]
		deps = append(deps, Dependency{Name: name, Version: version, Direct: v != nil && (v[version] || v[""])})
	}
	return deps
}

// yamlKey splits `key: rest` (key plain or quoted) of a block mapping line. A line that is not a mapping entry
// (a sequence item) has ok == false. The rest has a trailing comment removed.
func yamlKey(s string) (key, rest string, ok bool) {
	if s[0] == '\'' || s[0] == '"' {
		q := s[0]
		i := 1
		for i < len(s) {
			if s[i] == q {
				if q == '\'' && i+1 < len(s) && s[i+1] == '\'' {
					i += 2
					continue
				}
				break
			}
			if q == '"' && s[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(s) || i+1 >= len(s) || s[i+1] != ':' {
			return "", "", false
		}
		key = unquoteYAML(s[:i+1])
		rest = s[i+2:]
	} else {
		if s[0] == '-' || s[0] == '[' || s[0] == '{' {
			return "", "", false
		}
		i := strings.Index(s, ": ")
		switch {
		case i >= 0:
			key, rest = s[:i], s[i+1:]
		case strings.HasSuffix(s, ":"):
			key = s[:len(s)-1]
		default:
			return "", "", false
		}
	}
	rest = strings.TrimSpace(rest)
	if i := strings.Index(rest, " #"); i >= 0 {
		rest = strings.TrimSpace(rest[:i])
	}
	return key, rest, true
}

// unquoteYAML removes the quotes of a single- or double-quoted scalar.
func unquoteYAML(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		switch {
		case s[0] == '\'' && s[len(s)-1] == '\'':
			return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
		case s[0] == '"' && s[len(s)-1] == '"':
			return s[1 : len(s)-1]
		}
	}
	return s
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
