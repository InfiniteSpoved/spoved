// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretdetection

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

const (
	// MaxCustomPatternLen is the longest custom pattern, in bytes.
	MaxCustomPatternLen = 1024
	// MaxCustomNameLen is the longest custom rule name, in bytes.
	MaxCustomNameLen = 100
	// MaxCorpusMatches is how often a custom pattern may match the ordinary
	// text corpus; more means it matches everything (`.*`, `\w+`).
	MaxCorpusMatches = 10
)

// Reasons ValidateCustomRule refuses a rule; test with errors.Is.
var (
	ErrCustomNameInvalid     = errors.New("rule name is empty or too long")
	ErrPatternTooLong        = errors.New("pattern is too long")
	ErrPatternInvalid        = errors.New("pattern is not a valid RE2 expression")
	ErrPatternMatchesEmpty   = errors.New("pattern matches the empty string")
	ErrPatternNoMatchInTest  = errors.New("pattern finds nothing in the test string")
	ErrPatternMatchesTooMuch = errors.New("pattern matches ordinary text too often")
)

// ValidateCustomRule checks a user-written rule: the pattern is RE2, at most
// 1024 bytes, can't match the empty string, finds a secret in testString and
// doesn't match ordinary text more than MaxCorpusMatches times. If the pattern
// has a capture group, group 1 is the secret, else the whole match.
func ValidateCustomRule(name, pattern, testString string) error {
	if name == "" || len(name) > MaxCustomNameLen {
		return ErrCustomNameInvalid
	}
	if len(pattern) > MaxCustomPatternLen {
		return ErrPatternTooLong
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPatternInvalid, err)
	}
	if re.MatchString("") {
		return ErrPatternMatchesEmpty
	}
	d, err := NewDetector([]Rule{customRule(name, pattern, re.NumSubexp())})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPatternInvalid, err)
	}
	if len(d.Scan([]byte(testString))) == 0 {
		return ErrPatternNoMatchInTest
	}
	if len(re.FindAllIndex(corpus(), MaxCorpusMatches+1)) > MaxCorpusMatches {
		return ErrPatternMatchesTooMuch
	}
	return nil
}

// customRule is the Rule of a validated custom pattern (numGroups is
// the pattern's capture group count).
func customRule(name, pattern string, numGroups int) Rule {
	return Rule{ID: "custom", Name: name, Type: "custom", Regex: pattern, SecretGroup: min(numGroups, 1)}
}

// corpus is a fixed 64 KiB of ordinary text: words, numbers, punctuation and
// lines, made by a simple generator so it is the same everywhere.
var corpus = sync.OnceValue(func() []byte {
	words := strings.Fields(`the of and to in is that for it as was with be by on not he this are or his from at which but have an had they you were their one all we can her has there been if more when will would who so no
config server value name list item index result error file path user group time data build test version release branch commit merge request issue comment label state open closed
2024 1.2.3 404 8080 v2 x86_64 id-42 foo_bar camelCase snake_case kebab-case`)
	var b strings.Builder
	x := uint32(1)
	for b.Len() < 64<<10 {
		x = x*1664525 + 1013904223
		b.WriteString(words[int(x>>16)%len(words)])
		switch x >> 8 % 17 {
		case 0:
			b.WriteString(".\n")
		case 1:
			b.WriteString(", ")
		case 2:
			b.WriteString(" = \"")
		default:
			b.WriteByte(' ')
		}
	}
	return []byte(b.String())
})
