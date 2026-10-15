// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package cron

import (
	"context"
	"testing"

	audit_model "forgejo.org/models/audit"
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

func TestCronAuditActor(t *testing.T) {
	var got *audit_model.Actor
	task := &Task{Name: "actor test", config: &BaseConfig{}, fun: func(ctx context.Context, _ *user_model.User, _ Config) error {
		got = audit_model.ActorFromContext(ctx)
		return nil
	}}
	defer test.MockVariableValue(&setting.Maintenance.Mode, setting.MaintenanceOff)()

	task.Run()
	assert.Equal(t, audit_model.SystemActor("cron:actor test"), got)

	admin := &audit_model.Actor{Type: audit_model.ActorUser, ID: 1, Name: "user1", IP: "192.0.2.1"}
	task.RunWithUser(audit_model.WithActor(t.Context(), admin), &user_model.User{ID: 1, Name: "user1"}, nil)
	assert.Equal(t, &audit_model.Actor{Type: audit_model.ActorUser, ID: 1, Name: "user1", IP: "192.0.2.1", Task: "actor test"}, got)
	assert.Empty(t, admin.Task, "the request's actor is not changed")
}
