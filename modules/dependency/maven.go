// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import "strings"

func init() {
	register(&Format{Ecosystem: Maven, Small: true, match: func(b string) bool { return b == "pom.xml" }, parse: parsePOM})
}

type pomDependency struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Version    string `xml:"version"`
	Scope      string `xml:"scope"`
}

type pomProject struct {
	GroupID string `xml:"groupId"`
	Version string `xml:"version"`
	Parent  struct {
		GroupID string `xml:"groupId"`
		Version string `xml:"version"`
	} `xml:"parent"`
	Properties struct {
		Items []xmlProp `xml:",any"`
	} `xml:"properties"`
	Dependencies         []pomDependency `xml:"dependencies>dependency"`
	DependencyManagement struct {
		Dependencies []pomDependency `xml:"dependencies>dependency"`
	} `xml:"dependencyManagement"`
}

// parsePOM lists the dependencies the pom itself declares. ${property} is resolved only from the same file's
// <properties> and the project's own version and group; a version a parent or a BOM would give (nothing is fetched)
// is empty, unless the same file's dependencyManagement states it. Every dependency counts as direct, test scope too.
func parsePOM(data []byte, _ Finder) ([]Dependency, error) {
	var p pomProject
	if err := decodeXML(data, &p, "pom.xml"); err != nil {
		return nil, err
	}
	props := map[string]string{}
	for _, x := range p.Properties.Items {
		props[x.XMLName.Local] = strings.TrimSpace(x.Value)
	}
	version := strings.TrimSpace(p.Version)
	if version == "" {
		version = strings.TrimSpace(p.Parent.Version)
	}
	group := strings.TrimSpace(p.GroupID)
	if group == "" {
		group = strings.TrimSpace(p.Parent.GroupID)
	}
	for _, k := range []string{"project.version", "pom.version", "version"} {
		props[k] = version
	}
	for _, k := range []string{"project.groupId", "pom.groupId", "groupId"} {
		props[k] = group
	}
	props["project.parent.version"] = strings.TrimSpace(p.Parent.Version)
	resolve := func(s string) string { return expandProps(strings.TrimSpace(s), props, "${", "}") }

	managed := map[string]string{}
	for _, d := range p.DependencyManagement.Dependencies {
		managed[resolve(d.GroupID)+":"+resolve(d.ArtifactID)] = resolve(d.Version)
	}
	var deps []Dependency
	for _, d := range p.Dependencies {
		g, a := resolve(d.GroupID), resolve(d.ArtifactID)
		if g == "" || a == "" {
			continue
		}
		v := resolve(d.Version)
		if v == "" {
			v = managed[g+":"+a]
		}
		deps = append(deps, Dependency{Name: g + ":" + a, Version: v, Direct: true})
	}
	return deps, nil
}
