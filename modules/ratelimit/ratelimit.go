// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// Package ratelimit implements in-memory token buckets for incoming traffic, configured by [rate_limit].
package ratelimit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"forgejo.org/modules/setting"
)

// Traffic classes, matching the [rate_limit.<class>] sections.
const (
	API      = "api"
	APIWrite = "api_write"
	Git      = "git"
	Web      = "web"
	Auth     = "auth"
)

const maxKeys = 100_000

// Result describes the state of the bucket that decided a request. Limit is 0 when no limit applies.
type Result struct {
	Allowed    bool
	Limit      int
	Remaining  int
	RetryAfter time.Duration
	Reset      time.Duration
}

// RetryAfterSeconds is RetryAfter rounded up to whole seconds, at least 1.
func (r Result) RetryAfterSeconds() int {
	return max(1, int(math.Ceil(r.RetryAfter.Seconds())))
}

// LimitedError is returned by authentication methods when the auth class rejects an attempt.
type LimitedError struct {
	Result
}

func (e *LimitedError) Error() string {
	return fmt.Sprintf("rate limit exceeded, retry in %ds", e.RetryAfterSeconds())
}

type bucket struct {
	tokens float64
	last   time.Time
}

// ponytail: one mutex per class store, shard it if lock contention ever shows up in profiles
type store struct {
	mu        sync.Mutex
	rate      float64
	burst     int
	buckets   map[string]*bucket
	lastSweep time.Time
}

var (
	storesMu sync.Mutex
	stores   = map[string]*store{}
	now      = time.Now
)

// getStore returns the store of a class half, or nil when it is not limited.
func getStore(class string, authenticated bool) *store {
	if !setting.RateLimit.Enabled {
		return nil
	}
	c := setting.RateLimit.Classes[class]
	rate, burst, name := c.AuthenticatedRate, c.AuthenticatedBurst, class
	if !authenticated && class != Auth {
		rate, burst, name = c.AnonymousRate, c.AnonymousBurst, class+"/anonymous"
	}
	if rate <= 0 || burst <= 0 {
		return nil
	}
	storesMu.Lock()
	defer storesMu.Unlock()
	s, ok := stores[name]
	if !ok {
		s = &store{rate: rate, burst: burst, buckets: map[string]*bucket{}}
		stores[name] = s
	}
	return s
}

// get returns the refilled bucket of key for a client. The caller holds s.mu.
func (s *store) get(key, remoteAddr string, t time.Time) *bucket {
	b, ok := s.buckets[key]
	if !ok {
		// A full bucket is the same as a missing one, so dropping full buckets loses nothing. Sweep once a minute,
		// or once a second while the map is full so that a flood of new keys can't make every request a full scan.
		if t.Sub(s.lastSweep) >= time.Minute || (len(s.buckets) >= maxKeys && t.Sub(s.lastSweep) >= time.Second) {
			s.sweep(t)
		}
		// When the map is still full, new keys share a bucket per network of the client, so that whoever filled
		// the map only limits their own network. These keys may exceed the cap.
		if len(s.buckets) >= maxKeys {
			key = overflowKey(remoteAddr)
			b, ok = s.buckets[key]
		}
		if !ok {
			b = &bucket{tokens: float64(s.burst), last: t}
			s.buckets[key] = b
		}
	}
	b.tokens = min(float64(s.burst), b.tokens+t.Sub(b.last).Seconds()*s.rate)
	b.last = t
	return b
}

func (s *store) sweep(t time.Time) {
	for k, b := range s.buckets {
		if b.tokens+t.Sub(b.last).Seconds()*s.rate >= float64(s.burst) {
			delete(s.buckets, k)
		}
	}
	s.lastSweep = t
}

func (s *store) result(b *bucket, allowed bool) Result {
	r := Result{
		Allowed:   allowed,
		Limit:     s.burst,
		Remaining: int(b.tokens),
		Reset:     time.Duration((float64(s.burst) - b.tokens) / s.rate * float64(time.Second)),
	}
	if !allowed {
		r.RetryAfter = time.Duration((1 - b.tokens) / s.rate * float64(time.Second))
	}
	return r
}

// Check takes one token from the bucket of key in a traffic class, for a client at remoteAddr.
func Check(class, remoteAddr, key string, authenticated bool) Result {
	s := getStore(class, authenticated)
	if s == nil {
		return Result{Allowed: true}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.get(key, remoteAddr, now())
	if b.tokens < 1 {
		return s.result(b, false)
	}
	b.tokens--
	return s.result(b, true)
}

// AuthAttempt is a credential check counted against the auth class.
type AuthAttempt struct {
	Result
	buckets []*bucket
}

// StartAuth takes one token from the auth buckets of the client IP and of each key before a credential check, so
// that parallel attempts can't overshoot the limit. It takes nothing when a bucket is empty. Call Succeeded after a
// successful check to give the tokens back: only failed attempts count.
func StartAuth(remoteAddr string, keys ...string) *AuthAttempt {
	s := getStore(Auth, true)
	if s == nil || IsExempt(remoteAddr) {
		return &AuthAttempt{Result: Result{Allowed: true}}
	}
	keys = append([]string{IPKey(remoteAddr)}, keys...)
	s.mu.Lock()
	defer s.mu.Unlock()
	t := now()
	buckets := make([]*bucket, 0, len(keys))
	for _, k := range keys {
		b := s.get(k, remoteAddr, t)
		if b.tokens < 1 {
			return &AuthAttempt{Result: s.result(b, false)}
		}
		if !slices.Contains(buckets, b) { // keys may share an overflow bucket
			buckets = append(buckets, b)
		}
	}
	for _, b := range buckets {
		b.tokens--
	}
	return &AuthAttempt{Result: Result{Allowed: true}, buckets: buckets}
}

// Succeeded gives back the tokens taken by StartAuth.
func (a *AuthAttempt) Succeeded() {
	s := getStore(Auth, true)
	if s == nil || len(a.buckets) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range a.buckets {
		b.tokens = min(float64(s.burst), b.tokens+1)
	}
}

// Err returns a *LimitedError when the attempt was rejected.
func (a *AuthAttempt) Err() error {
	if a.Allowed {
		return nil
	}
	return &LimitedError{a.Result}
}

func parseIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	return net.ParseIP(host)
}

// IPKey returns the bucket key of a client address: the IPv4 address, or the /64 of an IPv6 address.
func IPKey(remoteAddr string) string {
	ip := parseIP(remoteAddr)
	if ip == nil {
		return "ip:invalid"
	}
	if ip4 := ip.To4(); ip4 != nil {
		return "ip:" + ip4.String()
	}
	return "ip:" + ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// overflowKey returns the shared bucket key of the IPv4 /24 or IPv6 /48 of a client.
func overflowKey(remoteAddr string) string {
	ip := parseIP(remoteAddr)
	if ip == nil {
		return "overflow:invalid"
	}
	if ip4 := ip.To4(); ip4 != nil {
		return "overflow:" + ip4.Mask(net.CIDRMask(24, 32)).String() + "/24"
	}
	return "overflow:" + ip.Mask(net.CIDRMask(48, 128)).String() + "/48"
}

// UserKey returns the auth bucket key of a sign-in name. It is hashed so that its size doesn't depend on the input.
func UserKey(name string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(name))))
	return "user:" + hex.EncodeToString(sum[:])
}

// TwoFactorKey returns the auth bucket key of the second factor of a user.
func TwoFactorKey(uid int64) string {
	return "2fa:" + strconv.FormatInt(uid, 10)
}

// IsExempt reports whether the client address is in [rate_limit] EXEMPT_CIDRS.
func IsExempt(remoteAddr string) bool {
	ip := parseIP(remoteAddr)
	return ip != nil && setting.RateLimit.ExemptCIDRs.MatchIPAddr(ip)
}

// ResetForTest drops all buckets, so that changed settings apply.
func ResetForTest() {
	storesMu.Lock()
	defer storesMu.Unlock()
	stores = map[string]*store{}
}
