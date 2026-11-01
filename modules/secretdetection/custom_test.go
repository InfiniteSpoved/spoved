// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretdetection

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ValidateStoredRule is ValidateCustomRule without a test string: it refuses the same rules, for the same reasons.
func TestValidateStoredRule(t *testing.T) {
	kw := []string{"acme-key"}
	pattern := `acme-key-[a-z0-9]{20}`
	require.NoError(t, ValidateStoredRule("rule", pattern, kw))
	for _, c := range []struct {
		name, pattern string
		keywords      []string
		want          error
	}{
		{"", pattern, kw, ErrCustomNameInvalid},
		{strings.Repeat("n", MaxCustomNameLen+1), pattern, kw, ErrCustomNameInvalid},
		{"r", strings.Repeat("a", MaxCustomPatternLen+1), kw, ErrPatternTooLong},
		{"r", `acme-key-(`, kw, ErrPatternInvalid},
		{"r", `(acme-key-[a-z0-9]+)+`, kw, ErrPatternTooComplex},
		{"r", `acme-key-[a-z0-9]+`, kw, ErrPatternTooComplex},
		{"r", `a{0}`, kw, ErrPatternMatchesEmpty},
		{"r", pattern, nil, ErrKeywordMissing},
		{"r", pattern, []string{"ab"}, ErrKeywordMissing},
		{"r", pattern, []string{strings.Repeat("k", MaxCustomKeywordLen+1)}, ErrKeywordMissing},
		{"r", pattern, []string{"aaa", "bbb", "ccc", "ddd", "eee", "fff"}, ErrKeywordMissing},
		{"r", `[a-z]{8,12}`, kw, ErrPatternMatchesTooMuch},
	} {
		require.ErrorIs(t, ValidateStoredRule(c.name, c.pattern, c.keywords), c.want, "%q %q", c.name, c.pattern)
	}
}
