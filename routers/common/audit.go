// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package common

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	audit_model "forgejo.org/models/audit"
	auth_model "forgejo.org/models/auth"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/httplib"
	"forgejo.org/modules/log"
	"forgejo.org/modules/ratelimit"
	"forgejo.org/modules/util"
	audit_service "forgejo.org/services/audit"
	auth_service "forgejo.org/services/auth"
	"forgejo.org/services/auth/source/db"
	"forgejo.org/services/authz"
	"forgejo.org/services/context"
)

// SetAuditActor stores the request's audit actor right after authentication. It is stored once per request; API sudo
// changes it in place (Actor.SetSudo).
func SetAuditActor(ctx *context.Base, ar auth_service.AuthenticationResult) {
	a := RequestAuditActor(ctx.Req)
	a.Credential = ar.Credential()
	if u := ar.User(); u != nil {
		a.Type, a.ID, a.Name = audit_model.ActorUser, u.ID, u.Name
		if u.ID == user_model.ActionsUserID {
			a.Type = audit_model.ActorActions
		}
	}
	ctx.AppendContextValue(audit_model.ActorContextKey, a)
}

// RequestAuditActor is the anonymous actor of req: its client IP, user agent and a new request ID. req must have
// passed ProtocolMiddlewares, so that the client IP comes from trusted proxies only.
func RequestAuditActor(req *http.Request) *audit_model.Actor {
	a := &audit_model.Actor{
		Type:      audit_model.ActorAnonymous,
		UserAgent: audit_model.Text(req.UserAgent(), audit_model.MaxUserAgent), // capped here: actors are copied into aggregates
		RequestID: util.CryptoRandomString(util.RandomStringMedium),            // never the client's X-Request-ID
	}
	if peer, candidates, ok := httplib.AmbiguousClientIP(req); ok {
		a.IP, a.IPAmbiguous, a.IPCandidates = peer, true, slices.Clone(candidates[:min(len(candidates), audit_model.MaxIPCandidates)]) // never the parsed header's array
	} else {
		a.IP = auditPeer(req.RemoteAddr, ClientIP(req))
	}
	return a
}

// WithRequestAuditActor stores the request's anonymous actor for handlers that authenticate by themselves, such as
// the runner RPC, which fill in who it is once they know.
func WithRequestAuditActor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		next.ServeHTTP(w, req.WithContext(audit_model.WithActor(req.Context(), RequestAuditActor(req))))
	})
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

// RecordAuthFailure records a request whose credential was refused, with an anonymous actor: an expired access token
// as expired, a token refused by the scope policy as a refusal, anything else as a failed sign-in. A request the auth
// rate limit refused is recorded by WriteIfRateLimited instead.
func RecordAuthFailure(b *context.Base, v *auth_service.AuthenticationAttemptedIncorrectCredential) {
	SetAuditActor(b, &auth_service.UnauthenticatedResult{})
	var limited *ratelimit.LimitedError
	var expired auth_model.ErrAccessTokenExpired
	subject := v.UserID
	switch {
	case errors.As(v.Error, &limited):
	case errors.As(v.Error, &expired):
		auth_service.RecordExpired(b, audit_model.CredentialAccessToken, expired.ID, expired.UID, expired.Name, expired.Expires)
	case errors.Is(v.Error, authz.ErrScopePolicyEnforced):
		auth_service.RecordRefusal(b, "scope_policy", 0, subject, audit_model.IPKey(audit_model.ActorFromContext(b), 48)+"|"+strconv.FormatInt(subject, 10), nil)
	default:
		if badPassword, ok := errors.AsType[db.ErrUserPasswordInvalid](v.Error); ok {
			subject = badPassword.UID
		} else if prohibited, ok := errors.AsType[user_model.ErrUserProhibitLogin](v.Error); ok {
			subject = prohibited.UID
		}
		auth_service.RecordSignInFailure(b, subject, "bad_credential")
	}
}

// CredentialUsed records the first use of the request's credential from its client IP per day; it runs after the
// rate limit and the IP allowlist, so a refused request records nothing here.
func CredentialUsed(method string) func(*context.Context) {
	return func(ctx *context.Context) {
		audit_model.RecordCredentialUsed(ctx, method)
	}
}

// CredentialUsedAPI is CredentialUsed for the API, before sudo changes the actor.
func CredentialUsedAPI(ctx *context.APIContext) {
	audit_model.RecordCredentialUsed(ctx, "api")
}
