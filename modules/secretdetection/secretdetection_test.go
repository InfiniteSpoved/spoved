// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretdetection

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"os"
	"regexp"
	"strings"
	"testing"

	"forgejo.org/modules/keying"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	keying.Init([]byte("secretdetection test key"))
	os.Exit(m.Run())
}

const (
	alnum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b64   = alnum + "+/"
	b64u  = alnum + "-_"
	hexa  = "0123456789abcdef"
)

// fake returns n characters of alphabet that look random (all different while
// n <= len(alphabet): the stride is coprime to the alphabet sizes used), so
// test tokens have the format and the entropy of real ones but are no secret,
// and no full token is a literal in the source.
func fake(n int, alphabet string) string {
	var b strings.Builder
	for i := range n {
		b.WriteByte(alphabet[i*7%len(alphabet)])
	}
	return b.String()
}

// pemBlock builds a PEM block of a kind such as "RSA PRIVATE KEY".
func pemBlock(kind string, lines int) string {
	body := make([]string, lines)
	for i := range body {
		body[i] = fake(64, b64)[i%7:] + fake(i%7, b64)
	}
	return "-----BEGIN " + kind + "-----\n" + strings.Join(body, "\n") + "\n-----END " + kind + "-----\n"
}

func b64url(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func allRulesDetector(t testing.TB) *Detector {
	t.Helper()
	d, err := NewDetector(BuiltinRules())
	require.NoError(t, err)
	return d
}

func countRule(d *Detector, content, id string) int {
	n := 0
	for _, f := range d.Scan([]byte(content)) {
		if f.RuleID == id {
			n++
		}
	}
	return n
}

func TestBuiltinRules(t *testing.T) {
	kebab := regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	seen := map[string]bool{}
	for _, r := range BuiltinRules() {
		assert.Regexp(t, kebab, r.ID)
		assert.False(t, seen[r.ID], r.ID)
		seen[r.ID] = true
		assert.NotEmpty(t, r.Name, r.ID)
		assert.Regexp(t, kebab, r.Type, r.ID)
		assert.NotEmpty(t, r.Keywords, r.ID)
		assert.Equal(t, r.ID == "generic-secret-assignment", r.DefaultOff, r.ID)
		got, ok := RuleByID(r.ID)
		assert.True(t, ok)
		assert.Equal(t, r.ID, got.ID)
	}
	_, ok := RuleByID("nope")
	assert.False(t, ok)

	// A caller changing the copy doesn't change the rules.
	rules := BuiltinRules()
	rules[0].Keywords[0] = "changed"
	rules[0].Allowlist[0] = "changed"
	assert.NotEqual(t, "changed", BuiltinRules()[0].Keywords[0])
	assert.NotEqual(t, "changed", BuiltinRules()[0].Allowlist[0])
}

func TestRules(t *testing.T) {
	d := allRulesDetector(t)
	hex40 := fake(40, hexa)
	jwt := func(header, payload, sig string) string { return b64url(header) + "." + b64url(payload) + "." + sig }
	payload := `{"sub":"1234567890","name":"Jane Roe"}`

	tests := []struct {
		rule      string
		positives []string
		negatives []string
	}{
		{
			"aws-access-key-id",
			[]string{"id = AKIA" + fake(16, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"), `"ASIA` + fake(16, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567") + `"`},
			[]string{
				"AKIAIOSFODNN7EXAMPLE",                // AWS documentation example
				"AKIA1890189018901890",                // digits 0, 1, 8, 9 are not base32
				"AKIA" + fake(15, "ABCDEFGHIJKLMNOP"), // too short
				"xAKIA" + fake(16, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"),
			},
		},
		{
			"aws-secret-access-key",
			[]string{
				"aws_secret_access_key = \"" + fake(40, b64) + "\"",
				"AWS_SECRET_ACCESS_KEY=" + fake(40, b64) + "\n",
				`{"aws_secret_access_key": "` + fake(40, b64) + `"}`,
			},
			[]string{
				"aws_secret_access_key = \"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY\"", // AWS documentation example
				"aws_secret_access_key = \"" + fake(39, b64) + "\"",
				"aws_secret_access_key = " + fake(41, b64),
				"AWS_SECRET_ACCESS_KEY=${AWS_SECRET}",
				"password = " + fake(40, b64), // no aws in front
				"aws_secret_access_key = " + hex40,
			},
		},
		{
			"aws-secret-access-key-near-id",
			[]string{
				"AKIA" + fake(16, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567") + "\nsecret: " + fake(40, b64) + "\n",
				"[default]\nid=AKIA" + fake(16, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567") + " " + fake(40, b64),
			},
			[]string{
				"AKIA" + fake(16, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567") + "\nsha1: " + hex40, // too little entropy
				"AKIA" + fake(16, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567") + strings.Repeat("\n", 130) + fake(40, b64),
				fake(40, b64), // no key ID
			},
		},
		{
			"azure-storage-account-key",
			[]string{"DefaultEndpointsProtocol=https;AccountName=acct;AccountKey=" + fake(86, b64) + "==;EndpointSuffix=core.windows.net"},
			[]string{
				"AccountKey=<your-account-key>",
				"AccountKey=" + fake(60, b64) + "==",
				"AccountKey=" + strings.Repeat("x", 86) + "==",
				"AccountKey=" + fake(86, b64) + "=", // not padded like a 64 byte key
			},
		},
		{
			"azure-ad-client-secret",
			[]string{`client_secret: "` + fake(3, alnum) + "8Q~" + fake(34, alnum) + `"`},
			[]string{
				`client_secret: "` + fake(3, alnum) + "8Q~" + fake(10, alnum) + `"`,
				`client_secret: "` + fake(3, alnum) + "8R~" + fake(34, alnum) + `"`,
				`client_secret: "abc8Q~EXAMPLEEXAMPLEEXAMPLEEXAMPLEEXAMPLE"`,
			},
		},
		{
			"gcp-service-account-key",
			[]string{gcpJSON("service_account", `-----BEGIN PRIVATE KEY-----\n`+strings.Repeat(fake(64, b64)+`\n`, 3)+`-----END PRIVATE KEY-----\n`)},
			[]string{
				gcpJSON("authorized_user", `-----BEGIN PRIVATE KEY-----\n`+strings.Repeat(fake(64, b64)+`\n`, 3)+`-----END PRIVATE KEY-----\n`),
				gcpJSON("service_account", "<paste the private key here>"),
				gcpJSON("service_account", `-----BEGIN PRIVATE KEY-----\nshort\n-----END PRIVATE KEY-----\n`),
			},
		},
		{
			"gcp-api-key",
			[]string{`key = "AIza` + fake(35, b64u) + `"`},
			[]string{"AIza" + fake(20, b64u), "AIza" + strings.Repeat("x", 35), "AIza" + strings.Repeat("a", 35)},
		},
		{
			"github-token",
			[]string{"ghp_" + fake(36, alnum), "gho_" + fake(36, alnum), "ghu_" + fake(36, alnum), "ghs_" + fake(36, alnum), "ghr_" + fake(36, alnum)},
			[]string{
				"ghp_" + strings.Repeat("A", 36), // no entropy
				"ghp_" + fake(35, alnum),
				"ghp_" + fake(37, alnum),
				"ghp_<token>",
				"xghp_" + fake(36, alnum),
				"ghx_" + fake(36, alnum),
			},
		},
		{
			"github-fine-grained-pat",
			[]string{"github_pat_" + fake(82, alnum)},
			[]string{"github_pat_" + fake(81, alnum), "github_pat_" + strings.Repeat("B", 82), "github_pat_" + fake(82, alnum) + "x"},
		},
		{
			"gitlab-token",
			[]string{"glpat-" + fake(20, b64u), "gldt-" + fake(20, b64u), "glrt-" + fake(26, b64u), "glptt-" + hex40},
			[]string{"glpat-" + fake(10, b64u), "glpat-" + strings.Repeat("x", 20), "glpat-<token>", "glpat-" + strings.Repeat("a", 20)},
		},
		{
			"forgejo-access-token",
			[]string{
				`token = "` + hex40 + `"`,
				"curl -H 'Authorization: token " + hex40 + "' https://git.corp.test/api/v1/user",
				"machine git.corp.test login bob password " + hex40,
				"machine git.corp.test\n  login bob\n  password " + hex40,
				"git clone https://bob:" + hex40 + "@git.corp.test/org/repo.git",
				"FORGEJO_ACCESS_TOKEN=" + hex40,
			},
			[]string{
				"commit " + hex40, // a hash is not a token
				"sha = " + hex40,
				`token = "` + fake(39, hexa) + `"`,
				`token = "` + strings.Repeat("0", 40) + `"`,
				`token = "${FORGEJO_TOKEN}"`,
				`token = "<your 40 character token>"`,
				"https://git.corp.test/org/repo.git",
			},
		},
		{
			"private-key",
			[]string{
				pemBlock("RSA PRIVATE KEY", 5),
				pemBlock("OPENSSH PRIVATE KEY", 5),
				pemBlock("EC PRIVATE KEY", 3),
				pemBlock("DSA PRIVATE KEY", 5),
				pemBlock("PGP PRIVATE KEY BLOCK", 4),
				pemBlock("PRIVATE KEY", 4),
				pemBlock("ENCRYPTED PRIVATE KEY", 4),
				strings.ReplaceAll(pemBlock("RSA PRIVATE KEY", 4), "\n", "\r\n"),
			},
			[]string{
				pemBlock("PUBLIC KEY", 4),
				pemBlock("CERTIFICATE", 4),
				"-----BEGIN PRIVATE KEY-----\nMIIE\n-----END PRIVATE KEY-----\n", // too short
				"-----BEGIN PRIVATE KEY-----\n<paste your key here, 64 or more characters, between the lines>\n-----END PRIVATE KEY-----\n",
				"-----BEGIN RSA PRIVATE KEY-----\n" + strings.Repeat("A", 100) + "\n-----END RSA PRIVATE KEY-----\n", // no entropy
				"-----BEGIN RSA PRIVATE KEY-----\n" + fake(64, b64) + "\n",                                           // no END line
			},
		},
		{
			"slack-token",
			[]string{
				"xoxb-210987654321-123456789012-" + fake(24, alnum),
				"xoxp-210987654321-123456789012-112233445566-" + fake(32, hexa),
				"xoxa-2-210987654321-123456789012-" + fake(32, hexa),
				"xoxr-210987654321-123456789012-" + fake(24, alnum),
			},
			[]string{"xoxb-12-34", "xoxb-your-token-here", "xoxb-xxxxxxxxxx-xxxxxxxxxx-xxxxxxxxxxxxxxxxxxxxxxxx", "xoxc-210987654321-123456789012-" + fake(24, alnum)},
		},
		{
			"slack-webhook-url",
			[]string{"https://hooks.slack.com/services/T0A1B2C3D/B4E5F6G7H/" + fake(24, alnum)},
			[]string{
				"https://hooks.slack.com/services/T00000000/B00000000/" + strings.Repeat("X", 24), // documentation example
				"https://hooks.slack.com/services/T0A1B2C3D/B4E5F6G7H/short",
				"https://hooks.slack.com/",
			},
		},
		{
			"stripe-live-key",
			[]string{"sk_live_" + fake(24, alnum), "rk_live_" + fake(24, alnum)},
			[]string{
				"sk_live_" + strings.Repeat("x", 24),
				"sk_live_short",
				"sk_live_<your key>",
				"sk_test_" + fake(24, alnum), // its own rule
				"pk_live_" + fake(24, alnum), // publishable
			},
		},
		{
			"stripe-test-key",
			[]string{"sk_test_" + fake(24, alnum), "rk_test_" + fake(24, alnum)},
			[]string{"sk_test_" + strings.Repeat("x", 24), "sk_test_short", "sk_live_" + fake(24, alnum)},
		},
		{
			"jwt",
			[]string{
				jwt(`{"alg":"HS256","typ":"JWT"}`, payload, fake(43, b64u)),
				jwt(`{"typ":"JWT","alg":"RS256"}`, payload, fake(43, b64u)),
				jwt(`{"alg":"none"}`, payload, ""), // alg none still counts
				`Authorization: Bearer ` + jwt(`{"alg":"ES256"}`, payload, fake(43, b64u)),
			},
			[]string{
				jwt(`{"typ":"JWT","kid":"1"}`, payload, fake(43, b64u)), // header without alg
				jwt(`not json at all, but long`, payload, fake(43, b64u)),
				"eyJ" + fake(20, b64u) + ".eyJ" + fake(20, b64u) + "." + fake(43, b64u), // header is no JSON
				jwt(`{"alg":"HS256"}`, `{"a":1}`, "sig"),                                // too short to be a token
			},
		},
		{
			"generic-secret-assignment",
			[]string{`api_key = "` + fake(30, alnum) + `"`, `password: "` + fake(24, b64u) + `"`, `"client_secret": "` + fake(32, alnum) + `"`},
			[]string{
				`password = "${DATABASE_PASSWORD_FROM_ENV}"`,
				`secret = "changeme"`,
				`token = "` + strings.Repeat("a1", 15) + `"`, // no entropy
				`password = "<your-password-goes-here>"`,
				`api_key = ` + fake(30, alnum), // not quoted
			},
		},
	}

	covered := map[string]bool{}
	for _, tt := range tests {
		covered[tt.rule] = true
		t.Run(tt.rule, func(t *testing.T) {
			for _, p := range tt.positives {
				assert.Equal(t, 1, countRule(d, p, tt.rule), "should find: %.80q", p)
			}
			for _, n := range tt.negatives {
				assert.Zero(t, countRule(d, n, tt.rule), "should not find: %.80q", n)
			}
		})
	}
	for _, r := range BuiltinRules() {
		assert.True(t, covered[r.ID], "no test for rule %s", r.ID)
	}
}

func gcpJSON(typ, privateKey string) string {
	return fmt.Sprintf("{\n  \"type\": %q,\n  \"project_id\": \"demo-project\",\n  \"private_key_id\": %q,\n  \"private_key\": \"%s\",\n  \"client_email\": \"sa@demo-project.iam.gserviceaccount.com\"\n}\n",
		typ, fake(40, hexa), privateKey)
}

func TestFindingFields(t *testing.T) {
	d := allRulesDetector(t)
	token := "ghp_" + fake(36, alnum)

	t.Run("lines and columns", func(t *testing.T) {
		for _, eol := range []string{"\n", "\r\n"} {
			content := "first" + eol + "second line" + eol + "  token: " + token + eol + "last" + eol
			fs := d.Scan([]byte(content))
			require.Len(t, fs, 1)
			assert.Equal(t, "github-token", fs[0].RuleID)
			assert.Equal(t, 3, fs[0].StartLine)
			assert.Equal(t, 3, fs[0].EndLine)
			assert.Equal(t, 10, fs[0].StartCol)
			assert.Equal(t, Fingerprint([]byte(token)), fs[0].Fingerprint())
		}
	})
	t.Run("first and last line", func(t *testing.T) {
		fs := d.Scan([]byte(token))
		require.Len(t, fs, 1)
		assert.Equal(t, [3]int{1, 1, 1}, [3]int{fs[0].StartLine, fs[0].EndLine, fs[0].StartCol})
	})
	t.Run("multi-line PEM block", func(t *testing.T) {
		for _, eol := range []string{"\n", "\r\n"} {
			pem := strings.ReplaceAll(pemBlock("RSA PRIVATE KEY", 5), "\n", eol)
			fs := d.Scan([]byte("a" + eol + "b" + eol + pem + "c" + eol))
			require.Len(t, fs, 1)
			// BEGIN is line 3, the body is lines 4 to 8, END is line 9.
			assert.Equal(t, [3]int{4, 8, 1}, [3]int{fs[0].StartLine, fs[0].EndLine, fs[0].StartCol})
		}
	})
	t.Run("sorted by position", func(t *testing.T) {
		fs := d.Scan([]byte("sk_test_" + fake(24, alnum) + "\n" + token + "\n"))
		require.Len(t, fs, 2)
		assert.Equal(t, "stripe-test-key", fs[0].RuleID)
		assert.Equal(t, "github-token", fs[1].RuleID)
	})
	t.Run("one finding per secret", func(t *testing.T) {
		content := "AWS_ACCESS_KEY_ID=AKIA" + fake(16, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567") + "\nAWS_SECRET_ACCESS_KEY=" + fake(40, b64) + "\n"
		fs := d.Scan([]byte(content))
		require.Len(t, fs, 2)
		assert.Equal(t, "aws-access-key-id", fs[0].RuleID)
		assert.Equal(t, "aws-secret-access-key", fs[1].RuleID)
	})
	t.Run("printing never shows the secret", func(t *testing.T) {
		fs := d.Scan([]byte(token))
		require.Len(t, fs, 1)
		for _, out := range []string{fmt.Sprint(fs[0]), fmt.Sprintf("%v %+v %#v %s", fs[0], fs[0], fs[0], fs[0]), fmt.Sprint(&fs[0]), fmt.Sprint(fs)} {
			assert.NotContains(t, out, token[4:])
			assert.NotContains(t, out, fmt.Sprint([]byte(token[4:12])), "no byte dump")
		}
	})
}

func TestScanSkips(t *testing.T) {
	d := allRulesDetector(t)
	token := "ghp_" + fake(36, alnum)

	assert.Len(t, d.Scan([]byte(token)), 1)
	assert.Empty(t, d.Scan(nil))
	assert.Empty(t, d.Scan([]byte("")))

	t.Run("binary", func(t *testing.T) {
		assert.Empty(t, d.Scan([]byte("\x00"+token)))
		assert.Empty(t, d.Scan([]byte(strings.Repeat("a", 7999)+"\x00"+token)))
		// A NUL after the first 8000 bytes doesn't make it binary, as in git.
		assert.Len(t, d.Scan([]byte(token+"\n"+strings.Repeat("a", 8000)+"\x00")), 1)
	})
	t.Run("LFS pointer", func(t *testing.T) {
		pointer := "version https://git-lfs.github.com/spec/v1\noid sha256:" + fake(16, hexa) + "\nsize 12345\n"
		assert.Empty(t, d.Scan([]byte(pointer+"token = "+token)))
	})
	t.Run("max content size", func(t *testing.T) {
		content := []byte(strings.Repeat("a\n", 100) + token)
		d := allRulesDetector(t)
		d.MaxContentSize = len(content) - 1
		assert.Empty(t, d.Scan(content))
		d.MaxContentSize = len(content)
		assert.Len(t, d.Scan(content), 1)
	})
	t.Run("long lines", func(t *testing.T) {
		d := allRulesDetector(t)
		d.MaxLineLength = 100
		long := strings.Repeat("a", 500) + " " + token // beyond the cut
		fs := d.Scan([]byte("x\n" + long + "\n" + token + "\n" + long))
		require.Len(t, fs, 1)
		assert.Equal(t, 3, fs[0].StartLine)
		// A token before the cut is found, a PEM block over short lines still is.
		fs = d.Scan([]byte(token + " " + strings.Repeat("a", 500) + "\n" + pemBlock("RSA PRIVATE KEY", 3)))
		assert.Len(t, fs, 2)
		d.MaxLineLength = 0
		assert.Len(t, d.Scan([]byte(long)), 1)
	})
	t.Run("matches per rule", func(t *testing.T) {
		d := allRulesDetector(t)
		var b strings.Builder
		for i := range 300 {
			b.WriteString("ghp_" + fake(36, alnum)[i%10:] + fake(i%10, alnum) + "\n")
		}
		assert.Len(t, d.Scan([]byte(b.String())), DefaultMaxMatchesPerRule)
		d.MaxMatchesPerRule = 5
		assert.Len(t, d.Scan([]byte(b.String())), 5)
	})
}

func TestContextDoesNotHideSecret(t *testing.T) {
	d := allRulesDetector(t)
	keyID := "AKIA" + fake(16, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567")
	// AWS XML answers and templates sit right next to real keys; the allowlist only looks at the secret.
	xml := "<AccessKeyId>" + keyID + "</AccessKeyId><SecretAccessKey>" + fake(40, b64) + "</SecretAccessKey>"
	assert.Equal(t, 1, countRule(d, xml, "aws-access-key-id"))
	assert.Equal(t, 1, countRule(d, xml, "aws-secret-access-key-near-id"))
	assert.Equal(t, 1, countRule(d, "{{ example }} token = "+keyID, "aws-access-key-id"))
}

func TestOverlappingFindingsCountOnce(t *testing.T) {
	d := allRulesDetector(t)
	content := gcpJSON("service_account", `-----BEGIN PRIVATE KEY-----\n`+strings.Repeat(fake(64, b64)+`\n`, 3)+`-----END PRIVATE KEY-----\n`)
	assert.Equal(t, 1, countRule(d, content, "gcp-service-account-key"))
	assert.Zero(t, countRule(d, content, "private-key"))
}

func TestNewDetectorRefusesBadRules(t *testing.T) {
	ok := Rule{ID: "x", Regex: `a(b)`, SecretGroup: 1, Keywords: []string{"a"}}
	_, err := NewDetector([]Rule{ok})
	require.NoError(t, err)
	for name, r := range map[string]Rule{
		"no ID":           {Regex: `a`},
		"bad regex":       {ID: "x", Regex: `a(`},
		"missing group":   {ID: "x", Regex: `a(b)`, SecretGroup: 2},
		"uppercase kword": {ID: "x", Regex: `a`, Keywords: []string{"A"}},
		"empty keyword":   {ID: "x", Regex: `a`, Keywords: []string{""}},
		"bad allowlist":   {ID: "x", Regex: `a`, Allowlist: []string{`(`}},
	} {
		_, err := NewDetector([]Rule{r})
		assert.Error(t, err, name)
	}
}

func TestRuleAllowlistAndKeywords(t *testing.T) {
	d, err := NewDetector([]Rule{{ID: "r", Regex: `secret-([0-9]{6})`, SecretGroup: 1, Keywords: []string{"secret-"}, Allowlist: []string{`000000`}}})
	require.NoError(t, err)
	assert.Len(t, d.Scan([]byte("secret-123456")), 1)
	assert.Empty(t, d.Scan([]byte("secret-000000")))
	d, err = NewDetector([]Rule{{ID: "r", Regex: `[0-9]{6}`, Keywords: []string{"needle"}}})
	require.NoError(t, err)
	assert.Empty(t, d.Scan([]byte("123456")))
	assert.Len(t, d.Scan([]byte("Needle 123456")), 1)
}

func TestFingerprint(t *testing.T) {
	a := Fingerprint([]byte("secret one"))
	assert.Equal(t, a, Fingerprint([]byte("secret one")))
	assert.NotEqual(t, a, Fingerprint([]byte("secret two")))
	assert.Regexp(t, `^[0-9a-f]{64}$`, a)

	sum := sha256Hex("secret one")
	assert.NotEqual(t, sum, a, "must not be the plain SHA-256")

	// The same secret found by two rules has the same fingerprint.
	d := allRulesDetector(t)
	hex40 := fake(40, hexa)
	fs := d.Scan([]byte("token = " + hex40 + "\n"))
	fs = append(fs, d.Scan([]byte("https://bob:"+hex40+"@git.corp.test/"))...)
	require.Len(t, fs, 2)
	assert.Equal(t, fs[0].Fingerprint(), fs[1].Fingerprint())
	assert.Equal(t, Fingerprint([]byte(hex40)), fs[0].Fingerprint())
}

func TestValidateCustomRule(t *testing.T) {
	require.NoError(t, ValidateCustomRule("Acme token", `ACME-[0-9]{8}`, "x = ACME-12345678"))
	require.NoError(t, ValidateCustomRule("Acme token", `acme_token=([a-z0-9]{12})`, "acme_token=abcdef123456"))

	tests := []struct {
		name, pattern, test string
		want                error
	}{
		{"", `ACME-[0-9]{8}`, "ACME-12345678", ErrCustomNameInvalid},
		{strings.Repeat("n", MaxCustomNameLen+1), `ACME-[0-9]{8}`, "ACME-12345678", ErrCustomNameInvalid},
		{"long", "A" + strings.Repeat("b", MaxCustomPatternLen), "A" + strings.Repeat("b", MaxCustomPatternLen), ErrPatternTooLong},
		{"invalid", `ACME-(`, "ACME-", ErrPatternInvalid},
		{"backtracking syntax", `(?=ACME)ACME`, "ACME", ErrPatternInvalid},
		{"empty", `[0-9]*`, "123", ErrPatternMatchesEmpty},
		{"empty alternative", `ACME|`, "ACME", ErrPatternMatchesEmpty},
		{"no match in test", `ACME-[0-9]{8}`, "ACME-1234", ErrPatternNoMatchInTest},
		{"empty test", `ACME-[0-9]{8}`, "", ErrPatternNoMatchInTest},
		{"everything", `.+`, "anything", ErrPatternMatchesTooMuch},
		{"dot star in text", `x.*`, "x", ErrPatternMatchesTooMuch},
		{"words", `\w{3,}`, "abc", ErrPatternMatchesTooMuch},
		{"digits", `[0-9]`, "1", ErrPatternMatchesTooMuch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name := tt.name
			if tt.want != ErrCustomNameInvalid {
				name = "rule"
			}
			assert.ErrorIs(t, ValidateCustomRule(name, tt.pattern, tt.test), tt.want)
		})
	}
}

func TestCorpusIsFixedOrdinaryText(t *testing.T) {
	c := corpus()
	assert.Len(t, c, len(corpus()))
	assert.GreaterOrEqual(t, len(c), 64<<10)
	assert.Equal(t, c, corpus())
	// No built-in secret rule fires on it.
	assert.Empty(t, allRulesDetector(t).Scan(c))
}

// Scan time must grow linearly with the size of the content, whatever it
// looks like. Run with: go test -run xxx -bench . ./modules/secretdetection/
func BenchmarkScan(b *testing.B) {
	d := allRulesDetector(b)
	noCut, err := NewDetector(BuiltinRules())
	require.NoError(b, err)
	noCut.MaxLineLength = 0 // the match cap alone must keep it linear

	for _, size := range []int{1 << 20, 10 << 20} {
		inputs := map[string][]byte{
			"minified-js":   minifiedJS(size),
			"random-base64": randomBase64(size),
		}
		for name, content := range inputs {
			for dname, det := range map[string]*Detector{"default": d, "no-line-cut": noCut} {
				b.Run(fmt.Sprintf("%s/%dMiB/%s", name, size>>20, dname), func(b *testing.B) {
					b.SetBytes(int64(len(content)))
					for b.Loop() {
						det.Scan(content)
					}
				})
			}
		}
	}
}

// minifiedJS is size bytes on one line, full of the keywords rules look for.
func minifiedJS(size int) []byte {
	r := rand.New(rand.NewPCG(1, 2))
	var b strings.Builder
	for b.Len() < size {
		fmt.Fprintf(&b, `function f%d(a,b){var token=a.key+"%d";if(b.password==="x"){return eyJ%d}return{secret:b,akia:%d}};`, r.IntN(1000), r.IntN(1e6), r.IntN(1e6), r.IntN(100))
	}
	return []byte(b.String()[:size])
}

// randomBase64 is size bytes of base64 in 76 column lines.
func randomBase64(size int) []byte {
	r := rand.New(rand.NewPCG(3, 4))
	out := make([]byte, 0, size)
	for len(out) < size {
		out = append(out, b64[r.IntN(len(b64))])
		if len(out)%77 == 76 {
			out = append(out, '\n')
		}
	}
	return out[:size]
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
