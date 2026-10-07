// Copyright The Forgejo Authors.
// SPDX-License-Identifier: MIT

package setting

import (
	"testing"

	"forgejo.org/modules/container"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_loadAdminFrom(t *testing.T) {
	iniStr := `
	[admin]
	DISABLE_REGULAR_ORG_CREATION = true
  DEFAULT_EMAIL_NOTIFICATIONS = z
  SEND_NOTIFICATION_EMAIL_ON_NEW_USER = true
  USER_DISABLED_FEATURES = a,b
  EXTERNAL_USER_DISABLE_FEATURES = x,y
	`
	cfg, err := NewConfigProviderFromData(iniStr)
	require.NoError(t, err)
	loadAdminFrom(cfg)

	assert.True(t, Admin.DisableRegularOrgCreation)
	assert.Equal(t, "z", Admin.DefaultEmailNotification)
	assert.True(t, Admin.SendNotificationEmailOnNewUser)
	assert.Equal(t, container.SetOf("a", "b"), Admin.UserDisabledFeatures)
	assert.Equal(t, container.SetOf("x", "y"), Admin.ExternalUserDisableFeatures)
}

// AC9: DEFAULT_EMAIL_NOTIFICATION_EVENTS is the instance default event list for Custom.
func Test_loadAdminDefaultEmailEvents(t *testing.T) {
	cfg, err := NewConfigProviderFromData("[admin]\nDEFAULT_EMAIL_NOTIFICATIONS = custom\nDEFAULT_EMAIL_NOTIFICATION_EVENTS = pr_approved, mention\n")
	require.NoError(t, err)
	loadAdminFrom(cfg)
	assert.Equal(t, "custom", Admin.DefaultEmailNotification)
	assert.Equal(t, []string{"pr_approved", "mention"}, Admin.DefaultEmailNotificationEvents)

	cfg, err = NewConfigProviderFromData("[admin]\n")
	require.NoError(t, err)
	loadAdminFrom(cfg)
	assert.Empty(t, Admin.DefaultEmailNotificationEvents)
}
