// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"testing"

	"forgejo.org/models/db"
	system_model "forgejo.org/models/system"
	"forgejo.org/modules/setting/config"

	"github.com/stretchr/testify/require"
)

// lastSettingsRevision: fixtures reset the settings revision, so two tests could store different values under the
// same revision and the cached one would win; keep the revision above every one seen so far.
var lastSettingsRevision int

// bumpSettingsRevision must follow every test write to a system setting (including a web or API request that
// stores one) before the setting is read again.
func bumpSettingsRevision(t *testing.T) {
	t.Helper()
	for system_model.GetRevision(db.DefaultContext) <= lastSettingsRevision {
		require.NoError(t, system_model.SetSettings(db.DefaultContext, nil))
	}
	lastSettingsRevision = system_model.GetRevision(db.DefaultContext)
	config.GetDynGetter().InvalidateCache()
}
