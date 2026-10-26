// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package setting

import "time"

// DependencyGraph holds the dependency graph settings, [dependency_graph]: the manifests of the default branch are
// read into a list of dependencies per repository, and an SBOM can be exported from it.
var DependencyGraph = struct {
	Enabled            bool
	MaxManifestSize    int64 // bytes of one lock file; the small formats (go.mod, requirements.txt, ...) stop at 1 MiB
	MaxManifests       int   // manifests read per repository
	MaxDependencies    int   // dependencies stored per repository
	MaxTreeEntries     int   // entries of the tree a scan walks
	ScanTimeout        time.Duration
	MaxConcurrentScans int // scans running at once in this process
}{}

func loadDependencyGraphFrom(rootCfg ConfigProvider) (err error) {
	sec := rootCfg.Section("dependency_graph")
	DependencyGraph.Enabled = sec.Key("ENABLED").MustBool(false)
	DependencyGraph.MaxManifestSize = max(sec.Key("MAX_MANIFEST_SIZE").MustInt64(20<<20), 1)
	DependencyGraph.MaxManifests = max(sec.Key("MAX_MANIFESTS").MustInt(500), 1)
	DependencyGraph.MaxDependencies = max(sec.Key("MAX_DEPENDENCIES").MustInt(50000), 1)
	DependencyGraph.MaxTreeEntries = max(sec.Key("MAX_TREE_ENTRIES").MustInt(200000), 1)
	DependencyGraph.MaxConcurrentScans = max(sec.Key("MAX_CONCURRENT_SCANS").MustInt(1), 1)
	if DependencyGraph.ScanTimeout, err = sec.Key("SCAN_TIMEOUT").MustDuration(60 * time.Second); err != nil {
		return err
	}
	DependencyGraph.ScanTimeout = max(DependencyGraph.ScanTimeout, time.Second)
	return nil
}

// DependencyGraphState is stored as JSON in the "dependency_graph.enabled" system setting. An empty Mode means the
// app.ini default ([dependency_graph] ENABLED); once the switch is saved in Site administration, the stored value wins.
type DependencyGraphState struct {
	Mode        string `json:"mode"` // "on", "off" or "" (app.ini)
	UpdatedBy   int64  `json:"updated_by"`
	UpdatedUnix int64  `json:"updated_unix"`
}

// Enabled is the site switch in force. A stored value that can't be parsed or isn't known reads as off.
func (s DependencyGraphState) Enabled() bool {
	switch s.Mode {
	case "":
		return DependencyGraph.Enabled
	case "on":
		return true
	}
	return false
}
