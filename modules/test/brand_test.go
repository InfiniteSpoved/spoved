// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBrandMentions(t *testing.T) {
	assert.Empty(t, BrandMentions(`<html><head><title>CodeHat</title><script>var x = "Forgejo";</script></head>
<body>Powered by CodeHat <a class="codehat-credit" href="https://forgejo.org">based on Forgejo</a></body></html>`))

	assert.Equal(t, []string{"Welcome to Forgejo"}, BrandMentions(`<body><p>Welcome to Forgejo</p></body>`))
	assert.Equal(t, []string{"GitHat"}, BrandMentions(`<head><title>GitHat</title></head>`))
	assert.Equal(t, []string{"Forgejo"}, BrandMentions(`<input placeholder="Forgejo">`))
	assert.Equal(t, []string{"Forgejo forge"}, BrandMentions(`<meta property="og:description" content="Forgejo forge">`))
	assert.Equal(t, []string{"Forgejo"}, BrandMentions(`<a class="other" href="#">Forgejo</a>`))
}
