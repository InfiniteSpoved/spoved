// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import (
	"slices"
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
// it needs and counts every one.
//
// The reader accepts only the subset pnpm writes, and fails closed (a parse error, never partial rows) on any
// valid YAML it would read differently from pnpm's own parser: in the sections it reads (importers, packages and
// the top-level dependency maps of v5) a key must be a plain or single/double-quoted scalar without escapes, the
// indentation is 2 spaces per level (no tabs, no jumps), and anchors, aliases, tags, merge keys, block scalars, flow
// mappings in place of a section, unterminated quotes and explicit `?` keys are refused. The one explicit key it
// understands is the `? key` / `: value` form pnpm writes for keys over 1024 characters.
//
// A package is direct if an importer (the root project or a workspace project) lists it, with the version where the
// lockfile names one. pnpm 10+ can write several YAML documents: a first one for the tooling pnpm itself needs and
// then the lockfile of the project. All documents are read, except that the tooling document is left out when a
// later one has importers. Packages that are files or links on the disk are not listed.
func parsePnpmLock(s *sink, data []byte, _ Finder) error {
	sc := newScanner(s, data)
	var docs []*pnpmDoc
	cur := newPnpmDoc()
	docs = append(docs, cur)

	section := ""         // the top-level key: importers, packages, dependencies, ...
	var importerDeps bool // inside dependencies/devDependencies/optionalDependencies of an importer
	var depName string    // the dependency whose specifier/version lines follow
	var pkg *pnpmPackage  // the package whose name/version lines follow
	rootDeps := false     // v5: the dependency maps at the top level belong to the root project
	prev := 0             // the indentation of the previous line of an interpreted section
	explicit := false     // the previous line was `? key`, its `: value` line is next
	bad := func() error { return parseErr("pnpm-lock.yaml") }

	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "---") && strings.TrimSpace(line) == "---" {
			if cur.seen { // a leading `---` starts the first document
				if len(docs) >= maxPnpmDocuments {
					return bad()
				}
				cur = newPnpmDoc()
				docs = append(docs, cur)
			}
			section, importerDeps, depName, pkg, rootDeps, prev, explicit = "", false, "", nil, false, 0, false
			continue
		}
		trimmed := strings.TrimLeft(line, " ")
		if strings.ContainsRune(line[:len(line)-len(trimmed)], '\t') || strings.HasPrefix(trimmed, "\t") {
			return bad()
		}
		if trimmed == "" || trimmed[0] == '#' || strings.TrimSpace(trimmed) == "" {
			continue
		}
		indent := len(line) - len(trimmed)
		cur.seen = true
		interpreted := indent == 0 || section == "importers" || section == "packages" || rootDeps
		// every line of the file, in every section, must be a shape pnpm writes: a quote or bracket left open on
		// any line would make a YAML reader treat the lines after it as text and see other packages than we do
		if line != strings.TrimRight(line, " \t") || (!explicit && !pnpmLineShape(trimmed)) {
			return bad()
		}
		if explicit {
			// the value of an explicit key: `: resolution: {...}` is a line at the content column (indent + 2)
			rest, ok := strings.CutPrefix(trimmed, ":")
			if !ok || indent != 2 || (rest != "" && rest[0] != ' ') {
				return bad()
			}
			explicit = false
			trimmed = strings.TrimLeft(rest, " ")
			indent, prev = 4, 4
			if trimmed == "" {
				continue
			}
		} else if indent%2 != 0 || indent > prev+2 {
			return bad()
		} else if prev = indent; interpreted && indent > pnpmReadDepth(section, rootDeps) {
			continue // inside the value of an entry: nothing here is read
		}
		if indent == 2 && section == "packages" && strings.HasPrefix(trimmed, "? ") {
			// js-yaml writes a key over 1024 characters as `? key` and the mapping after `: `
			k := strings.TrimSpace(trimmed[2:])
			if k == "" || strings.ContainsAny(k[:1], "&*!|>{[\"'%@`") && !(k[0] == '\'' || k[0] == '"') {
				return bad()
			}
			key, ok := explicitKey(k)
			if !ok {
				return bad()
			}
			if err := s.entry(); err != nil || len(cur.packages) >= s.maxRows {
				return cmpOr(err, ErrTooManyDependencies)
			}
			cur.packages = append(cur.packages, pnpmPackage{key: key})
			pkg = &cur.packages[len(cur.packages)-1]
			explicit = true
			continue
		}
		key, rest, ok := yamlKey(trimmed)
		if interpreted && (!ok || key == "" || strings.ContainsRune("?&*!|>%@`", rune(trimmed[0])) || (trimmed[0] != '\'' && trimmed[0] != '"' && strings.HasPrefix(key, "<<")) || badYAMLValue(rest)) {
			return bad()
		}
		if !ok {
			continue
		}
		switch {
		case indent == 0:
			section, importerDeps, depName, pkg = key, false, "", nil
			rootDeps = pnpmDepSection(key)
			if (section == "importers" || section == "packages" || rootDeps) && rest != "" && rest != "{}" {
				return bad() // a flow mapping, an alias or a block scalar in place of the section
			}
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
						if err := cur.addDirect(s, depName, rest); err != nil { // v5: `name: 1.2.3`
							return err
						}
					}
				}
			case 8:
				if importerDeps && depName != "" && key == "version" {
					if err := cur.addDirect(s, depName, rest); err != nil {
						return err
					}
				}
			}
		case rootDeps && indent == 2:
			depName = key
			if rest != "" {
				if err := cur.addDirect(s, key, rest); err != nil { // v5: the root project's dependencies at the top level
					return err
				}
			}
		case rootDeps && indent == 4 && key == "version" && depName != "":
			if err := cur.addDirect(s, depName, rest); err != nil { // v6: `version:` under the name
				return err
			}
		case section == "packages":
			switch indent {
			case 2:
				if rest != "" {
					return bad() // pnpm writes the fields of a package below it; a flow mapping is read differently
				}
				if err := s.entry(); err != nil || len(cur.packages) >= s.maxRows {
					return cmpOr(err, ErrTooManyDependencies)
				}
				cur.packages = append(cur.packages, pnpmPackage{key: key})
				pkg = &cur.packages[len(cur.packages)-1]
			case 4:
				if pkg != nil && (key == "name" || key == "version") {
					if escaped(rest) {
						return bad()
					}
					if key == "name" {
						pkg.name = unquoteYAML(rest)
					} else {
						pkg.version = unquoteYAML(rest)
					}
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return scanErr(err, "pnpm-lock.yaml")
	}
	if explicit {
		return bad()
	}

	if len(docs) > 1 && slices.ContainsFunc(docs[1:], func(d *pnpmDoc) bool { return d.importers > 0 }) {
		docs = docs[1:]
	}
	for _, d := range docs {
		if err := d.emitDependencies(s); err != nil {
			return err
		}
	}
	return nil
}

// pnpmLineShape reports whether a line, without its indentation, is one of the shapes pnpm writes: `key:`,
// `key: scalar`, `- scalar` or `- key: scalar`, where a scalar is plain, single-quoted (only `”` inside), double-quoted
// or a flow collection whose brackets and quotes balance on the line. Anything else (a trailing comment or space, an
// anchor, alias, tag or block scalar, a quote left open) is not.
func pnpmLineShape(t string) bool {
	if t == "-" {
		return false
	}
	if rest, ok := strings.CutPrefix(t, "- "); ok {
		t = rest
		if t == "" || t[0] == ' ' {
			return false
		}
	}
	key, raw, ok := yamlKeyRaw(t)
	if !ok {
		return pnpmValueShape(t) // a sequence item that is a bare scalar
	}
	if key == "" || strings.ContainsRune("?&*!|>%@`", rune(t[0])) {
		return false
	}
	if raw == "" {
		return true
	}
	if raw[0] != ' ' || len(raw) == 1 || raw[1] == ' ' {
		return false
	}
	return pnpmValueShape(raw[1:])
}

// pnpmValueShape checks one scalar or flow collection that fills the rest of a line.
func pnpmValueShape(v string) bool {
	switch v[0] {
	case '&', '*', '!', '|', '>', '%', '@', '`', '#', ' ':
		return false
	case '"', '\'':
		n := quotedEnd(v)
		return n == len(v)
	case '{', '[':
		return flowShape(v)
	}
	return !strings.Contains(v, " #") && !strings.Contains(v, ": ") && !strings.HasSuffix(v, ":")
}

// quotedEnd returns the length of the quoted scalar at the start of v, or -1 when it doesn't close.
func quotedEnd(v string) int {
	q := v[0]
	for i := 1; i < len(v); i++ {
		switch {
		case q == '"' && v[i] == '\\':
			i++
		case v[i] == q:
			if q == '\'' && i+1 < len(v) && v[i+1] == '\'' {
				i++
				continue
			}
			return i + 1
		}
	}
	return -1
}

// flowShape checks a flow collection on one line: brackets pair up, quoted scalars (which start only where a scalar
// does) close, and nothing follows the end.
func flowShape(v string) bool {
	var stack []byte
	atStart := true
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case atStart && (c == '"' || c == '\''):
			n := quotedEnd(v[i:])
			if n < 0 {
				return false
			}
			i += n - 1
			atStart = false
			continue
		case atStart && strings.IndexByte("&*!|>%@`", c) >= 0:
			return false
		case c == '{' || c == '[':
			if len(stack) >= 16 {
				return false
			}
			stack = append(stack, map[byte]byte{'{': '}', '[': ']'}[c])
			atStart = true
			continue
		case c == '}' || c == ']':
			if len(stack) == 0 || stack[len(stack)-1] != c {
				return false
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 && i != len(v)-1 {
				return false
			}
		case c == '#' && i > 0 && v[i-1] == ' ':
			return false
		}
		if c != ' ' {
			atStart = c == ',' || c == ':' // spaces keep it: a scalar starts after the delimiter
		}
	}
	return len(stack) == 0
}

// pnpmReadDepth is the deepest indentation of a line the reader looks at in a section.
func pnpmReadDepth(section string, rootDeps bool) int {
	switch {
	case section == "importers":
		return 8
	case section == "packages", rootDeps:
		return 4
	}
	return 0 // top-level keys
}

// escaped reports a double-quoted scalar with an escape sequence, which YAML decodes and this reader doesn't.
func escaped(v string) bool {
	return strings.HasPrefix(v, `"`) && strings.Contains(v, `\`)
}

// badYAMLValue reports a value the reader can't take at face value in the sections it reads: an anchor, an alias, a
// tag, a block scalar, or a quote or flow collection that doesn't end on the line.
func badYAMLValue(v string) bool {
	if v == "" {
		return false
	}
	switch v[0] {
	case '&', '*', '!', '|', '>':
		return true
	case '\'', '"':
		return !quoteClosed(v)
	case '{':
		return v[len(v)-1] != '}'
	case '[':
		return v[len(v)-1] != ']'
	}
	return false
}

// quoteClosed reports whether the quoted scalar at the start of v ends on the line and nothing but a comment follows.
func quoteClosed(v string) bool {
	q := v[0]
	for i := 1; i < len(v); i++ {
		switch {
		case q == '"' && v[i] == '\\':
			i++
		case v[i] == q:
			if q == '\'' && i+1 < len(v) && v[i+1] == '\'' {
				i++
				continue
			}
			rest := strings.TrimSpace(v[i+1:])
			return rest == "" || rest[0] == '#'
		}
	}
	return false
}

// explicitKey reads the key of an `? key` line: a plain scalar, or a quoted one without escapes.
func explicitKey(k string) (string, bool) {
	switch k[0] {
	case '\'', '"':
		if !quoteClosed(k) || (k[0] == '"' && strings.Contains(k, "\\")) {
			return "", false
		}
		return unquoteYAML(k[:strings.LastIndexByte(k, k[0])+1]), true
	}
	if i := strings.Index(k, " #"); i >= 0 {
		k = strings.TrimSpace(k[:i])
	}
	return k, k != ""
}

func newPnpmDoc() *pnpmDoc { return &pnpmDoc{direct: map[string]map[string]bool{}} }

func pnpmDepSection(k string) bool {
	return k == "dependencies" || k == "devDependencies" || k == "optionalDependencies"
}

// addDirect records that an importer resolves name to value (`1.2.3`, `1.2.3(react@18)`, `link:../x`, `other@1.0.0`).
func (d *pnpmDoc) addDirect(s *sink, name, value string) error {
	if escaped(value) {
		return parseErr("pnpm-lock.yaml")
	}
	value = unquoteYAML(value)
	if pnpmLocal(value) {
		return nil
	}
	version := strings.TrimPrefix(value, "/")
	if i := strings.IndexAny(version, "(_"); i >= 0 {
		version = version[:i]
	}
	if i := strings.LastIndex(version, "@"); i > 0 { // an alias: the real package is `other@1.0.0`
		name, version = version[:i], version[i+1:]
	}
	if d.direct[name] == nil {
		if err := s.entry(); err != nil {
			return err
		}
		d.direct[name] = map[string]bool{}
	}
	if !d.direct[name][version] {
		if err := s.entry(); err != nil {
			return err
		}
		d.direct[name][version] = true
	}
	return nil
}

// pnpmLocal tells a file, link or workspace reference from a registry version.
func pnpmLocal(s string) bool {
	return strings.HasPrefix(s, "link:") || strings.HasPrefix(s, "file:") || strings.HasPrefix(s, "workspace:") || strings.Contains(s, "@file:") || strings.Contains(s, "@link:")
}

func (d *pnpmDoc) emitDependencies(s *sink) error {
	oldKeys := strings.HasPrefix(d.version, "5") // name/version instead of name@version
	for _, p := range d.packages {
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
		// a git or tarball importer dependency names its source in the importer; the package key has the version
		pkgVersionKey := ""
		if _, kv := pnpmKey(p.key, oldKeys); kv != "" {
			pkgVersionKey = kv
		}
		direct := v != nil && (v[version] || v[""] || (pkgVersionKey != "" && v[pkgVersionKey]))
		if err := s.emit(Dependency{Name: name, Version: version, Direct: direct}); err != nil {
			return err
		}
	}
	return nil
}

// yamlKeyRaw is yamlKey without trimming: rest is what follows the key's colon exactly as written (" value", or "").
func yamlKeyRaw(s string) (key, rest string, ok bool) {
	if s[0] == '\'' || s[0] == '"' {
		n := quotedEnd(s)
		if n < 0 || n >= len(s) || s[n] != ':' {
			return "", "", false
		}
		return unquoteYAML(s[:n]), s[n+1:], true
	}
	if s[0] == '-' || s[0] == '[' || s[0] == '{' {
		return "", "", false
	}
	if i := strings.Index(s, ": "); i >= 0 {
		return s[:i], s[i+1:], true
	}
	if k, ok := strings.CutSuffix(s, ":"); ok {
		return k, "", true
	}
	return "", "", false
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
		if q == '"' && strings.Contains(s[:i+1], "\\") {
			return "", "", false // an escape: YAML decodes it, this reader doesn't
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
