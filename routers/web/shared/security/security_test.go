// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package security

import (
	"testing"

	"forgejo.org/modules/optional"

	"github.com/stretchr/testify/assert"
)

func TestFormVersionOrRange(t *testing.T) {
	for _, v := range []string{"4.17.20", "v1.2.3", "1.0.0-rc.1", "2.1.0+build", "1"} {
		assert.False(t, isRange(v), v)
		assert.Equal(t, v, Form{Version: v}.usage().Version, v)
		assert.Empty(t, Form{Version: v}.usage().Range, v)
	}
	for _, v := range []string{"^4.0.0", "~1.2", ">=2,<3", ">1 <2", "1.x", "1.2.X", "*", "1 - 2", "[1.0,2.0)", "1 || 2", "!=1.0"} {
		assert.True(t, isRange(v), v)
		assert.Equal(t, v, Form{Version: v}.usage().Range, v)
		assert.Empty(t, Form{Version: v}.usage().Version, v)
	}
}

func TestFormSearched(t *testing.T) {
	assert.False(t, Form{}.searched(Vulnerabilities))
	assert.False(t, Form{Ecosystem: "npm", State: "all", Project: "x", Owner: "y"}.searched(Vulnerabilities), "only the fields that name something count")
	assert.True(t, Form{Advisory: "CVE-2024-1"}.searched(Vulnerabilities))
	assert.True(t, Form{Name: "lodash"}.searched(Vulnerabilities))
	assert.False(t, Form{Ecosystem: "npm", Direct: true}.searched(Dependencies))
	assert.True(t, Form{Name: "lodash"}.searched(Dependencies))
	assert.True(t, Form{Version: "1.0.0"}.searched(Dependencies), "so that a missing package is told")
}

func TestFormQuery(t *testing.T) {
	assert.Empty(t, Form{}.Query())
	q := Form{Advisory: "CVE-2024-1", Ecosystem: "npm", Name: "lodash", Version: "^4", State: "all", Project: "p", Owner: "o", Direct: true}.Query()
	assert.Equal(t, "advisory=CVE-2024-1&direct=true&ecosystem=npm&name=lodash&owner=o&project=p&state=all&version=%5E4", q.Encode())
	assert.Equal(t, optional.Some(true), Form{Direct: true}.usage().Direct)
	assert.False(t, Form{}.usage().Direct.Has())
}
