// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package webhook_test

import (
	"testing"

	"forgejo.org/models/unittest"

	_ "forgejo.org/models/repo" // the audit log reads a hook's scope from their tables
	_ "forgejo.org/models/user"
)

func TestMain(m *testing.M) {
	unittest.MainTest(m, &unittest.TestOptions{
		FixtureFiles: []string{
			"webhook.yml",
			"hook_task.yml",
		},
	})
}
