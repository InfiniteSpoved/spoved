// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// NOTICE: many of the patterns below are adapted from gitleaks v8.28.0,
// config/gitleaks.toml (https://github.com/gitleaks/gitleaks/tree/v8.28.0),
// which is licensed under the MIT licence:
//
//	MIT License
//
//	Copyright (c) 2019 Zachary Rice
//
//	Permission is hereby granted, free of charge, to any person obtaining a copy
//	of this software and associated documentation files (the "Software"), to deal
//	in the Software without restriction, including without limitation the rights
//	to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
//	copies of the Software, and to permit persons to whom the Software is
//	furnished to do so, subject to the following conditions:
//
//	The above copyright notice and this permission notice shall be included in all
//	copies or substantial portions of the Software.
//
//	THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
//	IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
//	FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
//	AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
//	LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
//	OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
//	SOFTWARE.
//
// Changes: narrowed to the rules listed here, secret groups and boundaries
// reworked, allowlists and the JWT/GCP/Forgejo/AWS-secret rules are our own.

package secretdetection

import (
	"encoding/base64"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"forgejo.org/modules/json"
)

// placeholders are dropped by every built-in rule: documentation examples,
// runs of x, ${VAR} and {{ var }} templates, <token> markers.
var placeholders = []string{`(?i)example`, `(?i)x{5,}`, `\$\{`, `\{\{`, `<[^<>\s]{2,40}>`}

// Sets of bytes for Rule.TokenChars.
const (
	alnumChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	wordChars  = alnumChars + "_-"
)

var builtinRules = []Rule{
	{
		ID: "aws-access-key-id", Name: "AWS access key ID", Type: "aws",
		Keywords:    []string{"akia", "asia", "abia", "acca", "a3t"},
		Regex:       `((?:A3T[A-Z0-9]|AKIA|ASIA|ABIA|ACCA)[A-Z2-7]{16})`,
		SecretGroup: 1, MinEntropy: 3, TokenChars: alnumChars,
	},
	{
		// The secret key is 40 base64 characters, which is too common alone:
		// it counts after aws_secret_access_key (or similar) = ... or a space.
		ID: "aws-secret-access-key", Name: "AWS secret access key", Type: "aws",
		Keywords:    []string{"aws"},
		Regex:       `(?i)aws[\w.-]{0,30}secret[\w.-]{0,30}["']?(?:\s*(?::=|=>|[:=])\s*|\s+)["']?([A-Za-z0-9/+=]{40})(?:[^A-Za-z0-9/+=]|$)`,
		SecretGroup: 1, MinEntropy: 4, Window: 256, Anchored: true,
	},
	{
		// ... or within 120 bytes after an access key ID.
		ID: "aws-secret-access-key-near-id", Name: "AWS secret access key next to a key ID", Type: "aws",
		Keywords:    []string{"akia", "asia", "abia", "acca", "a3t"},
		Regex:       `\b(?:A3T[A-Z0-9]|AKIA|ASIA|ABIA|ACCA)[A-Z2-7]{16}\b[\s\S]{0,120}?[^A-Za-z0-9/+=]([A-Za-z0-9/+=]{40})(?:[^A-Za-z0-9/+=]|$)`,
		SecretGroup: 1, MinEntropy: 4.3, Window: 256, Anchored: true,
	},
	{
		ID: "azure-storage-account-key", Name: "Azure storage account key", Type: "azure",
		Keywords:    []string{"accountkey", "storage_key", "storage_account_key", "storageaccount_key", "storagekey", "storageaccountkey"},
		Regex:       `(?i)(?:accountkey|storage_?(?:account_?)?key)["']?\s*[:=]\s*["']?([A-Za-z0-9+/]{86}==)`,
		SecretGroup: 1, MinEntropy: 4,
		Allowlist: []string{"^" + regexp.QuoteMeta(azuriteKey) + "$"},
	},
	{
		// Service Bus, Event Hub and IoT Hub connection strings.
		ID: "azure-shared-access-key", Name: "Azure shared access key", Type: "azure",
		Keywords:    []string{"sharedaccesskey"},
		Regex:       `(?i)sharedaccesskey["']?\s*[:=]\s*["']?([A-Za-z0-9+/]{43}=)`,
		SecretGroup: 1, MinEntropy: 4,
	},
	{
		// The 84 character PAT format (2024) has AZDO at bytes 77 to 80.
		ID: "azure-devops-pat", Name: "Azure DevOps personal access token", Type: "azure",
		Keywords:    []string{"azdo"},
		Regex:       `([A-Za-z0-9]{76}AZDO[A-Za-z0-9]{4})`,
		SecretGroup: 1, MinEntropy: 3.5, TokenChars: alnumChars,
		// The regex has no literal prefix: the hit "AZDO" (this case only) sits
		// 76 bytes into the match, and 76 alphanumerics must come before it.
		Window: 4, Anchored: true, KeywordOffset: 76, HitCheck: azdoHit,
	},
	{
		ID: "azure-ad-client-secret", Name: "Azure AD client secret", Type: "azure",
		Keywords:    []string{"q~"},
		Regex:       `([a-zA-Z0-9_~.]{3}\dQ~[a-zA-Z0-9_~.-]{31,34})`,
		SecretGroup: 1, MinEntropy: 3, TokenChars: alnumChars + "_~.-", TrailingOK: ".",
	},
	{
		// type and private_key sit in the same JSON object, type first. The
		// secret is the key body, as for the private-key rule, so one key has
		// one fingerprint wherever it is stored.
		ID: "gcp-service-account-key", Name: "GCP service account private key", Type: "gcp",
		Keywords:    []string{"service_account"},
		Regex:       `"type"\s*:\s*"service_account"[\s\S]{0,600}?"private_key"\s*:\s*"-----BEGIN (?:RSA )?PRIVATE KEY-----\s*((?:\\+[nrt/]|\\u[0-9a-fA-F]{4}|[A-Za-z0-9+/=\s]){64,}?)(?:\\+[nrt]|\s)*-----END (?:RSA )?PRIVATE KEY-----`,
		SecretGroup: 1, MinEntropy: 3, Normalize: normalizePEM, MaxSecretLen: maxPrivateKeyLen,
	},
	{
		ID: "gcp-api-key", Name: "GCP API key", Type: "gcp",
		Keywords:    []string{"aiza"},
		Regex:       `(AIza[\w-]{35})`,
		SecretGroup: 1, MinEntropy: 3.5, TokenChars: wordChars, TrailingOK: "-",
	},
	{
		// ghp_ personal, gho_ OAuth, ghu_ user-to-server, ghs_ server-to-server, ghr_ refresh.
		ID: "github-token", Name: "GitHub token", Type: "github",
		Keywords:    []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_"},
		Regex:       `(gh[pousr]_[0-9A-Za-z]{36,251})`,
		SecretGroup: 1, MinEntropy: 3, TokenChars: alnumChars,
	},
	{
		ID: "github-fine-grained-pat", Name: "GitHub fine-grained personal access token", Type: "github",
		Keywords:    []string{"github_pat_"},
		Regex:       `(github_pat_[0-9A-Za-z_]{82})`,
		SecretGroup: 1, MinEntropy: 3, TokenChars: alnumChars + "_",
	},
	{
		// personal, deploy, runner, pipeline trigger, feature flag, incoming
		// mail, agent, CI job, SCIM, feature flag client and OAuth application
		// secret tokens, the routable form (a dot and 9 characters at the
		// end) and the runner registration token.
		ID: "gitlab-token", Name: "GitLab token", Type: "gitlab",
		Keywords:    []string{"glpat-", "gldt-", "glrt-", "glptt-", "glft-", "glimt-", "glagent-", "glcbt-", "glsoat-", "glffct-", "gloas-", "gr1348941"},
		Regex:       `(gl(?:pat|dt|rt|ptt|ft|imt|agent|cbt|soat|ffct|oas)-[0-9A-Za-z_-]{20,300}(?:\.[0-9a-z]{9})?|GR1348941[0-9A-Za-z_-]{20})`,
		SecretGroup: 1, MinEntropy: 3, TokenChars: wordChars,
	},
	// An access token of Forgejo, Gitea and spoved is 40 hex characters (hex
	// of 20 random bytes, models/auth/access_token.go) with no prefix, so it
	// only counts in a context that says it is a credential. One rule per
	// context, so each regex starts with its own words: `token = <hex>`,
	// `Authorization: token <hex>` (also as a JSON or YAML key), a .netrc
	// password and the password of a URL (https://user:<hex>@host).
	{
		ID: "forgejo-access-token", Name: "Forgejo, Gitea or spoved access token", Type: "forgejo",
		Keywords:    []string{"token"},
		Regex:       `(?i:token)[\w.-]{0,20}["']?\s*[:=]\s*["']?([0-9a-f]{40})`,
		SecretGroup: 1, MinEntropy: 3, TokenChars: alnumChars, Window: 100, Anchored: true,
	},
	{
		ID: "forgejo-access-token-header", Name: "Forgejo, Gitea or spoved access token in an Authorization header", Type: "forgejo",
		Keywords:    []string{"authorization"},
		Regex:       `(?i:authorization)["']?\s*[:=]\s*["']?(?i:token|bearer)\s+([0-9a-f]{40})`,
		SecretGroup: 1, MinEntropy: 3, TokenChars: alnumChars,
	},
	{
		ID: "forgejo-access-token-netrc", Name: "Forgejo, Gitea or spoved access token in a .netrc file", Type: "forgejo",
		Keywords:    []string{"machine"},
		Regex:       `machine\s+\S{1,64}\s+login\s+\S{1,64}\s+password\s+([0-9a-f]{40})`,
		SecretGroup: 1, MinEntropy: 3, TokenChars: alnumChars,
	},
	{
		ID: "forgejo-access-token-url", Name: "Forgejo, Gitea or spoved access token in a URL", Type: "forgejo",
		Keywords:    []string{"://"},
		Regex:       `://(?:[^\s/:@]{1,64}:)?([0-9a-f]{40})@`,
		SecretGroup: 1, MinEntropy: 3, TokenChars: alnumChars,
	},
	{
		// OAuth2 application client secrets (gto_) and authorization codes
		// (gta_) of the instance: base32 of 32 random bytes (models/auth/oauth2.go).
		ID: "forgejo-oauth2-secret", Name: "Forgejo, Gitea or spoved OAuth2 secret", Type: "forgejo",
		Keywords:    []string{"gto_", "gta_"},
		Regex:       `(gt[ao]_[a-z2-7]{52})`,
		SecretGroup: 1, MinEntropy: 3, TokenChars: alnumChars,
	},
	{
		// PEM private keys, RSA, EC, DSA, OPENSSH, PGP and plain PKCS#8. The
		// secret is the block body, without the BEGIN and END lines, with
		// optional armor headers (Proc-Type, Comment) in front.
		ID: "private-key", Name: "Private key", Type: "private-key",
		Keywords:    []string{"-----begin"},
		Regex:       `(?i)-----BEGIN[ A-Z0-9_-]{0,100}PRIVATE KEY(?: BLOCK)?-----(?:\s|\\+[nrt])*((?:[A-Za-z][A-Za-z-]{1,29}:[^\r\n\\]{0,200}(?:[\r\n]|\\+[nrt])+\s*)*[A-Za-z0-9+/=\s\\]{64,}?)\s*-----END[ A-Z0-9_-]{0,100}PRIVATE KEY(?: BLOCK)?-----`,
		SecretGroup: 1, MinEntropy: 3, Normalize: normalizePEM, MaxSecretLen: maxPrivateKeyLen,
	},
	{
		// xoxb bot, xoxp user, xoxa app, xoxr refresh (also the legacy forms
		// with one group of digits), xapp app-level, xoxe rotating tokens.
		ID: "slack-token", Name: "Slack token", Type: "slack",
		Keywords:    []string{"xoxb-", "xoxp-", "xoxa-", "xoxr-", "xapp-", "xoxe"},
		Regex:       `(xapp-[0-9]-[A-Za-z0-9]{6,}-[0-9]{8,}-[A-Za-z0-9]{32,}|xoxe\.xox[bp]-[0-9]-[A-Za-z0-9]{100,}|xoxe-[0-9]-[A-Za-z0-9]{100,}|xox[bpar]-[0-9]{1,14}(?:-[0-9A-Za-z]{8,40}){1,3})`,
		SecretGroup: 1, MinEntropy: 2, TokenChars: alnumChars,
	},
	{
		ID: "slack-webhook-url", Name: "Slack webhook URL", Type: "slack",
		Keywords:    []string{"hooks.slack.com"},
		Regex:       `hooks\.slack\.com/(?:services|workflows|triggers)/([A-Za-z0-9+/]{43,56})`,
		SecretGroup: 1, MinEntropy: 3,
	},
	{
		ID: "stripe-live-key", Name: "Stripe live secret key", Type: "stripe",
		Keywords:    []string{"sk_live_", "rk_live_"},
		Regex:       `((?:sk|rk)_live_[0-9A-Za-z]{10,247})`,
		SecretGroup: 1, MinEntropy: 2, TokenChars: alnumChars,
	},
	{
		// Its own rule so users can mark test keys as "used in tests".
		ID: "stripe-test-key", Name: "Stripe test secret key", Type: "stripe",
		Keywords:    []string{"sk_test_", "rk_test_"},
		Regex:       `((?:sk|rk)_test_[0-9A-Za-z]{10,247})`,
		SecretGroup: 1, MinEntropy: 2, TokenChars: alnumChars,
	},
	{
		// Three base64url parts; header and payload start with eyJ (`{"`). The
		// signature may be empty (alg none, which still counts). The token of
		// jwt.io's documentation is no secret.
		ID: "jwt", Name: "JSON Web Token", Type: "jwt",
		Keywords:    []string{"eyj"},
		Regex:       `(eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]*)`,
		SecretGroup: 1, MinEntropy: 3, TokenChars: wordChars,
		Check:     jwtHeaderHasAlg,
		Allowlist: []string{`\.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c$`},
	},
	{
		// Dotted names (locale keys, settings) are no secrets.
		ID: "generic-secret-assignment", Name: "Generic secret assignment", Type: "generic",
		Keywords:    []string{"secret", "password", "passwd", "token", "key"},
		Regex:       `(?i)(?:secret|password|passwd|token|key)[\w.-]{0,20}["']?\s*(?::=|[:=])\s*["']([^\s"'\x60]{20,100})["']`,
		SecretGroup: 1, MinEntropy: 3.5, DefaultOff: true, Window: 256, Anchored: true,
		Allowlist: []string{`^[A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)+$`},
	},
}

// azdoHit: AZDO in capitals with 76 alphanumeric bytes before it and 4 after.
func azdoHit(c []byte, p int) bool {
	if p < 76 || p+8 > len(c) || string(c[p:p+4]) != "AZDO" {
		return false
	}
	for _, b := range c[p-76 : p] {
		if !isAlnum(b) {
			return false
		}
	}
	for _, b := range c[p+4 : p+8] {
		if !isAlnum(b) {
			return false
		}
	}
	return true
}

func isAlnum(b byte) bool {
	return '0' <= b && b <= '9' || 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z'
}

// azuriteKey is the storage account key of the Azurite emulator, the same on
// every machine and in the Azure documentation. It is built from parts so the
// source holds no full key literal.
var azuriteKey = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/" + "K1SZFPTOtr/KBHBeksoGMGw=="

const maxPrivateKeyLen = 64 << 10 // PGP exports with several subkeys pass 16 KiB

// normalizePEM is the canonical form of a private key body: up to three
// layers of string escaping undone (JSON, PHP, shell: \\, \/, \n, \r, \t,
// \uXXXX), the armor header lines (Proc-Type, Comment, ...) dropped and all
// whitespace removed. If what is left is not base64 (text between the two
// dashed lines, an escape this doesn't know), the whitespace-stripped raw body
// is used instead: a match is never dropped, it only gets a less stable
// fingerprint.
func normalizePEM(secret []byte) []byte {
	s := string(secret)
	for range 3 {
		if !strings.Contains(s, `\`) {
			break
		}
		s = unescape(s)
	}
	var out []byte
	for line := range strings.SplitSeq(strings.ReplaceAll(s, "\r", "\n"), "\n") {
		if strings.Contains(line, ":") {
			continue
		}
		for _, c := range []byte(line) {
			switch {
			case c <= ' ':
			case 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '+' || c == '/' || c == '=':
				out = append(out, c)
			default:
				return stripSpace(secret)
			}
		}
	}
	return out
}

func stripSpace(b []byte) []byte {
	return slices.DeleteFunc(slices.Clone(b), func(c byte) bool { return c <= ' ' })
}

var controlEscapes = map[byte]byte{'n': '\n', 'r': '\r', 't': '\t'}

// unescape undoes one layer of backslash escapes.
func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n', 'r', 't':
			b.WriteByte(controlEscapes[s[i]])
		case '\\', '/':
			b.WriteByte(s[i])
		case 'u':
			if n, err := strconv.ParseUint(s[min(i+1, len(s)):min(i+5, len(s))], 16, 16); err == nil && i+5 <= len(s) {
				b.WriteRune(rune(n))
				i += 4
				continue
			}
			b.WriteString(`\u`)
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// jwtHeaderHasAlg: the first part of a JWT is a JSON object with "alg".
func jwtHeaderHasAlg(secret []byte) bool {
	head, _, _ := strings.Cut(string(secret), ".")
	raw, err := base64.RawURLEncoding.DecodeString(head)
	if err != nil {
		return false
	}
	var header map[string]any
	if json.Unmarshal(raw, &header) != nil {
		return false
	}
	_, ok := header["alg"]
	return ok
}

// BuiltinRules returns a copy of the built-in rules, generic ones (DefaultOff)
// included. Choosing which to use is up to the caller.
func BuiltinRules() []Rule {
	rules := make([]Rule, len(builtinRules))
	for i, r := range builtinRules {
		r.Keywords = slices.Clone(r.Keywords)
		r.Allowlist = append(slices.Clone(placeholders), r.Allowlist...)
		rules[i] = r
	}
	return rules
}

// RuleByID returns the built-in rule with that ID.
func RuleByID(id string) (Rule, bool) {
	for _, r := range BuiltinRules() {
		if r.ID == id {
			return r, true
		}
	}
	return Rule{}, false
}
