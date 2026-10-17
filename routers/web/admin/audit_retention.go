// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package admin

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"forgejo.org/modules/base"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/util"
	audit_service "forgejo.org/services/audit"
	"forgejo.org/services/context"
)

const tplAuditRetention base.TplName = "admin/audit_retention"

// auditRetentionLine is "Retention: 1 year" and, while a lowering waits, when it takes effect.
func auditRetentionLine(ctx *context.Context) string {
	s, now := setting.Config().AuditRetention.Value(ctx), time.Now()
	line := ctx.Locale.TrString("admin.audit.retention", ctx.Locale.TrString("admin.audit_retention.option."+strconv.Itoa(s.Effective(now))))
	if s.Pending(now) {
		line += " · " + ctx.Locale.TrString("admin.audit_retention.pending",
			ctx.Locale.TrString("admin.audit_retention.option."+strconv.Itoa(s.Target())), s.PendingUntil(now).UTC().Format("2006-01-02 15:04 UTC"))
	}
	return line
}

// AuditRetention shows the retention of the audit log and the form to change it.
func AuditRetention(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("admin.audit_retention")
	ctx.Data["PageIsAdminAudit"] = true
	ctx.Data["RetentionLine"] = auditRetentionLine(ctx)
	ctx.Data["Options"] = setting.AuditRetentionAllowed
	ctx.Data["Current"] = setting.Config().AuditRetention.Value(ctx).Effective(time.Now())
	ctx.HTML(http.StatusOK, tplAuditRetention)
}

// AuditRetentionPost stores the chosen retention; only the number of days comes from the form.
func AuditRetentionPost(ctx *context.Context) {
	days, err := strconv.Atoi(ctx.FormString("days"))
	if err == nil {
		err = audit_service.SetRetention(ctx, days)
	}
	if errors.Is(err, util.ErrInvalidArgument) || errors.Is(err, strconv.ErrSyntax) || errors.Is(err, strconv.ErrRange) {
		ctx.Error(http.StatusBadRequest, "invalid retention")
		return
	}
	if err != nil {
		ctx.ServerError("SetRetention", err)
		return
	}
	ctx.Flash.Success(ctx.Tr("admin.audit_retention.saved"))
	ctx.Redirect(setting.AppSubURL + "/admin/audit/retention")
}
