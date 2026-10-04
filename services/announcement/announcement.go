// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package announcement

import (
	"context"
	"html/template"

	"forgejo.org/models/db"
	system_model "forgejo.org/models/system"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/cache"
	"forgejo.org/modules/json"
	"forgejo.org/modules/markup"
	"forgejo.org/modules/markup/markdown"
	"forgejo.org/modules/timeutil"
)

const cacheKey = "announcements"

// Banner is an announcement with its rendered message, ready to be shown
type Banner struct {
	*system_model.Announcement
	RenderedMessage template.HTML
}

// RenderMessage renders the Markdown message of an announcement
func RenderMessage(ctx context.Context, message string) (template.HTML, error) {
	return markdown.RenderString(&markup.RenderContext{Ctx: ctx}, message)
}

// loadAll returns all announcements with their rendered message, from the cache when possible
func loadAll(ctx context.Context) ([]*Banner, error) {
	cached, err := cache.GetString(cacheKey, func() (string, error) {
		list, _, err := system_model.FindAnnouncements(ctx, db.ListOptions{})
		if err != nil {
			return "", err
		}
		banners := make([]*Banner, 0, len(list))
		for _, a := range list {
			rendered, err := RenderMessage(ctx, a.Message)
			if err != nil {
				return "", err
			}
			banners = append(banners, &Banner{Announcement: a, RenderedMessage: rendered})
		}
		data, err := json.Marshal(banners)
		return string(data), err
	})
	if err != nil {
		return nil, err
	}
	var banners []*Banner
	if err := json.Unmarshal([]byte(cached), &banners); err != nil {
		return nil, err
	}
	return banners, nil
}

// ActiveForUser returns the announcements to show to the given user (nil for
// anonymous visitors) right now, most severe first, leaving out the ones they dismissed
func ActiveForUser(ctx context.Context, doer *user_model.User) ([]*Banner, error) {
	all, err := loadAll(ctx)
	if err != nil {
		return nil, err
	}

	now := timeutil.TimeStampNow()
	isSigned := doer != nil
	isAdmin := isSigned && doer.IsAdmin
	active := make([]*system_model.Announcement, 0, len(all))
	byID := make(map[int64]*Banner, len(all))
	for _, b := range all {
		if b.IsActiveAt(now) && b.IsVisibleTo(isSigned, isAdmin) {
			active = append(active, b.Announcement)
			byID[b.ID] = b
		}
	}
	if len(active) == 0 {
		return nil, nil
	}

	if isSigned {
		if active, err = system_model.FilterDismissedAnnouncements(ctx, active, doer.ID); err != nil {
			return nil, err
		}
	}
	system_model.SortAnnouncements(active)

	banners := make([]*Banner, 0, len(active))
	for _, a := range active {
		banners = append(banners, byID[a.ID])
	}
	return banners, nil
}

// Create creates an announcement
func Create(ctx context.Context, a *system_model.Announcement) error {
	defer cache.Remove(cacheKey)
	return system_model.InsertAnnouncement(ctx, a)
}

// Update saves the changes to an announcement
func Update(ctx context.Context, a *system_model.Announcement) error {
	defer cache.Remove(cacheKey)
	return system_model.UpdateAnnouncement(ctx, a)
}

// Delete deletes an announcement
func Delete(ctx context.Context, id int64) error {
	defer cache.Remove(cacheKey)
	return system_model.DeleteAnnouncement(ctx, id)
}
