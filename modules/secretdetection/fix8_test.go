// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretdetection

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const lowerAlnum = "abcdefghijklmnopqrstuvwxyz0123456789"

// comparisonRuleTests are the forms the gitleaks/trufflehog comparison (#149) found missing or wrong.
func comparisonRuleTests() []ruleTest {
	hex40 := fake(40, hexa)
	pat52 := fake(52, lowerAlnum)
	jwt := b64url(`{"alg":"HS256","typ":"JWT"}`) + "." + b64url(`{"sub":"42","name":"Jane Roe"}`) + "." + fake(43, b64u)
	return []ruleTest{
		{
			"slack-token",
			[]string{
				"xoxs-" + "210987654321-123456789-2109876543-" + fake(10, hexa),
				"xoxo-" + "210987654321-123456789-2109876543-" + fake(32, hexa),
			},
			[]string{"xoxs-your-token-here", "xoxo-12-34"},
		},
		{
			"azure-devops-pat-legacy",
			[]string{
				"azure:\n  azure_key: " + pat52 + "\n",
				"<id>{azure CLIENT " + pat52 + "}</id>",
				"AZURE_DEVOPS_EXT_PAT=" + pat52,
				"ADO_PAT: '" + pat52 + "'",
			},
			[]string{
				"key: " + pat52,    // no keyword
				"azure:\n" + pat52, // not on the keyword's line
				"azure_key: " + fake(53, lowerAlnum),
				"azure_key: " + strings.ToUpper(pat52),
				"azure_key: " + strings.Repeat("ab", 26),   // no entropy
				"azure " + strings.Repeat(" ", 50) + pat52, // too far
			},
		},
		{
			"github-legacy-token",
			[]string{`"github_secret": "` + hex40 + `"`, "GITHUB_TOKEN=" + hex40, "github.oauth.client_secret: " + hex40},
			[]string{"GITHUB_SHA=" + hex40, "github.sha: " + hex40, "github_token = " + fake(39, hexa), "token = " + hex40},
		},
		{
			"jwt-base64",
			[]string{
				"token: " + base64.StdEncoding.EncodeToString([]byte(jwt)),
				base64.StdEncoding.EncodeToString([]byte(jwt + "\n")), // echo $TOKEN | base64
			},
			[]string{
				base64.StdEncoding.EncodeToString([]byte(b64url(`{"typ":"JWT"}`) + "." + b64url(`{"sub":"42","name":"Jane Roe"}`) + "." + fake(43, b64u))), // no alg
				"ZXlK" + fake(80, b64), // no JWT inside
				base64.StdEncoding.EncodeToString([]byte(jwt[:len(jwt)-43] + "signature")),
			},
		},
	}
}

// An xoxa-2 token has three IDs and a hex part: all of it is the secret, so two tokens that share the IDs are two
// secrets (the hex part used to be left out: redaction kept it and both had one fingerprint).
func TestSlackTokenIsMatchedWhole(t *testing.T) {
	d := allRulesDetector(t)
	ids := "xoxa-2-" + "210987654-21098765432-2109876543210-"
	for _, tok := range []string{ids + fake(32, hexa), "xoxs-" + "210987654321-123456789-2109876543-" + fake(10, hexa)} {
		content := "slack_token: '" + tok + "'\n"
		fs := scanOf(d, []byte(content))
		require.Len(t, fs, 1, tok)
		assert.Equal(t, tok, content[fs[0].Start:fs[0].End])
		assert.Equal(t, Fingerprint([]byte(tok)), fs[0].Fingerprint)
	}
	a, b := scanOf(d, []byte(ids+fake(32, hexa))), scanOf(d, []byte(ids+fake(32, "fedcba9876543210")))
	require.Len(t, a, 1)
	require.Len(t, b, 1)
	assert.NotEqual(t, a[0].Fingerprint, b[0].Fingerprint)
}

// A legacy GitHub token after a github name is a GitHub token, also when the name says token.
func TestGitHubLegacyTokenWinsOverForgejo(t *testing.T) {
	d := allRulesDetector(t)
	fs := scanOf(d, []byte("GITHUB_TOKEN="+fake(40, hexa)+"\n"))
	require.Len(t, fs, 1)
	assert.Equal(t, "github-legacy-token", fs[0].RuleID)
	assert.Equal(t, 1, countRule(d, "FORGEJO_TOKEN="+fake(40, hexa), "forgejo-access-token"))
}

// A JWT in base64 has the fingerprint of the JWT.
func TestBase64JWTHasTheJWTFingerprint(t *testing.T) {
	d := allRulesDetector(t)
	jwt := b64url(`{"alg":"RS256"}`) + "." + b64url(`{"sub":"42","name":"Jane Roe"}`) + "." + fake(86, b64u)
	plain, wrapped := scanOf(d, []byte(jwt)), scanOf(d, []byte("data:\n  token: "+base64.StdEncoding.EncodeToString([]byte(jwt))+"\n"))
	require.Len(t, plain, 1)
	require.Len(t, wrapped, 1)
	assert.Equal(t, "jwt-base64", wrapped[0].RuleID)
	assert.Equal(t, plain[0].Fingerprint, wrapped[0].Fingerprint)
}

// Documentation samples: a JWT signed with a word, and a token made of an ascending sequence.
func TestSamplePlaceholdersAreNoSecrets(t *testing.T) {
	d := allRulesDetector(t)
	doc := b64url(`{"alg":"HS256","typ":"JWT"}`) + "." + b64url(`{"sub":"1234567890","name":"Jane Roe","iat":1516239022}`) + ".signature"
	assert.Empty(t, scanOf(d, []byte("Example header.payload: `"+doc+"`\n")))
	assert.Empty(t, scanOf(d, []byte("GITHUB_TOKEN=ghp_"+"0123456789"+"abcdefghijklmnopqrstuvwxyz\n")))
	assert.Empty(t, scanOf(d, []byte("token: ghp_"+fake(20, alnum)+"klmnopqrstuvwxyz\n")), "a run inside")
	assert.Len(t, scanOf(d, []byte("token: ghp_"+fake(36, alnum)+"\n")), 1)
	assert.Len(t, scanOf(d, []byte("xoxb-1234567890-123456789012-"+fake(24, alnum)+"\n")), 1, "nine ascending digits are an ID")
}
