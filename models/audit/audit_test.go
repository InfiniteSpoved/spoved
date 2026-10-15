// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package audit_test

import (
	"strings"
	"testing"

	audit_model "forgejo.org/models/audit"
	"forgejo.org/models/db"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/json"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	unittest.MainTest(m)
}

func recordOne(t *testing.T, ev audit_model.Event) *audit_model.Entry {
	t.Helper()
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := audit_model.WithActor(t.Context(), &audit_model.Actor{Type: audit_model.ActorUser, ID: 2, Name: "user2\x1b[31m"})
	require.NoError(t, audit_model.Record(ctx, ev))
	e := &audit_model.Entry{}
	has, err := db.GetEngine(t.Context()).Desc("id").Get(e)
	require.NoError(t, err)
	require.True(t, has)
	return e
}

func decode(t *testing.T, e *audit_model.Entry) map[string]any {
	t.Helper()
	m := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(e.Data), &m))
	return m
}

func TestRecordWhitelist(t *testing.T) {
	e := recordOne(t, audit_model.Event{
		Action: "credential.access_token.created", Category: audit_model.CategoryCredential, Result: audit_model.ResultSuccess,
		TargetType: "access_token", TargetID: 5, TargetName: "ci",
		Data: map[string]any{
			"after":  map[string]any{"name": "ci", "token": "secret", "token_hash": "h", "token_salt": "s", "token_last_eight": "l", "repo_ids": []int64{1, 2}},
			"token":  "secret",
			"other":  1,
			"before": "not a map",
			"reason": "x",
		},
	})
	data := decode(t, e)
	assert.Equal(t, map[string]any{"after": map[string]any{"name": "ci", "repo_ids": []any{1.0, 2.0}}, "reason": "x"}, data)
	assert.NotContains(t, e.Data, "secret")
	assert.Equal(t, "user2?[31m", e.ActorName, "control characters are stripped")
	assert.Nil(t, e.Seq)
	assert.Nil(t, e.Hash)

	for typ, fields := range audit_model.Fields {
		for _, f := range fields {
			assert.NotContains(t, []string{"token", "token_hash", "token_salt", "token_last_eight", "secret", "password"}, f, typ)
		}
	}
}

func TestRecordUnknownTargetKeepsNothing(t *testing.T) {
	e := recordOne(t, audit_model.Event{Action: "x", TargetType: "nope", Data: map[string]any{"name": "a"}})
	assert.Equal(t, "{}", e.Data)
}

func TestText(t *testing.T) {
	s := strings.Repeat("é", 200) // 400 bytes
	got := audit_model.Text(s, 255)
	assert.LessOrEqual(t, len(got), 255)
	assert.True(t, strings.HasSuffix(got, "…"))
	assert.Equal(t, "a?b", audit_model.Text("a\nb", 255))
	assert.Equal(t, "a\uFFFDb", audit_model.Text("a\xffb", 255), "invalid UTF-8 is replaced")

	e := recordOne(t, audit_model.Event{Action: "x", TargetType: "access_token", TargetName: s, Data: map[string]any{"name": s}})
	assert.LessOrEqual(t, len(e.TargetName), 255)
	assert.Equal(t, got, decode(t, e)["name"])
}

func TestRecordTruncatesData(t *testing.T) {
	ids := make([]int64, 10000) // ~60 KB of JSON
	for i := range ids {
		ids[i] = 100000 + int64(i)
	}
	e := recordOne(t, audit_model.Event{Action: "x", TargetType: "access_token", Data: map[string]any{
		"after": map[string]any{"repo_ids": ids, "name": "ci"},
	}})
	assert.LessOrEqual(t, len(e.Data), 32*1024)
	data := decode(t, e)
	assert.Equal(t, true, data["truncated"])
	after := data["after"].(map[string]any)
	assert.Equal(t, "ci", after["name"])
	assert.NotEmpty(t, after["repo_ids"])
}

func TestRecordWithoutActor(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	require.NoError(t, audit_model.Record(t.Context(), audit_model.Event{Action: "x"}))
	e := &audit_model.Entry{}
	_, err := db.GetEngine(t.Context()).Desc("id").Get(e)
	require.NoError(t, err)
	assert.Equal(t, audit_model.ActorUnknown, e.ActorType)
}

func TestRecordAmbiguousIP(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := audit_model.WithActor(t.Context(), &audit_model.Actor{Type: audit_model.ActorUser, IP: "10.0.0.1", IPAmbiguous: true, IPCandidates: []string{"1.2.3.4"}})
	require.NoError(t, audit_model.Record(ctx, audit_model.Event{Action: "x"}))
	e := &audit_model.Entry{}
	_, err := db.GetEngine(t.Context()).Desc("id").Get(e)
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1", e.IP)
	assert.JSONEq(t, `{"ip_ambiguous":true,"ip_candidates":["1.2.3.4"]}`, e.Data)
}
