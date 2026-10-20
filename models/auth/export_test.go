// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package auth

import (
	"testing"

	lru "github.com/hashicorp/golang-lru/v2"
)

// MockSuccessfulAccessTokenCache turns the successful token cache on for one test.
func MockSuccessfulAccessTokenCache(t *testing.T) {
	old := successfulAccessTokenCache
	successfulAccessTokenCache, _ = lru.New[string, any](10)
	t.Cleanup(func() { successfulAccessTokenCache = old })
}
