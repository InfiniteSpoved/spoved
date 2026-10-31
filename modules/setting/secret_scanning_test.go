// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package setting

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecretScanningSettings(t *testing.T) {
	t.Cleanup(func() { require.NoError(t, loadSecretScanningFrom(mustProvider(t, ""))) })
	require.NoError(t, loadSecretScanningFrom(mustProvider(t, "")))
	assert.False(t, SecretScanning.Enabled)
	assert.False(t, SecretScanning.GenericRules)
	assert.EqualValues(t, 1, SecretScanning.MaxConcurrentScans)
	assert.Equal(t, time.Hour, SecretScanning.ScanTimeout)
	assert.EqualValues(t, 1<<20, SecretScanning.MaxBlobSize)
	assert.EqualValues(t, 2<<30, SecretScanning.MaxScanBytes)
	assert.EqualValues(t, 2_000_000, SecretScanning.MaxScanBlobs)
	assert.EqualValues(t, 10_000, SecretScanning.MaxAlertsPerRepo)
	assert.EqualValues(t, 100, SecretScanning.MaxLocationsPerAlert)

	require.NoError(t, loadSecretScanningFrom(mustProvider(t, "[secret_scanning]\nENABLED = true\nGENERIC_RULES = true\nMAX_CONCURRENT_SCANS = 99\nSCAN_TIMEOUT = 5m\nMAX_ALERTS_PER_REPO = -4\n")))
	assert.True(t, SecretScanning.Enabled)
	assert.True(t, SecretScanning.GenericRules)
	assert.EqualValues(t, 16, SecretScanning.MaxConcurrentScans, "lowered to the maximum")
	assert.Equal(t, 5*time.Minute, SecretScanning.ScanTimeout)
	assert.EqualValues(t, 1, SecretScanning.MaxAlertsPerRepo, "raised to the minimum")
}

func mustProvider(t *testing.T, ini string) ConfigProvider {
	t.Helper()
	cfg, err := NewConfigProviderFromData(ini)
	require.NoError(t, err)
	return cfg
}
