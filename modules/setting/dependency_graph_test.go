// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package setting

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDependencyGraphSettings(t *testing.T) {
	cfg, err := NewConfigProviderFromData("")
	assert.NoError(t, err)
	assert.NoError(t, loadDependencyGraphFrom(cfg))
	assert.False(t, DependencyGraph.Enabled)
	assert.EqualValues(t, 20<<20, DependencyGraph.MaxManifestSize)
	assert.Equal(t, 500, DependencyGraph.MaxManifests)
	assert.Equal(t, 50000, DependencyGraph.MaxDependencies)
	assert.Equal(t, 200000, DependencyGraph.MaxTreeEntries)
	assert.Equal(t, 60*time.Second, DependencyGraph.ScanTimeout)

	cfg, err = NewConfigProviderFromData("[dependency_graph]\nENABLED = true\nMAX_MANIFESTS = 3\nSCAN_TIMEOUT = 5s\nMAX_DEPENDENCIES = -4\n")
	assert.NoError(t, err)
	assert.NoError(t, loadDependencyGraphFrom(cfg))
	assert.True(t, DependencyGraph.Enabled)
	assert.Equal(t, 3, DependencyGraph.MaxManifests)
	assert.Equal(t, 5*time.Second, DependencyGraph.ScanTimeout)
	assert.Equal(t, 1, DependencyGraph.MaxDependencies)
	DependencyGraph.Enabled = false
}

func TestOSVMirrorSettings(t *testing.T) {
	load := func(ini string) error {
		cfg, err := NewConfigProviderFromData(ini)
		assert.NoError(t, err)
		return loadDependencyGraphFrom(cfg)
	}
	t.Cleanup(func() { assert.NoError(t, load("")) })

	assert.NoError(t, load(""))
	assert.Equal(t, DefaultOSVMirrorURL, DependencyGraph.OSVMirrorURL)
	assert.Equal(t, 24*time.Hour, DependencyGraph.UpdateInterval)

	assert.NoError(t, load("[dependency_graph]\nOSV_MIRROR_URL = http://mirror.example:8080/osv/\nUPDATE_INTERVAL = 1m\n"))
	assert.Equal(t, "http://mirror.example:8080/osv", DependencyGraph.OSVMirrorURL)
	assert.Equal(t, time.Hour, DependencyGraph.UpdateInterval, "raised to the minimum")

	assert.NoError(t, load("[dependency_graph]\nOSV_MIRROR_URL =\nUPDATE_INTERVAL = 9000h\n"))
	assert.Empty(t, DependencyGraph.OSVMirrorURL, "empty: no download")
	assert.Equal(t, MaxUpdateInterval, DependencyGraph.UpdateInterval)

	for _, bad := range []string{
		"ftp://mirror.example/", "https://user:pw@mirror.example/", "https://mirror.example/?a=b", "https:///x", "mirror.example", "http://[::1",
	} {
		assert.Error(t, load("[dependency_graph]\nOSV_MIRROR_URL = "+bad+"\n"), bad)
	}
}
