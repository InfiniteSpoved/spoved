// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import (
	"bytes"
	"sort"
	"strings"

	"forgejo.org/modules/json"
)

func init() {
	register(&Format{Ecosystem: NuGet, Small: true, match: func(b string) bool { return strings.HasSuffix(b, ".csproj") && len(b) > len(".csproj") }, parse: parseCsproj})
	register(&Format{Ecosystem: NuGet, match: func(b string) bool { return b == "packages.lock.json" }, parse: parseNuGetLock})
}

type msbuildPackage struct {
	Include         string `xml:"Include,attr"`
	Version         string `xml:"Version,attr"`
	VersionOverride string `xml:"VersionOverride,attr"`
	VersionChild    string `xml:"Version"`
}

type msbuildProject struct {
	PropertyGroups []struct {
		Items []xmlProp `xml:",any"`
	} `xml:"PropertyGroup"`
	ItemGroups []struct {
		PackageReferences []msbuildPackage `xml:"PackageReference"`
		PackageVersions   []msbuildPackage `xml:"PackageVersion"`
	} `xml:"ItemGroup"`
}

func (p *msbuildProject) props() map[string]string {
	m := map[string]string{}
	for _, g := range p.PropertyGroups {
		for _, x := range g.Items {
			m[x.XMLName.Local] = strings.TrimSpace(x.Value)
		}
	}
	return m
}

func (m msbuildPackage) version() string {
	if m.Version != "" {
		return m.Version
	}
	return m.VersionChild
}

// parseCsproj lists the PackageReference items. A reference without a version takes it from the nearest
// Directory.Packages.props (central package management) above the project; VersionOverride wins. $(Property) is
// resolved from the same file's properties, else the version is empty.
func parseCsproj(data []byte, find Finder) ([]Dependency, error) {
	var proj msbuildProject
	if err := decodeXML(data, &proj, "csproj"); err != nil {
		return nil, err
	}
	props := proj.props()
	central := map[string]string{}
	if cd := find("Directory.Packages.props", true); cd != nil {
		var cp msbuildProject
		if decodeXML(cd, &cp, "Directory.Packages.props") == nil { // a broken props file only costs the versions
			cprops := cp.props()
			for _, g := range cp.ItemGroups {
				for _, v := range g.PackageVersions {
					central[strings.ToLower(v.Include)] = expandProps(strings.TrimSpace(v.version()), cprops, "$(", ")")
				}
			}
		}
	}
	var deps []Dependency
	for _, g := range proj.ItemGroups {
		for _, r := range g.PackageReferences {
			name := strings.TrimSpace(r.Include)
			if name == "" {
				continue
			}
			v := strings.TrimSpace(r.VersionOverride)
			if v == "" {
				v = strings.TrimSpace(r.version())
			}
			if v == "" {
				v = central[strings.ToLower(name)]
			} else {
				v = expandProps(v, props, "$(", ")")
			}
			deps = append(deps, Dependency{Name: name, Version: v, Direct: true})
		}
	}
	return deps, nil
}

type nugetLock struct {
	Dependencies map[string]map[string]struct {
		Type     string `json:"type"`
		Resolved string `json:"resolved"`
	} `json:"dependencies"`
}

// parseNuGetLock reads packages.lock.json: type Direct is direct, Transitive is not; "Project" entries (other
// projects of the solution) are not packages. A package in several target frameworks is one row.
func parseNuGetLock(data []byte, _ Finder) ([]Dependency, error) {
	var l nugetLock
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&l); err != nil {
		return nil, parseErr("packages.lock.json")
	}
	fws := make([]string, 0, len(l.Dependencies))
	for k := range l.Dependencies {
		fws = append(fws, k)
	}
	sort.Strings(fws)
	var deps []Dependency
	for _, fw := range fws {
		names := make([]string, 0, len(l.Dependencies[fw]))
		for n := range l.Dependencies[fw] {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			d := l.Dependencies[fw][n]
			if d.Type == "Project" {
				continue
			}
			deps = append(deps, Dependency{Name: n, Version: d.Resolved, Direct: d.Type == "Direct"})
		}
	}
	return deps, nil
}
