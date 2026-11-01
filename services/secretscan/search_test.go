// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretscan

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseFilter(t *testing.T) {
	for _, c := range []struct {
		state, reason, typ string
		want               Filter
		ignored            bool
	}{
		{"", "", "", Filter{}, false},
		{"open", "", "github-token", Filter{State: "open", Type: "github-token"}, false},
		{"closed", "wont_fix", "", Filter{State: "closed", Reason: "wont_fix"}, false},
		{"", "revoked", "", Filter{Reason: "revoked"}, false}, // a reason alone lists closed alerts
		{"open", "revoked", "", Filter{State: "open"}, true},  // no closed alert is open
		{"bogus", "", "", Filter{}, true},
		{"closed", "bogus", "", Filter{State: "closed"}, true},
		{"", "", strings.Repeat("a", 65), Filter{}, true},
	} {
		got, ignored := ParseFilter(c.state, c.reason, c.typ)
		assert.Equal(t, c.want, got, "%+v", c)
		assert.Equal(t, c.ignored, ignored, "%+v", c)
	}
}
