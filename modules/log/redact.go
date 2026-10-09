// Copyright 2026 The Forgejo Authors.
// SPDX-License-Identifier: GPL-3.0-or-later

package log

import (
	"regexp"
	"slices"
	"strings"
	"sync"
)

const redacted = "[REDACTED]"

const secretNames = `(?:[a-z0-9]+[_-])*(?:password|passwd|passphrase|pass|secret|token|apikey|api[_-]key|key|signature|sig)`

var (
	// header: the value runs to the end of the line or the closing quote of its JSON/quoted form
	redactHeader = regexp.MustCompile(`(?i)\b((?:proxy-)?authorization|set-cookie|cookie|x-api-key|api-key|x-license-key)(["']?\s*[:=]\s*["']?)[^\r\n"']*`)
	// "Bearer x" / "Basic x": prose such as "basic settings" has only letters and is left alone
	redactScheme = regexp.MustCompile(`(?i)\b(bearer|basic)(\s+)([A-Za-z0-9._~+/=-]{8,})`)
	// name=value, "name":"value" and 'name': 'value' (also in URL queries)
	redactPair = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9])(` + secretNames + `)(["']\s*:\s*["']?|\s*=\s*["']?)[^&\s"'<>,;)}\]#]+`)
	// OAuth code, only in a query string to leave prose alone
	redactCode     = regexp.MustCompile(`(?i)([?&]code=)[^&\s"'<>,;)}\]#]+`)
	redactUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^/\s:@]+):[^@/\s]+@`)
	redactShapes   = regexp.MustCompile(`\b(?:(?:gta|gto|gtr)_[A-Za-z0-9]{10,}|glpat-[A-Za-z0-9_-]{10,}|gh[pos]_[A-Za-z0-9]{20,}|github_pat_\w{20,}|NRAK-[A-Z0-9]{10,}|xox[abp]-[A-Za-z0-9-]{10,}|eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]+)|-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`)

	secretsMu sync.RWMutex
	secrets   []string
)

// RegisterSecret makes Redact replace the literal value, e.g. a configured API key.
func RegisterSecret(s string) {
	if s == "" {
		return
	}
	secretsMu.Lock()
	defer secretsMu.Unlock()
	if !slices.Contains(secrets, s) {
		secrets = append(secrets, s)
	}
}

// Redact hides credentials in free text, header lines and URLs. It is applied to the JSON log format only.
func Redact(s string) string {
	secretsMu.RLock()
	for _, secret := range secrets {
		s = strings.ReplaceAll(s, secret, redacted)
	}
	secretsMu.RUnlock()

	s = redactShapes.ReplaceAllString(s, redacted)
	s = redactHeader.ReplaceAllString(s, "${1}${2}"+redacted)
	s = redactScheme.ReplaceAllStringFunc(s, func(m string) string {
		sub := redactScheme.FindStringSubmatch(m)
		if strings.Trim(sub[3], "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") == "" {
			return m
		}
		return sub[1] + sub[2] + redacted
	})
	s = redactPair.ReplaceAllString(s, "${1}${2}${3}"+redacted)
	s = redactCode.ReplaceAllString(s, "${1}"+redacted)
	return redactUserinfo.ReplaceAllString(s, "${1}:"+redacted+"@")
}
