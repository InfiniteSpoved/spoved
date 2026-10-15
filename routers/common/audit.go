// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package common

import (
	"net/http"
	"strconv"
	"time"

	audit_model "forgejo.org/models/audit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/httplib"
	"forgejo.org/modules/log"
	"forgejo.org/modules/util"
	audit_service "forgejo.org/services/audit"
	auth_service "forgejo.org/services/auth"
	"forgejo.org/services/context"
)

// SetAuditActor stores the request's audit actor right after authentication. It is stored once per request; API sudo
// changes it in place (Actor.SetSudo).
func SetAuditActor(ctx *context.Base, ar auth_service.AuthenticationResult) {
	a := &audit_model.Actor{
		Type:       audit_model.ActorAnonymous,
		Credential: ar.Credential(),
		UserAgent:  ctx.Req.UserAgent(),
		RequestID:  util.CryptoRandomString(util.RandomStringMedium), // never the client's X-Request-ID
	}
	if u := ar.User(); u != nil {
		a.Type, a.ID, a.Name = audit_model.ActorUser, u.ID, u.Name
		if u.ID == user_model.ActionsUserID {
			a.Type = audit_model.ActorActions
		}
	}
	if peer, candidates, ok := httplib.AmbiguousClientIP(ctx.Req); ok {
		a.IP, a.IPAmbiguous, a.IPCandidates = peer, true, candidates
	} else {
		a.IP = auditPeer(ctx.Req.RemoteAddr, ClientIP(ctx.Req))
	}
	ctx.AppendContextValue(audit_model.ActorContextKey, a)
}

// UsableAuditReader reports whether the doer may read the audit log: a site admin or an auditor whose account can
// sign in. The callers check how the request was authenticated.
func UsableAuditReader(doer *user_model.User) bool {
	return doer != nil && doer.IsActive && !doer.ProhibitLogin && (doer.IsAdmin || doer.IsAuditor)
}

// ExportAudit streams the audit export of f as "csv" or "json" with its manifest headers; link is the URL path the
// continue link of a truncated export points to.
func ExportAudit(ctx *context.Base, f *audit_service.Filter, format, link string) {
	_, err := audit_service.Export(ctx, ctx.Resp, f, format, audit_service.ExportCap, func(m *audit_service.Manifest) error {
		contentType := "text/csv"
		if format == "json" {
			contentType = "application/json"
		}
		ctx.SetServeHeaders(&context.ServeHeaderOptions{
			ContentType: contentType, ContentTypeCharset: "utf-8",
			Filename: "audit-log-" + m.GeneratedAt.Format("20060102T150405Z") + "." + format,
		})
		h := ctx.Resp.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Audit-Filter", m.FilterHeader())
		h.Set("X-Audit-Generated-At", m.GeneratedAt.Format(time.RFC3339))
		h.Set("X-Audit-Truncated", strconv.FormatBool(m.Truncated))
		if m.Truncated {
			h.Set("X-Audit-Exported-Through", m.Through.Format(time.RFC3339))
			h.Set("Link", "<"+link+"?"+m.Next(f).Encode()+">; rel=\"next\"")
		}
		return nil
	})
	if err == nil {
		return
	}
	if ctx.Resp.WrittenStatus() == 0 {
		ctx.Resp.Header().Del("Content-Disposition")
		log.Error("ExportAudit: %v", err)
		http.Error(ctx.Resp, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	log.Error("ExportAudit: %v", err)
	panic(http.ErrAbortHandler) // abort so the client sees a failed download, not a short file
}
