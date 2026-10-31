// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretscan

import (
	"strings"
	"testing"

	"forgejo.org/modules/secretdetection"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// the work of the comment check is bounded by a count of candidates and bytes, not by the time it takes
func TestCommentCandidatesBudget(t *testing.T) {
	for _, comment := range []string{
		strings.Repeat("a.=", 333),
		strings.Repeat("ab.=", 250),
		strings.Repeat("aaaaaaaaaaaaaaa.b=", 55),
		strings.Repeat("a.", 500),
		strings.Repeat("a/", 500),
		strings.Repeat("a/", 50) + strings.Repeat("A=", 400) + strings.Repeat("/a", 50),
	} {
		c := &commentCandidates{hashes: map[string]struct{}{}}
		err := c.addComment(comment)
		if err != nil {
			require.ErrorIs(t, err, ErrCommentTooCostly, comment[:8])
		}
		assert.LessOrEqual(t, c.count, maxCandidates+1, comment[:8])
		assert.LessOrEqual(t, c.bytes, maxCandidateBytes+maxCandidateLen, comment[:8])
		assert.LessOrEqual(t, len(c.hashes), c.count, comment[:8])
	}
	// the budget covers both texts of a comment
	c := &commentCandidates{hashes: map[string]struct{}{}}
	require.NoError(t, c.addComment("plain words and a token-like-thing-of-some-length"))
	n := c.count
	assert.Positive(t, n)

	// a plain comment that holds a secret is still refused by the detector, one that holds a fingerprinted one is found
	secret := "ghp" + "_" + "Ab1Cd2Ef3Gh4" + "Ij5Kl6Mn7Op8" + "Qr9St0Uv1Wx2"
	require.ErrorIs(t, (&commentCandidates{hashes: map[string]struct{}{}}).addComment("it was "+secret), ErrCommentSecret)
	bare := "wJalrXUtn" + "FEMI/K7MDENG/" + "bPxRfiCY" + "Zq3vT8kLm1"
	c = &commentCandidates{hashes: map[string]struct{}{}}
	require.NoError(t, c.addComment("old key: **"+bare+"**"))
	assert.Contains(t, c.hashes, secretdetection.Fingerprint([]byte(bare)))

	// the value joined to other text with _ + = . / - # :
	for _, form := range []string{bare + "_old", "tok_" + bare, bare + "+1", bare + "==", "a=" + bare + "=b", "/srv/" + bare + "_bak/x", "x:" + bare + "#y"} {
		c = &commentCandidates{hashes: map[string]struct{}{}}
		require.NoError(t, c.addComment(form), form)
		assert.Contains(t, c.hashes, secretdetection.Fingerprint([]byte(bare)), form)
	}
}
