// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package git

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBranchRuleMatch(t *testing.T) {
	kases := []struct {
		Rule          string
		BranchName    string
		ExpectedMatch bool
	}{
		{
			Rule:          "release/*",
			BranchName:    "release/v1.17",
			ExpectedMatch: true,
		},
		{
			Rule:          "release/**/v1.17",
			BranchName:    "release/test/v1.17",
			ExpectedMatch: true,
		},
		{
			Rule:          "release/**/v1.17",
			BranchName:    "release/test/1/v1.17",
			ExpectedMatch: true,
		},
		{
			Rule:          "release/*/v1.17",
			BranchName:    "release/test/1/v1.17",
			ExpectedMatch: false,
		},
		{
			Rule:          "release/v*",
			BranchName:    "release/v1.16",
			ExpectedMatch: true,
		},
		{
			Rule:          "*",
			BranchName:    "release/v1.16",
			ExpectedMatch: false,
		},
		{
			Rule:          "**",
			BranchName:    "release/v1.16",
			ExpectedMatch: true,
		},
		{
			Rule:          "main",
			BranchName:    "main",
			ExpectedMatch: true,
		},
		{
			Rule:          "master",
			BranchName:    "main",
			ExpectedMatch: false,
		},
	}

	for _, kase := range kases {
		pb := ProtectedBranch{RuleName: kase.Rule}
		assert.Equal(t, kase.ExpectedMatch, pb.Match(kase.BranchName), "%s - %s", kase.BranchName, kase.Rule)
	}
}

func TestCanDeployKeyPush(t *testing.T) {
	cases := []struct {
		name    string
		rule    ProtectedBranch
		keyID   int64
		allowed bool
	}{
		{"push disabled", ProtectedBranch{CanPush: false}, 1, false},
		{"push enabled without allow-list", ProtectedBranch{CanPush: true}, 1, true},
		{"allow-list without deploy keys", ProtectedBranch{CanPush: true, EnableWhitelist: true}, 1, false},
		{"allow-list with all deploy keys", ProtectedBranch{CanPush: true, EnableWhitelist: true, WhitelistDeployKeys: true}, 1, true},
		{"allow-list with listed key", ProtectedBranch{CanPush: true, EnableWhitelist: true, WhitelistDeployKeyIDs: []int64{1, 2}}, 2, true},
		{"allow-list with unlisted key", ProtectedBranch{CanPush: true, EnableWhitelist: true, WhitelistDeployKeyIDs: []int64{1, 2}}, 3, false},
		{"listed key but push disabled", ProtectedBranch{CanPush: false, EnableWhitelist: true, WhitelistDeployKeyIDs: []int64{1}}, 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.allowed, c.rule.CanDeployKeyPush(c.keyID))
		})
	}
}
