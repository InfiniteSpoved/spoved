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
	"time"
)

const (
	// MaxCustomPatternLen is the longest custom pattern, in bytes.
	MaxCustomPatternLen = 1024
	// MaxCustomNameLen is the longest custom rule name, in bytes.
	MaxCustomNameLen = 100
	// MaxCustomTestStringLen is the longest test string, in bytes.
	MaxCustomTestStringLen = 4 << 10
	// MaxCustomRepeat is the largest count in a pattern like x{0,100}.
	MaxCustomRepeat = 100
	// MaxCustomProgSize is the most instructions a compiled pattern may have
	// (the largest built-in rules have 1000 to 1600).
	MaxCustomProgSize = 2000
	// MinCustomKeywordLen is the shortest keyword, in bytes.
	MinCustomKeywordLen = 3
	// MinCustomSecretLen is the shortest secret the test string may yield.
	MinCustomSecretLen = 8
	// MaxCorpusMatches is how often a custom pattern may match the ordinary
	// text corpus; more means it matches ordinary files (`.*`, `\w+`, a hash).
	MaxCorpusMatches = 10
	// maxCorpusTime is how long matching the corpus may take (about 150 times
	// what the built-in rules need): a slower pattern would stall every scan.
	maxCorpusTime = 100 * time.Millisecond
)

// Reasons ValidateCustomRule refuses a rule; test with errors.Is.
var (
	ErrCustomNameInvalid     = errors.New("rule name is empty or too long")
	ErrPatternTooLong        = errors.New("pattern is too long")
	ErrPatternInvalid        = errors.New("pattern is not a valid RE2 expression")
	ErrPatternTooComplex     = errors.New("pattern has too large repeat counts or compiles to too many instructions")
	ErrPatternMatchesEmpty   = errors.New("pattern matches the empty string")
	ErrTestStringTooLong     = errors.New("test string is too long")
	ErrKeywordMissing        = errors.New("keywords must be at least 3 bytes, and one must appear in the match of the test string")
	ErrPatternNoMatchInTest  = errors.New("pattern finds nothing in the test string")
	ErrSecretTooShort        = errors.New("the secret found in the test string is too short")
	ErrPatternMatchesTooMuch = errors.New("pattern matches ordinary text too often")
	ErrPatternTooSlow        = errors.New("pattern is too slow")
)

// ValidateCustomRule checks a user-written rule and returns why it is refused.
// The pattern is RE2, at most 1024 bytes, with repeat counts up to 100 and at
// most 2000 compiled instructions. It can't match the empty string. keywords
// are literals of at least 3 bytes, at least one in the text the pattern
// matches in testString (at most 4 KiB), and the rule only runs on content that
// has one of them: that keeps a custom rule from costing CPU on every file.
// The secret found in testString (capture group 1 if the pattern has one, else
// the whole match) must be at least 8 bytes. The pattern may match the fixed
// 64 KiB corpus of ordinary text at most MaxCorpusMatches times and quickly.
// Build the rule with CustomRule.
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
	if !keywordInMatch(rule.Keywords, strings.ToLower(testString[m[0]:m[1]])) {
		return ErrKeywordMissing
	}
	if m[2*rule.SecretGroup+1]-m[2*rule.SecretGroup] < MinCustomSecretLen {
		return ErrSecretTooShort
	}
	d, err := NewDetector([]Rule{rule})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPatternInvalid, err)
	}
	if len(d.Scan([]byte(testString))) == 0 {
		return ErrPatternNoMatchInTest
	}
	// The first 4 KiB tell a slow pattern at once, without a whole second of CPU.
	start := time.Now()
	re.FindAllIndex(corpus()[:4<<10], -1)
	if time.Since(start) > maxCorpusTime/10 {
		return ErrPatternTooSlow
	}
	start = time.Now()
	matches := len(re.FindAllIndex(corpus(), MaxCorpusMatches+1))
	if matches > MaxCorpusMatches {
		return ErrPatternMatchesTooMuch
	}
	if time.Since(start) > maxCorpusTime {
		return ErrPatternTooSlow
	}
	return nil
}

// CustomRule is the Rule of a custom pattern: the secret is capture group 1 if
// the pattern has one, else the whole match. The keywords are lowercased.
func CustomRule(id, name, pattern string, keywords []string) Rule {
	r := Rule{ID: id, Name: name, Type: "custom", Regex: pattern}
	if re, err := regexp.Compile(pattern); err == nil {
		r.SecretGroup = min(re.NumSubexp(), 1)
	}
	for _, k := range keywords {
		r.Keywords = append(r.Keywords, strings.ToLower(k))
	}
	return r
}

func keywordInMatch(keywords []string, match string) bool {
	if len(keywords) == 0 {
		return false
	}
	found := false
	for _, k := range keywords {
		if len(k) < MinCustomKeywordLen {
			return false
		}
		found = found || strings.Contains(match, k)
	}
	return found
}

// checkComplexity refuses big repeat counts and big programs: the cost of RE2
// per byte grows with both, and a custom rule runs on content of every push.
func checkComplexity(pattern string) error {
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPatternInvalid, err)
	}
	var tooBig func(*syntax.Regexp) bool
	tooBig = func(r *syntax.Regexp) bool {
		if r.Op == syntax.OpRepeat && (r.Min > MaxCustomRepeat || r.Max > MaxCustomRepeat) {
			return true
		}
		return slices.ContainsFunc(r.Sub, tooBig)
	}
	if tooBig(parsed) {
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
