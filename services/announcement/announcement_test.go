// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package announcement

import (
	"testing"
	"time"

	system_model "forgejo.org/models/system"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/cache"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"
	"forgejo.org/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func messages(banners []*Banner) []string {
	result := make([]string, 0, len(banners))
	for _, b := range banners {
		result = append(result, string(b.RenderedMessage))
	}
	return result
}

func TestActiveForUser(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	defer test.MockVariableValue(&setting.CacheService.Cache, setting.Cache{Adapter: "memory", TTL: time.Hour})()
	require.NoError(t, cache.Init())
	cache.Remove(cacheKey)
	ctx := t.Context()

	admin := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

	banners, err := ActiveForUser(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, banners)

	now := timeutil.TimeStampNow()
	info := &system_model.Announcement{Message: "info *one*", Type: system_model.AnnouncementInfo, Audience: system_model.AnnouncementAudienceEveryone, Dismissible: true}
	critical := &system_model.Announcement{Message: "critical", Type: system_model.AnnouncementCritical, Audience: system_model.AnnouncementAudienceAdmins}
	scheduled := &system_model.Announcement{Message: "later", Type: system_model.AnnouncementWarning, Audience: system_model.AnnouncementAudienceEveryone, StartUnix: now + 3600}
	for _, a := range []*system_model.Announcement{info, critical, scheduled} {
		require.NoError(t, Create(ctx, a))
	}

	// creating invalidates the cache
	banners, err = ActiveForUser(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"<p>info <em>one</em></p>\n"}, messages(banners))

	banners, err = ActiveForUser(ctx, admin)
	require.NoError(t, err)
	assert.Equal(t, []string{"<p>critical</p>\n", "<p>info <em>one</em></p>\n"}, messages(banners))

	// changes made without the service are not seen until the cache is invalidated
	info.Message = "info two"
	require.NoError(t, system_model.UpdateAnnouncement(ctx, info))
	banners, err = ActiveForUser(ctx, user)
	require.NoError(t, err)
	assert.Equal(t, []string{"<p>info <em>one</em></p>\n"}, messages(banners))

	require.NoError(t, Update(ctx, info))
	banners, err = ActiveForUser(ctx, user)
	require.NoError(t, err)
	assert.Equal(t, []string{"<p>info two</p>\n"}, messages(banners))

	// dismissals are applied on top of the cached list
	require.NoError(t, system_model.DismissAnnouncement(ctx, info, user.ID))
	banners, err = ActiveForUser(ctx, user)
	require.NoError(t, err)
	assert.Empty(t, banners)
	banners, err = ActiveForUser(ctx, nil)
	require.NoError(t, err)
	assert.Len(t, banners, 1)

	require.NoError(t, Delete(ctx, info.ID))
	banners, err = ActiveForUser(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, banners)
}
