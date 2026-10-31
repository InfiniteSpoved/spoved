// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// Package secretdetection finds credentials (cloud keys, tokens, private keys)
// in text. It is pure: it reads bytes and reports where it found what, and it
// knows nothing about git, repositories or settings. Which rules are enabled
// is the caller's decision.
package secretdetection

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

// Rule describes one kind of secret. Rules are plain data; build a Detector
// from them with NewDetector.
type Rule struct {
	ID   string // stable, kebab-case, e.g. "aws-access-key-id"
	Name string // human readable
	Type string // secret type group for filters, e.g. "aws", "github"

	// Keywords are lowercase literals. A rule runs its regex only when the
	// lowercased content contains one of them (the main CPU saver). No
	// keywords means the rule always runs.
	Keywords []string

	// Regex is RE2 (Go regexp). SecretGroup is the capture group holding the
	// secret (0 = the whole match).
	Regex       string
	SecretGroup int

	// MinEntropy is the minimum Shannon entropy (bits per byte) of the
	// secret; 0 = no check.
	MinEntropy float64

	// Allowlist regexes are matched against the secret; a hit drops the
	// finding (placeholders like EXAMPLE, xxxx, ${VAR}, <token>).
	Allowlist []string

	// Check is an optional extra test on the secret that RE2 can't express.
	Check func(secret []byte) bool

	// DefaultOff marks noisy rules (generic ones) that callers should not
	// enable unless asked to.
	DefaultOff bool
}

type compiledRule struct {
	Rule
	re        *regexp.Regexp
	allowlist []*regexp.Regexp
}

func compileRule(r Rule) (*compiledRule, error) {
	if r.ID == "" {
		return nil, fmt.Errorf("secretdetection: rule without ID")
	}
	for _, k := range r.Keywords {
		if k == "" || k != strings.ToLower(k) {
			return nil, fmt.Errorf("secretdetection: rule %s: keyword %q must be non-empty and lowercase", r.ID, k)
		}
	}
	re, err := regexp.Compile(r.Regex)
	if err != nil {
		return nil, fmt.Errorf("secretdetection: rule %s: %w", r.ID, err)
	}
	if r.SecretGroup < 0 || r.SecretGroup > re.NumSubexp() {
		return nil, fmt.Errorf("secretdetection: rule %s: secret group %d does not exist", r.ID, r.SecretGroup)
	}
	c := &compiledRule{Rule: r, re: re}
	for _, a := range r.Allowlist {
		are, err := regexp.Compile(a)
		if err != nil {
			return nil, fmt.Errorf("secretdetection: rule %s: allowlist: %w", r.ID, err)
		}
		c.allowlist = append(c.allowlist, are)
	}
	return c, nil
}

// entropy is the Shannon entropy of b in bits per byte.
func entropy(b []byte) float64 {
	if len(b) == 0 {
		return 0
	}
	var counts [256]int
	for _, c := range b {
		counts[c]++
	}
	n := float64(len(b))
	var e float64
	for _, c := range counts {
		if c > 0 {
			p := float64(c) / n
			e -= p * math.Log2(p)
		}
	}
	return e
}
