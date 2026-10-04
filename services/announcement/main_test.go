// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package announcement

import (
	"testing"

	"forgejo.org/models/unittest"

	_ "forgejo.org/models"
)

func TestMain(m *testing.M) {
	unittest.MainTest(m)
}
