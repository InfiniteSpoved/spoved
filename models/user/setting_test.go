// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package user_test

import (
	"strings"
	"testing"

	"forgejo.org/models/db"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSettings(t *testing.T) {
	keyName := "test_user_setting"
	require.NoError(t, unittest.PrepareTestDatabase())

	newSetting := &user_model.Setting{UserID: 99, SettingKey: keyName, SettingValue: "Gitea User Setting Test"}

	// create setting
	err := user_model.SetUserSetting(db.DefaultContext, newSetting.UserID, newSetting.SettingKey, newSetting.SettingValue)
	require.NoError(t, err)
	// test about saving unchanged values
	err = user_model.SetUserSetting(db.DefaultContext, newSetting.UserID, newSetting.SettingKey, newSetting.SettingValue)
	require.NoError(t, err)

	// get specific setting
	settings, err := user_model.GetSettings(db.DefaultContext, 99, []string{keyName})
	require.NoError(t, err)
	assert.Len(t, settings, 1)
	assert.Equal(t, newSetting.SettingValue, settings[keyName].SettingValue)

	settingValue, err := user_model.GetUserSetting(db.DefaultContext, 99, keyName)
	require.NoError(t, err)
	assert.Equal(t, newSetting.SettingValue, settingValue)

	settingValue, err = user_model.GetUserSetting(db.DefaultContext, 99, "no_such")
	require.NoError(t, err)
	assert.Empty(t, settingValue)

	// updated setting
	updatedSetting := &user_model.Setting{UserID: 99, SettingKey: keyName, SettingValue: "Updated"}
	err = user_model.SetUserSetting(db.DefaultContext, updatedSetting.UserID, updatedSetting.SettingKey, updatedSetting.SettingValue)
	require.NoError(t, err)

	// get all settings
	settings, err = user_model.GetUserAllSettings(db.DefaultContext, 99)
	require.NoError(t, err)
	assert.Len(t, settings, 1)
	assert.Equal(t, updatedSetting.SettingValue, settings[updatedSetting.SettingKey].SettingValue)

	// delete setting
	err = user_model.DeleteUserSetting(db.DefaultContext, 99, keyName)
	require.NoError(t, err)
	settings, err = user_model.GetUserAllSettings(db.DefaultContext, 99)
	require.NoError(t, err)
	assert.Empty(t, settings)
}

func TestTimeZoneSetting(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	for name, valid := range map[string]bool{
		"Europe/Paris": true, "UTC": true, "Asia/Kolkata": true,
		"": false, "Local": false, "Not/AZone": false, "../etc/passwd": false, strings.Repeat("a", 65): false,
	} {
		assert.Equal(t, valid, user_model.IsValidTimeZone(name), name)
	}

	zone, err := user_model.GetUserTimeZone(t.Context(), 2)
	require.NoError(t, err)
	assert.Empty(t, zone)

	require.NoError(t, user_model.SetUserSetting(t.Context(), 2, user_model.SettingsKeyTimeZone, "Europe/Paris"))
	zone, err = user_model.GetUserTimeZone(t.Context(), 2)
	require.NoError(t, err)
	assert.Equal(t, "Europe/Paris", zone)

	// a stored value that is no longer valid reads as not set
	require.NoError(t, user_model.SetUserSetting(t.Context(), 2, user_model.SettingsKeyTimeZone, "Gone/Zone"))
	zone, err = user_model.GetUserTimeZone(t.Context(), 2)
	require.NoError(t, err)
	assert.Empty(t, zone)
}
