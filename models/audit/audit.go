// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// Package audit stores the audit log. Record is the one way to write an entry.
package audit

import (
	"context"
	"fmt"
	"strings"

	"forgejo.org/models/db"
	"forgejo.org/modules/json"
	"forgejo.org/modules/log"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"
)

// Entry is one row of the audit log. The table has no foreign keys and nothing that deletes a user, organization or
// repository touches it: entries outlive what they name until retention removes them.
type Entry struct {
	CreatedUnix       timeutil.TimeStamp `xorm:"INDEX NOT NULL"`
	Action            string             `xorm:"VARCHAR(64) NOT NULL index(action_id)"`
	Category          string             `xorm:"VARCHAR(16) NOT NULL index(category_id)"`
	Result            string             `xorm:"VARCHAR(16) NOT NULL"`
	ActorType         string             `xorm:"VARCHAR(16) NOT NULL"`
	ActorID           int64              `xorm:"NOT NULL DEFAULT 0 index(actor_id)"`
	ActorName         string             `xorm:"VARCHAR(255)"`
	SudoByID          int64              `xorm:"NOT NULL DEFAULT 0 index(sudo_by_id)"`
	CredentialType    string             `xorm:"VARCHAR(32) index(credential_id)"`
	CredentialID      int64              `xorm:"NOT NULL DEFAULT 0 index(credential_id)"`
	CredentialName    string             `xorm:"VARCHAR(255)"`
	CredentialOwnerID int64              `xorm:"NOT NULL DEFAULT 0 index(credential_owner_id)"`
	IP                string             `xorm:"VARCHAR(64) index(ip_id)"`
	UserAgent         string             `xorm:"VARCHAR(256)"`
	RequestID         string             `xorm:"VARCHAR(32) INDEX"`
	OwnerID           int64              `xorm:"NOT NULL DEFAULT 0 index(owner_id)"`
	GroupID           int64              `xorm:"NOT NULL DEFAULT 0 index(group_id)"`
	RepoID            int64              `xorm:"NOT NULL DEFAULT 0 index(repo_id)"`
	SubjectUserID     int64              `xorm:"NOT NULL DEFAULT 0 index(subject_user_id)"`
	TargetType        string             `xorm:"VARCHAR(32) index(target)"`
	TargetID          int64              `xorm:"NOT NULL DEFAULT 0 index(target)"`
	TargetName        string             `xorm:"VARCHAR(255)"`
	Data              string             `xorm:"LONGTEXT"`
	Seq               *int64             `xorm:"UNIQUE NULL"`
	PrevHash          *string            `xorm:"CHAR(64) NULL"`
	Hash              *string            `xorm:"CHAR(64) NULL"`
	// ID is last so that it is the last column of the (x, id) indexes above.
	ID int64 `xorm:"pk autoincr index(action_id) index(category_id) index(actor_id) index(sudo_by_id) index(credential_id) index(credential_owner_id) index(ip_id) index(owner_id) index(group_id) index(repo_id) index(subject_user_id)"`
}

func (Entry) TableName() string { return "audit_event" }

// ChainHead is the single row that the sealer moves forward.
type ChainHead struct {
	ID             int64  `xorm:"pk autoincr"`
	Seq            int64  `xorm:"NOT NULL DEFAULT 0"`
	Hash           string `xorm:"CHAR(64) NOT NULL"`
	AnchorSeq      int64  `xorm:"NOT NULL DEFAULT 0"`
	AnchorPrevHash string `xorm:"CHAR(64) NOT NULL"`
}

func (ChainHead) TableName() string { return "audit_chain_head" }

func init() {
	db.RegisterModel(new(Entry))
	db.RegisterModel(new(ChainHead))
}

const (
	CategoryAuth       = "auth"
	CategoryCredential = "credential"
	CategoryAudit      = "audit"

	ResultSuccess = "success"
	ResultFailure = "failure"
	ResultRefused = "refused"

	maxText     = 255
	maxData     = 32 * 1024
	maxUA       = 256
	maxIP       = 64
	maxListText = 255
)

// Event is what a caller records. Its category comes from Actions. Data holds only keys listed in Fields[TargetType] (and inside "before"/"after");
// everything else is dropped.
type Event struct {
	Action, Result           string
	OwnerID, GroupID, RepoID int64
	SubjectUserID            int64
	TargetType               string
	TargetID                 int64
	TargetName               string
	Data                     map[string]any
}

// Fields lists the data keys kept per target type. Secrets (token values, hashes, salts, last eight) are never listed.
var Fields = map[string][]string{
	"access_token":       {"name", "scopes", "expires", "resource_all_repos", "repo_ids"},
	"audit_export":       {"format", "filter", "capped", "through_id"},
	"credential_use":     {"method"},
	"deploy_key":         {"expires"},
	"instance":           {"version", "config_sha256"},
	"oauth2_app":         {"secret_expires"},
	"org_deploy_key":     {"expires"},
	"project_deploy_key": {"expires"},
	"refusal":            {"reason", "class", "method"},
	"signin":             {"method", "reason"},
	"ssh_key":            {"expires"},
}

// sharedKeys are the top-level data keys every target type may carry.
var sharedKeys = []string{
	"before", "after", "on_behalf", "reason", "truncated", "ip_ambiguous", "ip_candidates",
	"count", "first", "last", "overflow", // aggregated entries
}

// InsertEntry writes the row. It is a variable so that tests can make the insert fail.
var InsertEntry = func(ctx context.Context, e *Entry) error {
	return db.Insert(ctx, e)
}

// Record writes one entry with the actor of ctx, in ctx's transaction if it has one. An error means the entry was not
// written: callers inside a transaction return it, so the change fails with it.
func Record(ctx context.Context, ev Event) error {
	category, ok := Actions[ev.Action]
	if !ok {
		return fmt.Errorf("audit: unknown action %q", ev.Action)
	}
	a := ActorFromContext(ctx)
	if a == nil {
		log.Error("audit: no actor in context for %s", ev.Action)
		a = &Actor{Type: ActorUnknown}
	}
	data := filterData(ev.TargetType, ev.Data)
	if a.IPAmbiguous {
		data["ip_ambiguous"] = true
		if len(a.IPCandidates) > 0 {
			data["ip_candidates"] = a.IPCandidates
		}
	}
	if a.Task != "" {
		data["task"] = Text(a.Task, maxText)
	}
	cred := a.Credential
	if namer := credentialNamers[cred.Type]; cred.Name == "" && namer != nil {
		name, err := namer(ctx, cred)
		if err != nil {
			return err
		}
		cred.Name = name
	}
	encoded, err := encodeData(data)
	if err != nil {
		return err
	}
	return InsertEntry(ctx, &Entry{
		CreatedUnix:       timeutil.TimeStampNow(),
		Action:            ev.Action,
		Category:          category,
		Result:            ev.Result,
		ActorType:         a.Type,
		ActorID:           a.ID,
		ActorName:         Text(a.Name, maxText),
		SudoByID:          a.SudoByID,
		CredentialType:    cred.Type,
		CredentialID:      cred.ID,
		CredentialName:    Text(cred.Name, maxText),
		CredentialOwnerID: cred.OwnerID,
		IP:                Text(a.IP, maxIP),
		UserAgent:         Text(a.UserAgent, maxUA),
		RequestID:         Text(a.RequestID, 32),
		OwnerID:           ev.OwnerID,
		GroupID:           ev.GroupID,
		RepoID:            ev.RepoID,
		SubjectUserID:     ev.SubjectUserID,
		TargetType:        ev.TargetType,
		TargetID:          ev.TargetID,
		TargetName:        Text(ev.TargetName, maxText),
		Data:              encoded,
	})
}

// Text strips control characters and cuts s to at most n bytes at a rune boundary.
func Text(s string, n int) string {
	s, _ = util.SplitStringAtByteN(strings.ToValidUTF8(util.StripControl(s), "?"), n)
	return s
}

func filterData(targetType string, in map[string]any) map[string]any {
	allowed := Fields[targetType]
	out := filterKeys(in, append(allowed, sharedKeys...))
	for _, k := range []string{"before", "after"} {
		if m, ok := in[k].(map[string]any); ok {
			out[k] = filterKeys(m, allowed)
		} else {
			delete(out, k)
		}
	}
	return out
}

func filterKeys(in map[string]any, allowed []string) map[string]any {
	out := map[string]any{}
	for _, k := range allowed {
		v, ok := in[k]
		if !ok {
			continue
		}
		switch v := v.(type) {
		case string:
			out[k] = Text(v, maxText)
		case []string:
			list := make([]string, len(v))
			for i, s := range v {
				list[i] = Text(s, maxListText)
			}
			out[k] = list
		default:
			out[k] = v
		}
	}
	return out
}

// encodeData returns the JSON of data (keys sorted) cut to maxData: it halves the longest name list first, then the
// longest ID list, and sets "truncated".
func encodeData(data map[string]any) (string, error) {
	for {
		b, err := json.Marshal(data)
		if err != nil || len(b) <= maxData {
			return string(b), err
		}
		if !halveLongest[string](data) && !halveLongest[int64](data) {
			return "", util.NewInvalidArgumentErrorf("audit data too large")
		}
		data["truncated"] = true
	}
}

func halveLongest[T any](data map[string]any) bool {
	var longest []T
	var set func([]T)
	visit := func(m map[string]any) {
		for k, v := range m {
			if l, ok := v.([]T); ok && len(l) > len(longest) {
				longest, set = l, func(n []T) { m[k] = n }
			}
		}
	}
	visit(data)
	for _, k := range []string{"before", "after"} {
		if m, ok := data[k].(map[string]any); ok {
			visit(m)
		}
	}
	if len(longest) == 0 {
		return false
	}
	set(longest[:len(longest)/2])
	return true
}
