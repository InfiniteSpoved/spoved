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

	// Normalize is optional: it turns the captured secret into its canonical
	// form (a key with its line breaks and indentation removed), and the
	// entropy, allowlist, Check and the fingerprint all work on that form, so
	// one secret has one fingerprint however it is wrapped. Returning nothing
	// refuses the match.
	Normalize func(secret []byte) []byte

	// Check is an optional extra test on the (normalized) secret that RE2 can't
	// express.
	Check func(secret []byte) bool

	// TokenChars, if set, are the bytes a token is made of: a match is
	// dropped when the byte before or after the secret is one of them. That is
	// deliberate: a token right after a token character is part of a longer
	// word (a hash, an identifier), not a key of its own, and RE2 can't say so
	// with \b without consuming the neighbour. TrailingOK lists bytes that
	// may follow anyway. An escape that an escape (\n, \r,
	// \t, \xXX, \uXXXX) or a URL escape (%XX) may stand right before it. RE2 has no lookaround
	// and a consumed boundary would hide a neighbouring token, so this is
	// checked here, not in the regex.
	TokenChars string

	// MaxSecretLen drops a longer capture; 0 means DefaultMaxSecretLen.
	// MinSecretLen drops a shorter one; 0 means no minimum.
	MaxSecretLen, MinSecretLen int

	// TrailingOK are bytes that may follow the secret although they are in
	// TokenChars (a full stop ending a sentence, a hyphen in "key-backup").
	TrailingOK string

	// RejectCap, if above 0, stops the rule for this Scan after that many
	// matches were thrown away (and Scan reports it). Custom rules have one,
	// built-in rules are known to be cheap and have none.
	RejectCap int

	// KeywordInMatch drops a match that holds none of the rule's keywords
	// (compared lowercase), so a custom pattern's keyword promise holds when
	// scanning, not only for its test string.
	KeywordInMatch bool

	// Window, if above 0, runs the regex only on the Window bytes before and
	// after each keyword hit (windows that touch are merged). WindowBudget, if
	// above 0, caps the bytes of windows per Scan (the last window is cut, not
	// dropped); what lies beyond is not scanned and Scan reports it. Without a
	// budget the whole content may be covered. The regex can't match anything that doesn't lie within
	// Window bytes of a keyword. Needs keywords. Anchors (^, \b) at the edge
	// of a window see the edge as the start of the text.
	Window, WindowBudget int

	// DefaultOff marks noisy rules (generic ones) that callers should not
	// enable unless asked to.
	DefaultOff bool
}

type compiledRule struct {
	Rule
	re        *regexp.Regexp
	allowlist []*regexp.Regexp
	tokenSet  *[256]bool
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
	if r.TokenChars != "" {
		c.tokenSet = new([256]bool)
		for i := range len(r.TokenChars) {
			c.tokenSet[r.TokenChars[i]] = true
		}
	}
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

// boundaryOK is the TokenChars check for the secret at c[start:end].
func (r *compiledRule) boundaryOK(c []byte, start, end int) bool {
	if r.tokenSet == nil {
		return true
	}
	if end < len(c) && r.tokenSet[c[end]] && strings.IndexByte(r.TrailingOK, c[end]) < 0 {
		return false
	}
	if start == 0 || !r.tokenSet[c[start-1]] {
		return true
	}
	switch {
	case start >= 2 && c[start-2] == '\\' && strings.IndexByte("nrt", c[start-1]) >= 0:
		return true // JSON or C escape
	case start >= 3 && c[start-3] == '%' && isHex(c[start-2]) && isHex(c[start-1]):
		return true // URL encoding
	case start >= 4 && c[start-4] == '\\' && c[start-3] == 'x' && isHex(c[start-2]) && isHex(c[start-1]):
		return true // \xXX
	}
	return start >= 6 && c[start-6] == '\\' && c[start-5] == 'u' && isHex(c[start-4]) && isHex(c[start-3]) && isHex(c[start-2]) && isHex(c[start-1])
}

func isHex(b byte) bool {
	return '0' <= b && b <= '9' || 'a' <= b && b <= 'f' || 'A' <= b && b <= 'F'
}
