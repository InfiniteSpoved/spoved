// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import (
	"context"
	"maps"
	"strings"
)

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
		GroupID      string `xml:"groupId"`
		ArtifactID   string `xml:"artifactId"`
		Version      string `xml:"version"`
		RelativePath string `xml:"relativePath"`
	} `xml:"parent"`
	Properties struct {
		Items []xmlProp `xml:",any"`
	} `xml:"properties"`
	Dependencies         []pomDependency `xml:"dependencies>dependency"`
	DependencyManagement struct {
		Dependencies []pomDependency `xml:"dependencies>dependency"`
	} `xml:"dependencyManagement"`
}

// pomScope is what a pom.xml offers to the dependencies below it: its properties and managed versions.
type pomScope struct {
	props   map[string]string
	managed map[string]string
}

// newPomScope reads the properties and the managed versions of a pom; the ones of the parent pom (if any) are the
// base the pom's own override.
func newPomScope(p *pomProject, parent *pomScope) *pomScope {
	sc := &pomScope{props: map[string]string{}, managed: map[string]string{}}
	if parent != nil {
		maps.Copy(sc.props, parent.props)
		maps.Copy(sc.managed, parent.managed)
	}
	for _, x := range p.Properties.Items {
		sc.props[x.XMLName.Local] = strings.TrimSpace(x.Value)
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
		sc.props[k] = version
	}
	for _, k := range []string{"project.groupId", "pom.groupId", "groupId"} {
		sc.props[k] = group
	}
	sc.props["project.parent.version"] = strings.TrimSpace(p.Parent.Version)
	for _, d := range p.DependencyManagement.Dependencies {
		sc.managed[sc.resolve(d.GroupID)+":"+sc.resolve(d.ArtifactID)] = sc.resolve(d.Version)
	}
	return sc
}

func (sc *pomScope) resolve(s string) string {
	return expandProps(strings.TrimSpace(s), sc.props, "${", "}")
}

// parsePOM reads the dependencies a pom.xml declares (the test scope too: the file doesn't say what ships). A
// version is a literal or a `${property}` of the file, of `project.version`, of the managed versions or, one level
// up, of the parent pom in the same repository (the pom the child's <relativePath> points at, ../pom.xml by
// default). Anything else gives an empty version; nothing is fetched.
func parsePOM(ctx context.Context, data []byte, find Finder) ([]Dependency, error) {
	var p pomProject
	if err := decodeXML(data, &p, "pom.xml"); err != nil {
		return nil, err
	}
	var parent *pomScope
	if strings.TrimSpace(p.Parent.ArtifactID) != "" {
		rel := strings.TrimSpace(p.Parent.RelativePath)
		switch {
		case rel == "":
			rel = "../pom.xml"
		case !strings.HasSuffix(rel, ".xml"):
			rel = strings.TrimSuffix(rel, "/") + "/pom.xml"
		}
		if pd := find(rel, false); pd != nil {
			var pp pomProject
			if decodeXML(pd, &pp, "pom.xml") == nil {
				parent = newPomScope(&pp, nil)
			}
		}
	}
	sc := newPomScope(&p, parent)
	var deps []Dependency
	for _, d := range p.Dependencies {
		g, a := sc.resolve(d.GroupID), sc.resolve(d.ArtifactID)
		if g == "" || a == "" {
			continue
		}
		v := sc.resolve(d.Version)
		if v == "" {
			v = sc.managed[g+":"+a]
		}
		deps = append(deps, Dependency{Name: g + ":" + a, Version: v, Direct: true})
	}
	return deps, nil
}
