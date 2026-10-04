// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package setting

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadRateLimit(t *testing.T) {
	cfg, err := NewConfigProviderFromData(``)
	require.NoError(t, err)
	loadRateLimitFrom(cfg)
	assert.False(t, RateLimit.Enabled)
	assert.True(t, RateLimit.ExemptCIDRs.IsEmpty())
	assert.Equal(t, RateLimitClass{60, 600, 5, 50}, RateLimit.Classes["api"])
	assert.Equal(t, RateLimitClass{10, 100, 0, 0}, RateLimit.Classes["api_write"])
	assert.Equal(t, RateLimitClass{1, 10, 0, 0}, RateLimit.Classes["auth"])

	cfg, err = NewConfigProviderFromData(`
[rate_limit]
ENABLED = true
EXEMPT_CIDRS = 10.0.0.0/8, 192.0.2.1
[rate_limit.git]
AUTHENTICATED_RATE = 0.5
ANONYMOUS_BURST = 3
[rate_limit.api_write]
ANONYMOUS_RATE = 99
[rate_limit.auth]
RATE = 2
BURST = 4
`)
	require.NoError(t, err)
	loadRateLimitFrom(cfg)
	assert.True(t, RateLimit.Enabled)
	assert.True(t, RateLimit.ExemptCIDRs.MatchIPAddr(net.ParseIP("10.1.2.3")))
	assert.True(t, RateLimit.ExemptCIDRs.MatchIPAddr(net.ParseIP("192.0.2.1")))
	assert.False(t, RateLimit.ExemptCIDRs.MatchIPAddr(net.ParseIP("192.0.2.2")))
	assert.Equal(t, RateLimitClass{0.5, 100, 2, 3}, RateLimit.Classes["git"])
	assert.Equal(t, RateLimitClass{10, 100, 0, 0}, RateLimit.Classes["api_write"])
	assert.Equal(t, RateLimitClass{2, 4, 0, 0}, RateLimit.Classes["auth"])
}
