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
	"slices"
	"strings"

	"forgejo.org/modules/json"
)

// placeholders are dropped by every built-in rule: documentation examples,
// runs of x, ${VAR} and {{ var }} templates, <token> markers.
var placeholders = []string{`(?i)example`, `(?i)x{5,}`, `\$\{`, `\{\{`, `<[^<>\s]{2,40}>`}

var builtinRules = []Rule{
	{
		ID: "aws-access-key-id", Name: "AWS access key ID", Type: "aws",
		Keywords:    []string{"akia", "asia", "abia", "acca", "a3t"},
		Regex:       `\b((?:A3T[A-Z0-9]|AKIA|ASIA|ABIA|ACCA)[A-Z2-7]{16})\b`,
		SecretGroup: 1, MinEntropy: 3,
	},
	{
		// The secret key is 40 base64 characters, which is too common alone:
		// it counts after aws_secret_access_key (or similar) = ...
		ID: "aws-secret-access-key", Name: "AWS secret access key", Type: "aws",
		Keywords:    []string{"aws"},
		Regex:       `(?i)aws[\w.-]{0,30}secret[\w.-]{0,30}["']?\s*(?::=|=>|[:=])\s*["']?([A-Za-z0-9/+=]{40})(?:[^A-Za-z0-9/+=]|$)`,
		SecretGroup: 1, MinEntropy: 4,
	},
	{
		// ... or within 120 bytes after an access key ID.
		ID: "aws-secret-access-key-near-id", Name: "AWS secret access key next to a key ID", Type: "aws",
		Keywords:    []string{"akia", "asia", "abia", "acca", "a3t"},
		Regex:       `\b(?:A3T[A-Z0-9]|AKIA|ASIA|ABIA|ACCA)[A-Z2-7]{16}\b[\s\S]{0,120}?[^A-Za-z0-9/+=]([A-Za-z0-9/+=]{40})(?:[^A-Za-z0-9/+=]|$)`,
		SecretGroup: 1, MinEntropy: 4.3,
	},
	{
		ID: "azure-storage-account-key", Name: "Azure storage account key", Type: "azure",
		Keywords:    []string{"accountkey"},
		Regex:       `(?i)accountkey=([A-Za-z0-9+/]{86}==)`,
		SecretGroup: 1, MinEntropy: 4,
	},
	{
		ID: "azure-ad-client-secret", Name: "Azure AD client secret", Type: "azure",
		Keywords:    []string{"q~"},
		Regex:       `(?:^|[\\'"\x60\s>=:(,)])([a-zA-Z0-9_~.]{3}\dQ~[a-zA-Z0-9_~.-]{31,34})(?:$|[\\'"\x60\s<),])`,
		SecretGroup: 1, MinEntropy: 3,
	},
	{
		// type and private_key sit in the same JSON object, type first.
		ID: "gcp-service-account-key", Name: "GCP service account private key", Type: "gcp",
		Keywords:    []string{"service_account"},
		Regex:       `"type"\s*:\s*"service_account"[\s\S]{0,600}?"private_key"\s*:\s*"(-----BEGIN (?:RSA )?PRIVATE KEY-----(?:\\[nr]|[A-Za-z0-9+/=\s]){64,}?-----END (?:RSA )?PRIVATE KEY-----)`,
		SecretGroup: 1,
	},
	{
		ID: "gcp-api-key", Name: "GCP API key", Type: "gcp",
		Keywords:    []string{"aiza"},
		Regex:       `\b(AIza[\w-]{35})(?:[\x60'"\s;]|\\[nr]|$)`,
		SecretGroup: 1, MinEntropy: 3.5,
	},
	{
		// ghp_ personal, gho_ OAuth, ghu_ user-to-server, ghs_ server-to-server, ghr_ refresh.
		ID: "github-token", Name: "GitHub token", Type: "github",
		Keywords:    []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_"},
		Regex:       `\b(gh[pousr]_[0-9A-Za-z]{36})\b`,
		SecretGroup: 1, MinEntropy: 3,
	},
	{
		ID: "github-fine-grained-pat", Name: "GitHub fine-grained personal access token", Type: "github",
		Keywords:    []string{"github_pat_"},
		Regex:       `\b(github_pat_[0-9A-Za-z_]{82})\b`,
		SecretGroup: 1, MinEntropy: 3,
	},
	{
		// personal, deploy, runner, pipeline trigger, feature flag, incoming mail,
		// agent and OAuth application secret tokens.
		ID: "gitlab-token", Name: "GitLab token", Type: "gitlab",
		Keywords:    []string{"glpat-", "gldt-", "glrt-", "glptt-", "glft-", "glimt-", "glagent-", "gloas-"},
		Regex:       `\b(gl(?:pat|dt|rt|ptt|ft|imt|agent|oas)-[0-9A-Za-z_-]{20,50})(?:[^0-9A-Za-z_-]|$)`,
		SecretGroup: 1, MinEntropy: 3,
	},
	{
		// An access token of Forgejo, Gitea and spoved is 40 hex characters
		// (hex of 20 random bytes, models/auth/access_token.go) with no prefix,
		// so it only counts in a context that says it is a credential:
		// `token = <hex>`, `Authorization: token <hex>`, a .netrc password or
		// the password of a URL (https://user:<hex>@host).
		ID: "forgejo-access-token", Name: "Forgejo, Gitea or spoved access token", Type: "forgejo",
		Keywords: []string{"token", "bearer", "machine", "://"},
		Regex: `(?i)(?:[\w.-]{0,30}token[\w.-]{0,20}["']?\s*[:=]\s*["']?` +
			`|authorization:\s*(?:token|bearer)\s+` +
			`|machine\s+\S{1,100}\s+login\s+\S{1,100}\s+password\s+` +
			`|://(?:[^\s/:@]{1,100}:)?)([0-9a-f]{40})\b`,
		SecretGroup: 1, MinEntropy: 3,
	},
	{
		// PEM private keys, RSA, EC, DSA, OPENSSH, PGP and plain PKCS#8. The
		// secret is the block body, without the BEGIN and END lines.
		ID: "private-key", Name: "Private key", Type: "private-key",
		Keywords:    []string{"-----begin"},
		Regex:       `(?i)-----BEGIN[ A-Z0-9_-]{0,100}PRIVATE KEY(?: BLOCK)?-----\s*([A-Za-z0-9+/=\s:,.\\-]{64,}?)\s*-----END[ A-Z0-9_-]{0,100}PRIVATE KEY(?: BLOCK)?-----`,
		SecretGroup: 1, MinEntropy: 3,
	},
	{
		// xoxb bot, xoxp user, xoxa app, xoxr refresh.
		ID: "slack-token", Name: "Slack token", Type: "slack",
		Keywords:    []string{"xoxb-", "xoxp-", "xoxa-", "xoxr-"},
		Regex:       `\b(xox[bpar]-[0-9]{1,14}(?:-[0-9A-Za-z]{8,40}){2,3})\b`,
		SecretGroup: 1, MinEntropy: 2,
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
		Regex:       `\b((?:sk|rk)_live_[0-9A-Za-z]{10,99})\b`,
		SecretGroup: 1, MinEntropy: 2,
	},
	{
		// Its own rule so users can mark test keys as "used in tests".
		ID: "stripe-test-key", Name: "Stripe test secret key", Type: "stripe",
		Keywords:    []string{"sk_test_", "rk_test_"},
		Regex:       `\b((?:sk|rk)_test_[0-9A-Za-z]{10,99})\b`,
		SecretGroup: 1, MinEntropy: 2,
	},
	{
		// Three base64url parts; header and payload start with eyJ (`{"`). The
		// signature may be empty (alg none, which still counts).
		ID: "jwt", Name: "JSON Web Token", Type: "jwt",
		Keywords:    []string{"eyj"},
		Regex:       `\b(eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]*)`,
		SecretGroup: 1, MinEntropy: 3,
		Check: jwtHeaderHasAlg,
	},
	{
		ID: "generic-secret-assignment", Name: "Generic secret assignment", Type: "generic",
		Keywords:    []string{"secret", "password", "passwd", "token", "key"},
		Regex:       `(?i)(?:secret|password|passwd|token|key)[\w.-]{0,20}["']?\s*(?::=|[:=])\s*["']([^\s"'\x60]{20,100})["']`,
		SecretGroup: 1, MinEntropy: 3.5, DefaultOff: true,
	},
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
		r.Allowlist = slices.Clone(placeholders)
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
