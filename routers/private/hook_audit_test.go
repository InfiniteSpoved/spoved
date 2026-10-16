// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package private

import (
	"context"
	"fmt"
	"strings"
	"testing"

	audit_model "forgejo.org/models/audit"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/json"
	"forgejo.org/modules/private"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuditRefList(t *testing.T) {
	sha := func(i int) string { return fmt.Sprintf("%040x", i) }
	zero := strings.Repeat("0", 40)
	var refs []auditRef
	for i := range 150 {
		refs = append(refs, auditRef{Ref: fmt.Sprintf("refs/heads/b%d", i), Old: sha(i + 1), New: sha(i + 1000), Forced: new(false)})
	}
	refs[140].Forced = new(true)
	refs[145].Forced = nil // not checked
	refs[149].New = zero   // deleted

	list, more := auditRefList(refs)
	require.Len(t, list, auditRefCap)
	assert.Equal(t, 50, more)
	assert.Equal(t, "refs/heads/b140", list[0]["ref"])
	assert.Equal(t, "refs/heads/b145", list[1]["ref"])
	assert.Nil(t, list[1]["forced"])
	assert.Equal(t, "refs/heads/b149", list[2]["ref"])
	assert.Equal(t, "refs/heads/b0", list[3]["ref"])

	t.Run("budget", func(t *testing.T) {
		long := strings.Repeat("x", 400) + "\x1b"
		var refs []auditRef
		for i := range 100 {
			refs = append(refs, auditRef{Ref: "refs/heads/" + long + fmt.Sprint(i), Old: sha(i + 1), New: sha(i + 2), Forced: new(false)})
		}
		refs[99].Forced = new(true)
		list, more := auditRefList(refs)
		b, err := json.Marshal(list)
		require.NoError(t, err)
		assert.LessOrEqual(t, len(b), auditRefBudget)
		assert.Equal(t, 100, len(list)+more)
		assert.Positive(t, more)
		assert.True(t, *list[0]["forced"].(*bool))
		ref := list[0]["ref"].(string)
		assert.Len(t, ref, 255)
		assert.NotContains(t, ref, "\x1b")
	})
}

func TestCheckForcedBudget(t *testing.T) {
	zero := strings.Repeat("0", 40)
	refs := []auditRef{
		{Ref: "refs/heads/new", Old: zero, New: strings.Repeat("1", 40)},
		{Ref: "refs/heads/upd", Old: strings.Repeat("1", 40), New: strings.Repeat("2", 40)},
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // out of time before the first merge-base
	checkForced(ctx, t.TempDir(), refs)
	require.NotNil(t, refs[0].Forced)
	assert.False(t, *refs[0].Forced)
	assert.Nil(t, refs[1].Forced)

	t.Run("not a repository", func(t *testing.T) {
		refs := []auditRef{{Ref: "refs/heads/upd", Old: strings.Repeat("1", 40), New: strings.Repeat("2", 40)}}
		checkForced(t.Context(), t.TempDir(), refs)
		assert.Nil(t, refs[0].Forced) // exit code 128, not 0 or 1
	})
}

func TestHookActor(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})

	cred := func(uid int64, c string) audit_model.Credential {
		return hookActor(t.Context(), repo, &private.HookOptions{UserID: uid, AuditCredential: c}).Credential
	}
	assert.Equal(t, audit_model.Credential{Type: audit_model.CredentialAccessToken, ID: 1, Name: "Token A", OwnerID: 1}, cred(1, "access_token:1"))
	assert.Equal(t, audit_model.CredentialUnknown, cred(2, "access_token:1").Type) // another user's token
	assert.Equal(t, audit_model.CredentialUnknown, cred(2, "access_token:999").Type)
	assert.Equal(t, audit_model.CredentialUnknown, cred(2, "bogus:1").Type)
	assert.Equal(t, audit_model.CredentialUnknown, cred(2, "access_token").Type)
	assert.Equal(t, audit_model.CredentialUnknown, cred(2, "").Type)
	assert.Equal(t, audit_model.Credential{Type: audit_model.CredentialSystem}, cred(2, "system:0"))
	assert.Equal(t, audit_model.Credential{Type: audit_model.CredentialSSHKey, ID: 1, Name: "user2@localhost", OwnerID: 2}, cred(2, "ssh_key:1"))
	assert.Equal(t, audit_model.CredentialUnknown, cred(1, "ssh_key:1").Type)
	assert.Equal(t, audit_model.Credential{Type: audit_model.CredentialSession, OwnerID: 2}, cred(2, "session:0"))

	a := hookActor(t.Context(), repo, &private.HookOptions{UserID: 2, UserName: "user2", AuditIP: "not an ip", AuditRequestID: strings.Repeat("r", 100)})
	assert.Equal(t, audit_model.ActorUser, a.Type)
	assert.EqualValues(t, 2, a.ID)
	assert.Empty(t, a.IP)
	assert.Len(t, a.RequestID, 32)
	a = hookActor(t.Context(), repo, &private.HookOptions{UserID: 2, AuditIP: "::ffff:10.0.0.1"})
	assert.Equal(t, "10.0.0.1", a.IP)
}
