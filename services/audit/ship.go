// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package audit

import (
	audit_model "forgejo.org/models/audit"
	"forgejo.org/modules/log"
)

// shipEntries writes the entries a sealer just committed to the "audit" logger, one JSON event each, in seq order.
// It is best effort: the database is the source of truth, and a full writer queue only drops the event. The message
// is the action key; `data` goes out as one string, so it is redacted and capped like every other field. Without a
// writer nothing is built.
func shipEntries(rows []*audit_model.Entry) {
	l := log.GetManager().GetLogger("audit")
	if !l.IsEnabled() {
		return
	}
	for _, e := range rows {
		level := log.INFO
		if e.Result != audit_model.ResultSuccess {
			level = log.WARN
		}
		l.LogWithFields(0, level, map[string]any{
			"action": e.Action, "category": e.Category, "result": e.Result, "occurred_at": int64(e.CreatedUnix),
			"ip": e.IP, "user_agent": e.UserAgent, "request_id": e.RequestID,
			"actor_type": e.ActorType, "actor_id": e.ActorID, "actor_name": e.ActorName, "sudo_by_id": e.SudoByID,
			"credential_type": e.CredentialType, "credential_id": e.CredentialID, "credential_name": e.CredentialName, "credential_owner_id": e.CredentialOwnerID,
			"target_type": e.TargetType, "target_id": e.TargetID, "target_name": e.TargetName,
			"owner_id": e.OwnerID, "group_id": e.GroupID, "repo_id": e.RepoID, "subject_user_id": e.SubjectUserID,
			"seq": *e.Seq, "hash": *e.Hash, "data": e.Data,
		}, "%s", e.Action)
	}
}
