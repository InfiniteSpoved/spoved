// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package common

import (
	audit_model "forgejo.org/models/audit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/httplib"
	"forgejo.org/modules/util"
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
