// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretdetection

import (
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
	"sync"
)

const (
	// MaxCustomPatternLen is the longest custom pattern, in bytes.
	MaxCustomPatternLen = 1024
	// MaxCustomNameLen is the longest custom rule name, in bytes.
	MaxCustomNameLen = 100
	// MaxCustomTestStringLen is the longest test string, in bytes.
	MaxCustomTestStringLen = 4 << 10
	// MaxCustomRepeatSum is the most the maxima of all counted repeats in a
	// pattern (x{0,100}) may add up to; an unbounded repeat counts its minimum.
	MaxCustomRepeatSum = 256
	// MaxCustomProgSize is the most instructions a compiled pattern may have.
	MaxCustomProgSize = 300
	// MinCustomKeywordLen and MaxCustomKeywordLen bound a keyword, in bytes;
	// MaxCustomKeywords is how many a rule may have.
	MinCustomKeywordLen = 3
	MaxCustomKeywordLen = 64
	MaxCustomKeywords   = 5
	// MinCustomSecretLen is the shortest secret a custom rule reports.
	MinCustomSecretLen = 8
	// MaxCorpusMatches is how often a custom pattern may match the ordinary
	// text corpus; more means it matches ordinary files (`.*`, `\w+`, a hash).
	MaxCorpusMatches = 10
)

// Reasons ValidateCustomRule refuses a rule; test with errors.Is.
var (
	ErrCustomNameInvalid     = errors.New("rule name is empty or too long")
	ErrPatternTooLong        = errors.New("pattern is too long")
	ErrPatternInvalid        = errors.New("pattern is not a valid RE2 expression")
	ErrPatternTooComplex     = errors.New("pattern has nested or too large repeats, or compiles to too many instructions")
	ErrPatternMatchesEmpty   = errors.New("pattern matches the empty string")
	ErrTestStringTooLong     = errors.New("test string is too long")
	ErrKeywordMissing        = errors.New("a rule needs 1 to 5 keywords of 3 to 64 bytes, and every one must appear in the match of the test string")
	ErrPatternNoMatchInTest  = errors.New("pattern finds nothing in the test string")
	ErrSecretTooShort        = errors.New("the secret found in the test string is too short")
	ErrPatternMatchesTooMuch = errors.New("pattern matches ordinary text too often")
)

// ValidateCustomRule checks a user-written rule and returns why it is refused.
// The cost of a pattern is bounded by its shape, not measured: RE2 is linear,
// but the cost per byte grows with the size of the program and with repeats
// that can nest or add up. So the pattern is at most 1024 bytes and compiles to
// at most 300 instructions, no repeat (x{n,m}, x*, x+) stands inside another,
// and the maxima of the counted repeats add up to at most 256. It can't match
// the empty string. keywords (1 to 5, 3 to 64 bytes) are literals that all
// appear in the text the pattern matches in testString (at most 4 KiB): the
// rule runs only in windows around them (see Rule.Window), which keeps a
// custom rule from costing CPU on whole files. The secret found in testString
// (capture group 1 if the pattern has one, else the whole match) must be at
// least 8 bytes. The pattern may match the fixed 64 KiB corpus of ordinary
// text at most MaxCorpusMatches times. Build the rule with CustomRule.
func ValidateCustomRule(name, pattern, testString string, keywords []string) error {
	if name == "" || len(name) > MaxCustomNameLen {
		return ErrCustomNameInvalid
	}
	if len(testString) > MaxCustomTestStringLen {
		return ErrTestStringTooLong
	}
	if len(pattern) > MaxCustomPatternLen {
		return ErrPatternTooLong
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPatternInvalid, err)
	}
	if err := checkComplexity(pattern); err != nil {
		return err
	}
	if re.MatchString("") {
		return ErrPatternMatchesEmpty
	}
	rule := CustomRule("custom", name, pattern, keywords)
	m := re.FindStringSubmatchIndex(testString)
	if m == nil {
		return ErrPatternNoMatchInTest
	}
	if !keywordsInMatch(rule.Keywords, strings.ToLower(testString[m[0]:m[1]])) {
		return ErrKeywordMissing
	}
	if m[2*rule.SecretGroup+1]-m[2*rule.SecretGroup] < MinCustomSecretLen {
		return ErrSecretTooShort
	}
	d, err := NewDetector([]Rule{rule})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPatternInvalid, err)
	}
	if fs, _ := d.Scan([]byte(testString)); len(fs) == 0 {
		return ErrPatternNoMatchInTest
	}
	if len(re.FindAllIndex(corpus(), MaxCorpusMatches+1)) > MaxCorpusMatches {
		return ErrPatternMatchesTooMuch
	}
	return nil
}

// CustomRule is the Rule of a custom pattern: the secret is capture group 1 if
// the pattern has one, else the whole match, and at least 8 bytes. It runs in
// windows around the (lowercased) keywords: the secret length cap on each side
// plus room for the context.
func CustomRule(id, name, pattern string, keywords []string) Rule {
	r := Rule{
		ID: id, Name: name, Type: "custom", Regex: pattern,
		MinSecretLen: MinCustomSecretLen, Window: DefaultMaxSecretLen + 256, // a longer secret is cut by the window, so it is over the cap
		Allowlist: slices.Clone(placeholders),
	}
	if re, err := regexp.Compile(pattern); err == nil {
		r.SecretGroup = min(re.NumSubexp(), 1)
	}
	for _, k := range keywords {
		r.Keywords = append(r.Keywords, strings.ToLower(k))
	}
	return r
}

// keywordsInMatch: there are 1 to 5 keywords, each of a fitting length, and
// every one is in the match.
func keywordsInMatch(keywords []string, match string) bool {
	if len(keywords) == 0 || len(keywords) > MaxCustomKeywords {
		return false
	}
	for _, k := range keywords {
		if len(k) < MinCustomKeywordLen || len(k) > MaxCustomKeywordLen || !strings.Contains(match, k) {
			return false
		}
	}
	return true
}

// checkComplexity refuses the shapes that make RE2 slow: a big program, repeats
// inside repeats and counted repeats that add up.
func checkComplexity(pattern string) error {
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPatternInvalid, err)
	}
	sum := 0
	var tooBig func(r *syntax.Regexp, inRepeat bool) bool
	tooBig = func(r *syntax.Regexp, inRepeat bool) bool {
		repeat := r.Op == syntax.OpStar || r.Op == syntax.OpPlus || r.Op == syntax.OpRepeat
		if repeat && inRepeat {
			return true
		}
		if r.Op == syntax.OpRepeat {
			sum += max(r.Max, r.Min)
		}
		for _, sub := range r.Sub {
			if tooBig(sub, inRepeat || repeat) {
				return true
			}
		}
		return false
	}
	if tooBig(parsed, false) || sum > MaxCustomRepeatSum {
		return ErrPatternTooComplex
	}
	prog, err := syntax.Compile(parsed.Simplify())
	if err != nil || len(prog.Inst) > MaxCustomProgSize {
		return ErrPatternTooComplex
	}
	return nil
}

// corpus is a fixed 64 KiB of ordinary files: prose, code with tabs and
// braces, JSON, URLs, git hashes, UUIDs, lock file integrity lines, base64,
// and non-ASCII text. A simple generator makes it, so it is the same
// everywhere and holds no literal that looks like a secret.
var corpus = sync.OnceValue(func() []byte {
	words := strings.Fields(`the of and to in is that for it as was with be by on not this are or from at which but have an had they you were their one all we can her has there been if more when will would who so no
config server value name list item index result error file path user group time data build test version release branch commit merge request issue comment label state open closed
configuration repository environment implementation authentication organization notification description
2024 1.2.3 404 8080 v2 x86_64 id-42 foo_bar camelCase snake_case kebab-case`)
	var x uint32 = 1
	next := func(n int) int {
		x = x*1664525 + 1013904223
		return int(x>>8) % n
	}
	random := func(alphabet string, n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = alphabet[next(len(alphabet))]
		}
		return string(b)
	}
	const hexDigits, b64 = "0123456789abcdef", "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var b strings.Builder
	for b.Len() < 64<<10 {
		switch next(14) {
		case 0:
			b.WriteString("commit " + random(hexDigits, 40) + "\n")
		case 1:
			b.WriteString("sha256:" + random(hexDigits, 64) + "\n")
		case 2:
			u := random(hexDigits, 32)
			b.WriteString(u[:8] + "-" + u[8:12] + "-" + u[12:16] + "-" + u[16:20] + "-" + u[20:] + "\n")
		case 3:
			b.WriteString(random(b64, 76) + "\n")
		case 4:
			b.WriteString("integrity sha512-" + random(b64, 86) + "==\n")
		case 5:
			b.WriteString("github.com/org/" + words[next(len(words))] + " v1.2.3 h1:" + random(b64, 43) + "=\n")
		case 6:
			b.WriteString("See https://git.corp.test/" + words[next(len(words))] + "/" + words[next(len(words))] + "/pulls/" + random("0123456789", 3) + "?tab=files&x=1 for details.\n")
		case 7:
			b.WriteString("func " + words[next(len(words))] + "() error {\n\tif err != nil {\n\t\treturn fmt.Errorf(\"" + words[next(len(words))] + ": %w\", err)\n\t}\n}\n")
		case 8:
			b.WriteString(`{"id": ` + random("0123456789", 3) + `, "name": "` + words[next(len(words))] + `", "items": [1, 2, 3], "ok": true}` + "\n")
		case 9:
			b.WriteString("Grüße aus Köln, naïve café — 日本語のテキスト, ¿qué tal?\n")
		default:
			for range 6 + next(10) {
				b.WriteString(words[next(len(words))])
				b.WriteByte(" .,;="[next(5)])
				b.WriteByte(' ')
			}
			b.WriteByte('\n')
		}
	}
	return []byte(b.String())
})
