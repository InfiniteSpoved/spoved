// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package cron

import (
	"context"
	"testing"

	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"

	"github.com/stretchr/testify/assert"
)

func TestCronPausedInMaintenance(t *testing.T) {
	runs := 0
	task := &Task{Name: "maintenance test", config: &BaseConfig{}, fun: func(context.Context, *user_model.User, Config) error {
		runs++
		return nil
	}}

	defer test.MockVariableValue(&setting.Maintenance.Mode, setting.MaintenanceReadOnly)()
	task.Run()
	assert.Equal(t, 0, runs)

	setting.Maintenance.Mode = setting.MaintenanceOff
	task.Run()
	assert.Equal(t, 1, runs)
}
