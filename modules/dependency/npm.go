// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import (
	"errors"
	"strings"

	"forgejo.org/modules/json"
)

func init() {
	register(&Format{Ecosystem: NPM, match: func(b string) bool { return b == "package-lock.json" }, parse: parseNPMLock})
}

// npmIgnored is a JSON value that is checked but not kept: only the keys of the dependency maps are used, and a
// hostile file must not cost more memory than its size.
type npmIgnored struct{}

func (*npmIgnored) UnmarshalJSON([]byte) error { return nil }

type npmDeps map[string]npmIgnored

// npmDependencyKeys are the sections of a package.json (and of a lockfile's root entry) that name dependencies.
var npmDependencyKeys = map[string]bool{"dependencies": true, "devDependencies": true, "optionalDependencies": true, "peerDependencies": true}

// parseNPMLock reads lockfileVersion 2 and 3 from "packages" (the root, the empty key, says which ones are direct)
// and version 1 from the "dependencies" tree (the direct ones come from the package.json next to the file, if any).
// The file is streamed: only the fields read are kept, each kept entry is counted, and everything else is skipped.
func parseNPMLock(s *sink, data []byte, find Finder) error {
	type entry struct {
		key, name, version, license string
		link                        bool
	}
	var pkgs []entry
	packageKeys := 0
	direct := map[string]bool{}
	addDirect := func(n string) error {
		if direct[n] {
			return nil
		}
		if err := s.entry(); err != nil {
			return err
		}
		direct[n] = true
		return nil
	}
	pkgJSON := npmPackageJSONDeps(find("package.json", false))
	var v1 []Dependency
	j := newJSONStream(s, data)
	ok, err := j.object(func(key string) error {
		switch key {
		case "packages":
			_, err := j.object(func(k string) error {
				packageKeys++
				workspace := k == "" || !strings.Contains(k, "node_modules/")
				e := entry{key: k}
				if !workspace {
					// every kept entry becomes a row (or the scan fails): the cap is the one of the rows
					if err := s.entry(); err != nil || len(pkgs) >= s.maxRows {
						return cmpOr(err, ErrTooManyDependencies)
					}
				}
				_, err := j.object(func(f string) error {
					var err error
					switch {
					case npmDependencyKeys[f] && workspace:
						err = j.keys(addDirect)
					case f == "name" && !workspace:
						e.name, err = j.str()
					case f == "version" && !workspace:
						e.version, err = j.str()
					case f == "license" && !workspace:
						e.license, err = j.license()
					case f == "link" && !workspace:
						e.link, err = j.boolean()
					default:
						err = j.skip()
					}
					return err
				})
				if err == nil && !workspace {
					pkgs = append(pkgs, e)
				}
				return err
			})
			return err
		case "dependencies":
			return readV1Tree(j, s, true, &v1, pkgJSON)
		default:
			return j.skip()
		}
	})
	if err != nil {
		if errors.Is(err, ErrTooManyDependencies) || s.ctx.Err() != nil {
			return err
		}
		return parseErr("package-lock.json")
	}
	if !ok {
		return parseErr("package-lock.json")
	}
	if packageKeys > 0 {
		for _, e := range pkgs {
			if e.link {
				continue // workspace links (their target has its own entry)
			}
			installed := e.key[strings.LastIndex(e.key, "node_modules/")+len("node_modules/"):]
			name := installed
			if e.name != "" {
				name = e.name // an alias: the entry is installed under another name
			}
			nested := strings.Count(e.key, "node_modules/") > 1
			if err := s.emit(Dependency{Name: name, Version: e.version, License: e.license, Direct: !nested && (direct[installed] || direct[name])}); err != nil {
				return err
			}
		}
		return nil
	}
	for _, d := range v1 {
		if err := s.emit(d); err != nil {
			return err
		}
	}
	return nil
}

// readV1Tree reads the "dependencies" object of lockfileVersion 1: name -> {version, dependencies: {...}}.
func readV1Tree(j *jsonStream, s *sink, top bool, out *[]Dependency, direct map[string]bool) error {
	_, err := j.object(func(name string) error {
		if err := s.entry(); err != nil || len(*out) >= s.maxRows {
			return cmpOr(err, ErrTooManyDependencies)
		}
		d := Dependency{Name: name, Direct: top && direct[name]}
		idx := len(*out)
		*out = append(*out, d)
		_, err := j.object(func(f string) error {
			switch f {
			case "version":
				v, err := j.str()
				(*out)[idx].Version = v
				return err
			case "dependencies":
				return readV1Tree(j, s, false, out, direct)
			default:
				return j.skip()
			}
		})
		return err
	})
	return err
}

// npmPackageJSONDeps returns the names a package.json declares; nil data gives an empty set.
func npmPackageJSONDeps(data []byte) map[string]bool {
	out := map[string]bool{}
	if data == nil {
		return out
	}
	var p struct {
		Dependencies         npmDeps `json:"dependencies"`
		DevDependencies      npmDeps `json:"devDependencies"`
		OptionalDependencies npmDeps `json:"optionalDependencies"`
		PeerDependencies     npmDeps `json:"peerDependencies"`
	}
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

// npmRange is a version range of a package.json; a value that isn't a string reads as empty.
type npmRange string

func (r *npmRange) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*r = npmRange(s)
	}
	return nil
}

// npmPackageJSONRanges returns what a package.json asks for: dependency name to range (the last one wins when
// several sections name it). nil data gives an empty map.
func npmPackageJSONRanges(data []byte) map[string]string {
	out := map[string]string{}
	if data == nil {
		return out
	}
	var p struct {
		Dependencies         map[string]npmRange `json:"dependencies"`
		DevDependencies      map[string]npmRange `json:"devDependencies"`
		OptionalDependencies map[string]npmRange `json:"optionalDependencies"`
		PeerDependencies     map[string]npmRange `json:"peerDependencies"`
	}
	if json.Unmarshal(data, &p) != nil {
		return out
	}
	for _, m := range []map[string]npmRange{p.Dependencies, p.DevDependencies, p.OptionalDependencies, p.PeerDependencies} {
		for n, r := range m {
			out[n] = string(r)
		}
	}
	return out
}
