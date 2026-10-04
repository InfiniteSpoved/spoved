// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package ratelimit

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"forgejo.org/modules/hostmatcher"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setup(t *testing.T, classes map[string]setting.RateLimitClass) *time.Time {
	t.Helper()
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t.Cleanup(test.MockVariableValue(&setting.RateLimit.Enabled, true))
	t.Cleanup(test.MockVariableValue(&setting.RateLimit.Classes, classes))
	t.Cleanup(test.MockVariableValue(&setting.RateLimit.ExemptCIDRs, hostmatcher.ParseHostMatchList("", "192.0.2.0/24")))
	t.Cleanup(test.MockVariableValue(&now, func() time.Time { return clock }))
	ResetForTest()
	t.Cleanup(ResetForTest)
	return &clock
}

func TestCheck(t *testing.T) {
	clock := setup(t, map[string]setting.RateLimitClass{API: {AuthenticatedRate: 2, AuthenticatedBurst: 3}})

	for i := 2; i >= 0; i-- {
		r := Check(API, "192.0.2.1:1", "u:1", true)
		assert.True(t, r.Allowed)
		assert.Equal(t, 3, r.Limit)
		assert.Equal(t, i, r.Remaining)
	}
	r := Check(API, "192.0.2.1:1", "u:1", true)
	assert.False(t, r.Allowed)
	assert.Equal(t, 0, r.Remaining)
	assert.Equal(t, 500*time.Millisecond, r.RetryAfter)
	assert.Equal(t, 1, r.RetryAfterSeconds())
	assert.Equal(t, 1500*time.Millisecond, r.Reset)

	// other keys have their own bucket
	assert.True(t, Check(API, "192.0.2.1:1", "u:2", true).Allowed)

	// refill at 2 tokens/s, capped at the burst
	*clock = clock.Add(500 * time.Millisecond)
	assert.True(t, Check(API, "192.0.2.1:1", "u:1", true).Allowed)
	assert.False(t, Check(API, "192.0.2.1:1", "u:1", true).Allowed)
	*clock = clock.Add(time.Hour)
	assert.Equal(t, 2, Check(API, "192.0.2.1:1", "u:1", true).Remaining)

	// anonymous half is off (rate 0), unknown classes are off
	for range 10 {
		assert.True(t, Check(API, "192.0.2.1:1", "ip:1.2.3.4", false).Allowed)
		assert.Equal(t, 0, Check(Web, "192.0.2.1:1", "u:1", true).Limit)
	}
}

func TestCheckDisabled(t *testing.T) {
	setup(t, map[string]setting.RateLimitClass{API: {AuthenticatedRate: 1, AuthenticatedBurst: 1, AnonymousRate: 1, AnonymousBurst: 1}})
	defer test.MockVariableValue(&setting.RateLimit.Enabled, false)()
	for range 5 {
		assert.Equal(t, Result{Allowed: true}, Check(API, "192.0.2.1:1", "u:1", true))
	}
}

func TestStartAuth(t *testing.T) {
	clock := setup(t, map[string]setting.RateLimitClass{Auth: {AuthenticatedRate: 1, AuthenticatedBurst: 3}})
	alice, bob := UserKey("alice"), UserKey("bob")

	// successful attempts never consume
	for range 10 {
		a := StartAuth("1.1.1.1:1234", alice)
		assert.True(t, a.Allowed)
		a.Succeeded()
	}

	// failed attempts do, per username ...
	for range 3 {
		require.NoError(t, StartAuth("1.1.1.1:1234", alice).Err())
	}
	err := StartAuth("2.2.2.2:1234", UserKey(" ALICE ")).Err()
	var limited *LimitedError
	assert.True(t, errors.As(fmt.Errorf("joined: %w", err), &limited))
	assert.Equal(t, time.Second, limited.RetryAfter)

	// ... and per IP: the exhausted IP blocks other usernames too, without touching their bucket
	assert.False(t, StartAuth("1.1.1.1:1234", bob).Allowed)
	for range 3 {
		assert.True(t, StartAuth("3.3.3.3:1234", bob).Allowed)
	}
	assert.False(t, StartAuth("4.4.4.4:1234", bob).Allowed)

	// a rejected attempt takes nothing, so the IP recovers after one refill
	*clock = clock.Add(time.Second)
	assert.True(t, StartAuth("1.1.1.1:1234", UserKey("carol")).Allowed)

	// exempt clients are never limited
	for range 10 {
		assert.True(t, StartAuth("192.0.2.7:1234", alice).Allowed)
	}
}

func TestIPKey(t *testing.T) {
	assert.Equal(t, "ip:192.0.2.1", IPKey("192.0.2.1:1234"))
	assert.Equal(t, "ip:192.0.2.1", IPKey("192.0.2.1"))
	assert.NotEqual(t, IPKey("192.0.2.1:1"), IPKey("192.0.2.2:1"), "IPv4 clients must not share a key")
	assert.Equal(t, "ip:192.0.2.1", IPKey("[::ffff:192.0.2.1]:1"))
	assert.Equal(t, "ip:2001:db8:1:2::/64", IPKey("[2001:db8:1:2:aaaa::1]:1"))
	assert.Equal(t, IPKey("[2001:db8:1:2::1]:1"), IPKey("[2001:db8:1:2:ffff:ffff:ffff:ffff]:1"))
	assert.NotEqual(t, IPKey("[2001:db8:1:2::1]:1"), IPKey("[2001:db8:1:3::1]:1"))
	assert.Equal(t, "ip:invalid", IPKey("1.2.3.4, 5.6.7.8"))
	assert.Equal(t, "ip:invalid", IPKey("@"))
}

func TestUserKey(t *testing.T) {
	assert.Equal(t, UserKey("Alice "), UserKey("alice"))
	assert.NotEqual(t, UserKey("alice"), UserKey("bob"))
	assert.Len(t, UserKey(string(make([]byte, 1<<20))), len("user:")+64)
}

func TestSweepAndOverflow(t *testing.T) {
	clock := setup(t, map[string]setting.RateLimitClass{Web: {AnonymousRate: 1, AnonymousBurst: 2}})
	s := getStore(Web, false)

	for i := range maxKeys {
		Check(Web, "203.0.113.1:1", fmt.Sprint("ip:", i), false)
	}
	assert.Len(t, s.buckets, maxKeys)

	// the map is full and nothing has refilled yet: new IP keys share an overflow bucket per client network
	anon := func(addr string) bool { return Check(Web, addr, IPKey(addr), false).Allowed }
	assert.True(t, anon("198.51.100.1:1"))
	assert.True(t, anon("198.51.100.2:1"))
	assert.False(t, anon("198.51.100.3:1"), "same /24 shares the overflow bucket")
	assert.True(t, anon("192.0.2.1:1"), "other /24 has its own overflow bucket")
	assert.True(t, anon("[2001:db8:1:2::1]:1"))
	assert.True(t, anon("[2001:db8:1:3::1]:1"))
	assert.False(t, anon("[2001:db8:1:4::1]:1"), "same /48 shares the overflow bucket")
	assert.True(t, anon("[2001:db8:2::1]:1"))
	assert.Contains(t, s.buckets, "overflow:198.51.100.0/24")
	assert.Contains(t, s.buckets, "overflow:2001:db8:1::/48")
	assert.NotContains(t, s.buckets, "ip:198.51.100.1")
	// existing keys keep their own bucket
	assert.Equal(t, 0, Check(Web, "198.51.100.1:1", "ip:1", false).Remaining)

	// once buckets are full again, the next new key sweeps them; partly used buckets stay
	*clock = clock.Add(2 * time.Second)
	Check(Web, "192.0.2.1:1", "ip:1", false)
	Check(Web, "192.0.2.1:1", "ip:1", false)
	assert.True(t, anon("192.0.2.7:1"))
	assert.Len(t, s.buckets, 2)
	assert.Contains(t, s.buckets, "ip:1")
	assert.Contains(t, s.buckets, "ip:192.0.2.7")
}

func TestOverflowKeepsUserKeys(t *testing.T) {
	clock := setup(t, map[string]setting.RateLimitClass{Auth: {AuthenticatedRate: 0.0001, AuthenticatedBurst: 2}})
	a := getStore(Auth, true)
	for i := range maxKeys {
		a.buckets[fmt.Sprint("ip:", i)] = &bucket{tokens: 0, last: *clock}
	}
	a.lastSweep = *clock
	alice := UserKey("alice")

	// the map is full: the IPs fall back to their overflow bucket, but alice keeps her own bucket past the cap
	require.True(t, StartAuth("198.51.100.1:1", alice).Allowed)
	require.True(t, StartAuth("100.64.0.1:1", alice).Allowed)
	assert.Contains(t, a.buckets, alice)
	assert.InDelta(t, 0.0, a.buckets[alice].tokens, 0.001)
	assert.False(t, StartAuth("203.0.113.1:1", alice).Allowed, "a fresh network is still limited by the user bucket")

	// a successful attempt gives back exactly what it took
	attempt := StartAuth("203.0.113.1:1", UserKey("bob"))
	require.True(t, attempt.Allowed)
	assert.InDelta(t, 1.0, a.buckets["overflow:203.0.113.0/24"].tokens, 0.001)
	attempt.Succeeded()
	assert.InDelta(t, 2.0, a.buckets["overflow:203.0.113.0/24"].tokens, 0.001)
	assert.InDelta(t, 2.0, a.buckets[UserKey("bob")].tokens, 0.001)

	// user keys aren't created when the client's network is exhausted
	require.True(t, StartAuth("198.51.100.2:1", UserKey("erin")).Allowed)
	require.False(t, StartAuth("198.51.100.3:1", UserKey("carol")).Allowed)
	assert.NotContains(t, a.buckets, UserKey("carol"))
}
