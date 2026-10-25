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
