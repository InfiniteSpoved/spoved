// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package admin

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAuditLocation(t *testing.T) {
	assert.Equal(t, time.UTC, auditLocation(""))
	assert.Equal(t, time.UTC, auditLocation("No/Such_Zone"), "a zone tzdata lacks falls back")
	assert.Equal(t, "Europe/Berlin", auditLocation("Europe/Berlin").String())
}
