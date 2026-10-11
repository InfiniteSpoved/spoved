// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

var oldBrandRe = regexp.MustCompile(`Forgejo|Gitea|GitHat|CodeHat`)

// BrandMentions only sees rendered HTML. These strings reach users another way (plain-text 500 pages, the git
// login prompt, a Teams card, the JS error banner, API docs, package install snippets), so scan their source.
func TestNoOldBrandInUserFacingSource(t *testing.T) {
	literal := regexp.MustCompile("[\"`][^\"`\\n]*(Forgejo|Gitea|GitHat|CodeHat)[^\"`\\n]*[\"`]")
	for _, f := range []string{
		"routers/common/errpage.go",
		"services/context/context_response.go",
		"routers/web/githttp.go",
		"routers/web/repo/githttp.go",
		"routers/api/packages/api.go",
		"services/webhook/msteams.go",
		"web_src/js/bootstrap.js",
	} {
		for i, line := range readLines(t, filepath.Join("../..", f)) {
			if trimmed := strings.TrimSpace(line); !strings.HasPrefix(trimmed, "//") && !strings.HasPrefix(trimmed, "*") {
				assert.Empty(t, literal.FindString(line), "%s:%d", f, i+1)
			}
		}
	}

	// the registry names in the copy-paste snippets
	files, err := filepath.Glob("../../templates/package/content/*.tmpl")
	assert.NoError(t, err)
	for _, f := range files {
		for i, line := range readLines(t, f) {
			assert.Empty(t, regexp.MustCompile(`(?i)\b(forgejo|gitea)\b`).FindString(line), "%s:%d", f, i+1)
		}
	}

	// API docs: only upstream references that are true as written stay
	allowed := strings.NewReplacer(
		"Forgejo API", "", // the API's name, kept by docs/branding.md
		"deprecated in Forgejo 15", "", // upstream version numbers
		"forgejo.org/docs", "", // upstream documentation
		".forgejo/", "", // the workflow directory
	)
	text := regexp.MustCompile(`"(description|summary|title)": "[^\n]*`)
	for i, line := range readLines(t, "../../templates/swagger/v1_json.tmpl") {
		if text.MatchString(line) {
			assert.Empty(t, oldBrandRe.FindString(allowed.Replace(line)), "swagger:%d", i+1)
		}
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	assert.NoError(t, err)
	return strings.Split(string(b), "\n")
}
