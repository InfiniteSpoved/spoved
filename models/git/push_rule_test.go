// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package git_test

import (
	"strings"
	"testing"

	git_model "forgejo.org/models/git"
	"forgejo.org/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPushRuleCompile(t *testing.T) {
	for _, rule := range []git_model.PushRule{
		{CommitMessageRegex: "("},
		{CommitMessageBannedRegex: strings.Repeat("a", git_model.PushRuleMaxPatternLength+1)},
		{BranchNameRegex: "["},
		{BannedFilePatterns: "ok;[a"},
		{BannedFilePatterns: strings.Repeat("a", git_model.PushRuleMaxPatternLength+1)},
		{MaxFileSizeMB: -1},
	} {
		require.Error(t, rule.Compile(), "%+v", rule)
	}

	rule := git_model.PushRule{CommitMessageRegex: "^ABC-", BannedFilePatterns: " *.EXE ; id_rsa;secrets/**;"}
	require.NoError(t, rule.Compile())
	assert.True(t, rule.CommitMessageRe.MatchString("ABC-1 x"))
	assert.Nil(t, rule.BranchNameRe)
	for _, p := range []string{"a.exe", "sub/dir/x.tar.exe", "A/B/ID_RSA", "secrets/a/b"} {
		assert.True(t, rule.IsBannedFile(p), p)
	}
	for _, p := range []string{"exe", "id_rsa.pub", "a/secrets/b", "x.exe/readme"} {
		assert.False(t, rule.IsBannedFile(p), p)
	}
}

func TestPushRuleUpsert(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	rule, err := git_model.GetPushRule(t.Context(), 1)
	require.NoError(t, err)
	assert.Nil(t, rule)

	require.NoError(t, git_model.UpsertPushRule(t.Context(), &git_model.PushRule{RepoID: 1, BranchNameRegex: "^a", MaxFileSizeMB: 2}))
	require.NoError(t, git_model.UpsertPushRule(t.Context(), &git_model.PushRule{RepoID: 1, CommitMessageRegex: "^b"}))
	rule, err = git_model.GetPushRule(t.Context(), 1)
	require.NoError(t, err)
	assert.Equal(t, "^b", rule.CommitMessageRegex)
	assert.Empty(t, rule.BranchNameRegex)
	assert.Zero(t, rule.MaxFileSizeMB)
	unittest.AssertCount(t, &git_model.PushRule{}, 1)
}
