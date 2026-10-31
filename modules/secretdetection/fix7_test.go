// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretdetection

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lateSecrets are secrets of rules that run late, built from parts.
func lateSecrets() map[string]string {
	return map[string]string{
		"private-key":               "-----BEGIN RSA PRIVATE" + " KEY-----\n" + fake(64, b64) + "\n" + fake(64, b64) + "\n-----END RSA PRIVATE" + " KEY-----",
		"slack-token":               "xoxb-" + "1234567890" + "-" + fake(24, alnum),
		"stripe-live-key":           "sk_" + "live_" + fake(24, alnum),
		"generic-secret-assignment": `password = "` + fake(28, alnum) + `"`,
		"cacme":                     "acme_" + fake(32, alnum),
		"github-token":              "ghp_" + fake(36, alnum),
	}
}

// Decoys for the first rules of the order must not starve the later rules
// (red team round 6: two AWS rules took the whole budget).
func TestDecoysForTwoRulesDoNotStarveTheRest(t *testing.T) {
	d, err := NewDetector(append(BuiltinRules(), CustomRule("cacme", "n", `acme_([A-Za-z0-9]{32})`, []string{"acme"})))
	require.NoError(t, err)
	units := []string{
		"aws_secret=" + strings.Repeat("A", 40) + "\n",
		"AKIA" + strings.Repeat("A", 16) + " " + strings.Repeat("A", 40) + "\n",
	}
	for _, hdr := range []bool{false, true} {
		var sb strings.Builder
		if hdr {
			sb.WriteString(keywordLine + "acme\n")
		}
		for _, u := range units {
			sb.Write(repeatTo(u, 1<<20/slowdown))
			sb.WriteString("\n")
		}
		for _, s := range lateSecrets() {
			sb.WriteString(s + "\n")
		}
		fs, _ := d.Scan([]byte(sb.String()))
		for id := range lateSecrets() {
			assert.Positive(t, countRuleIn(fs, id), "%s (keyword line %v)", id, hdr)
		}
	}
}

// The same with a line of every keyword, 3 MiB of aws_secret decoys, and the
// secrets before and after them.
func TestKeywordLineAndAWSDecoysKeepLaterRules(t *testing.T) {
	d := allRulesDetector(t)
	secrets := "ghp_" + fake(36, alnum) + "\n" + "xoxb-1234567890-" + fake(24, alnum) + "\n" + "token = " + fake(40, hexa) + "\n"
	decoys := string(repeatTo("aws_secret = "+strings.Repeat("A", 40)+"\n", 3<<20/slowdown))
	for name, content := range map[string]string{
		"after":  keywordLine + decoys + "\n" + secrets,
		"before": keywordLine + secrets + "\n" + decoys,
	} {
		fs, _ := d.Scan([]byte(content))
		for _, id := range []string{"github-token", "slack-token", "forgejo-access-token"} {
			assert.Equal(t, 1, countRuleIn(fs, id), "%s: %s", name, id)
		}
	}
}

// Prose after a line with the keywords of the rules that have no literal
// prefix is cheap, and a token after it is found with complete=true.
func TestKeywordLineThenProseFindsTheToken(t *testing.T) {
	d := allRulesDetector(t)
	prose := repeatTo("The quick brown fox jumps over the lazy dog, and then it rests. ", 9<<20/slowdown)
	content := append([]byte(keywordLine), prose...)
	content = append(content, []byte("\nghp_"+fake(36, alnum)+"\n")...)
	start := time.Now()
	fs, complete := d.Scan(content)
	assert.Less(t, time.Since(start), time.Second*slowdown)
	assert.True(t, complete)
	assert.Equal(t, 1, countRuleIn(fs, "github-token"))
}

// Worst custom rules first, decoys for them, then a match of a later custom
// rule and a token.
func TestCustomRulesDoNotStarveALaterCustomRule(t *testing.T) {
	rules := append(BuiltinRules(), worstCustomRules()...)
	rules = append(rules, CustomRule("cacme", "n", `acme_([A-Za-z0-9]{32})`, []string{"acme"}))
	d, err := NewDetector(rules)
	require.NoError(t, err)
	content := keywordLine + "acme kw0 aba\n" + strings.Repeat("a", 4<<20/slowdown) + "\nacme_" + fake(32, alnum) + "\nghp_" + fake(36, alnum) + "\n"
	fs, _ := d.Scan([]byte(content))
	assert.Equal(t, 1, countRuleIn(fs, "cacme"))
	assert.Equal(t, 1, countRuleIn(fs, "github-token"))
}
