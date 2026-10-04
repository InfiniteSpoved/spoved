// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package user

import (
	goctx "context"
	"errors"
	"net/http"
	"strings"

	system_model "forgejo.org/models/system"
	"forgejo.org/modules/log"
	announcement_service "forgejo.org/services/announcement"
	"forgejo.org/services/context"
)

// GetAnnouncements is the middleware that makes the active announcements available to the page templates
func GetAnnouncements(ctx *context.Context) {
	if strings.HasPrefix(ctx.Req.URL.Path, "/api") {
		return
	}

	ctx.Data["SiteAnnouncements"] = func() []*announcement_service.Banner {
		banners, err := announcement_service.ActiveForUser(ctx, ctx.Doer)
		if err != nil {
			if !errors.Is(err, goctx.Canceled) {
				log.Error("Unable to load announcements: %v", err)
			}
			return nil
		}
		return banners
	}
}

// DismissAnnouncement hides an announcement for the signed-in user until its message changes
func DismissAnnouncement(ctx *context.Context) {
	a, err := system_model.GetAnnouncementByID(ctx, ctx.ParamsInt64(":id"))
	if err != nil {
		ctx.NotFoundOrServerError("GetAnnouncementByID", system_model.IsErrAnnouncementNotExist, err)
		return
	}
	if !a.CanBeDismissed() {
		ctx.Error(http.StatusUnprocessableEntity, "announcement cannot be dismissed")
		return
	}
	if err := system_model.DismissAnnouncement(ctx, a, ctx.Doer.ID); err != nil {
		ctx.ServerError("DismissAnnouncement", err)
		return
	}
	ctx.RedirectToFirst(ctx.FormString("redirect_to"))
}
