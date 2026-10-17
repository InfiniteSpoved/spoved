// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package audit_test

import (
	"testing"

	audit_model "forgejo.org/models/audit"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The canonical form and the hash are an external contract (auditors recompute them): never change these values.
func TestCanonicalGolden(t *testing.T) {
	e := &audit_model.Entry{
		ID: 7, CreatedUnix: 1700000000, Action: "auth.signin", Category: "auth", Result: "success",
		ActorType: "user", ActorID: 2, ActorName: "user2", SudoByID: 3,
		CredentialType: "access_token", CredentialID: 4, CredentialName: "t<&>", CredentialOwnerID: 2,
		IP: "192.0.2.1", UserAgent: "ua\"é", RequestID: "rid", OwnerID: 5, GroupID: 6, RepoID: 8, SubjectUserID: 2,
		TargetType: "access_token", TargetID: 9, TargetName: "tn", Data: `{"a":1}`,
	}
	canonical, err := audit_model.Canonical(e)
	require.NoError(t, err)
	assert.Equal(t, `[7,1700000000,"auth.signin","auth","success","user",2,"user2",3,"access_token",4,"t\u003c\u0026\u003e",2,"192.0.2.1","ua\"é","rid",5,6,8,2,"access_token",9,"tn","{\"a\":1}"]`, string(canonical))

	hash, err := audit_model.HashEntry(audit_model.ZeroHash, e)
	require.NoError(t, err)
	assert.Equal(t, "3dc898788830893539867a50b9af57d00bfdb2fbe05413364158fd215958589b", hash)
	assert.Len(t, hash, 64)
}
