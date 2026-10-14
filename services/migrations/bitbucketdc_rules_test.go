// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package migrations

import (
	"net/url"
	"testing"

	git_model "forgejo.org/models/git"
	webhook_module "forgejo.org/modules/webhook"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBitbucketBranchRule(t *testing.T) {
	for _, c := range []struct {
		matcherType, id string
		tags            bool
		match, noMatch  []string // branch names Bitbucket matches, and ones it doesn't (the rule may still)
	}{
		{"BRANCH", "refs/heads/main", false, []string{"main"}, []string{"main2", "x/main"}},
		{"BRANCH", "refs/heads/rel[1]{a}", false, []string{"rel[1]{a}"}, []string{"rel1a"}},
		{"PATTERN", "refs/heads/release/*", false, []string{"release/1", "release/x"}, []string{"release/1/2", "x/release/1"}},
		{"PATTERN", "release/*", true, []string{"release/1", "team/release/1"}, []string{"release/1/2"}},
		{"PATTERN", "heads/**/master", false, []string{"master", "a/master", "a/b/master", "x/heads/y/master"}, []string{"master2"}},
		{"PATTERN", "feature/", true, []string{"feature", "feature/x", "feature/x/y", "a/feature/x"}, []string{"features"}},
		{"PATTERN", "**/hotfix", true, []string{"hotfix", "a/hotfix", "a/b/hotfix"}, []string{"hotfix2"}},
		{"PATTERN", "a/**/b", true, []string{"a/b", "a/x/b", "a/x/y/b", "z/a/b"}, []string{"a/bc"}},
		{"PATTERN", "v?", true, []string{"v1", "x/v1"}, []string{"v10", "v/"}},
		{"PATTERN", "a{b}[c]", true, []string{"a{b}[c]", "x/a{b}[c]"}, []string{"abc"}},
		{"PATTERN", "a,b!c", true, []string{"a,b!c", "x/a,b!c"}, []string{"a", "b!c"}},
		{"PATTERN", "refs/heads/x,y", false, []string{"x,y"}, []string{"x", "y"}},
	} {
		rule, tags, ok := bitbucketBranchRule(c.matcherType, c.id)
		if !assert.True(t, ok, c.id) {
			continue
		}
		assert.Equal(t, c.tags, tags, c.id)
		pb := &git_model.ProtectedBranch{RuleName: rule}
		for _, b := range c.match {
			assert.True(t, pb.Match(b), "%s (%s) must match %s", c.id, rule, b)
		}
		for _, b := range c.noMatch {
			assert.False(t, pb.Match(b), "%s (%s) needn't match %s", c.id, rule, b)
		}
	}

	for _, c := range []struct{ matcherType, id string }{
		{"BRANCH", "refs/tags/v1"},
		{"BRANCH", "refs/heads/"},
		{"PATTERN", "refs/tags/*"},
		{"PATTERN", "tags/v*"},
		{"PATTERN", "refs/remotes/x"},
		{"PATTERN", ""},
		{"MODEL_BRANCH", "production"},
		{"MODEL_CATEGORY", "FEATURE"},
		{"ANY_REF", "ANY_REF_MATCHER_ID"},
	} {
		_, _, ok := bitbucketBranchRule(c.matcherType, c.id)
		assert.False(t, ok, "%s %s is unmapped", c.matcherType, c.id)
	}
}

func TestBitbucketHookEvents(t *testing.T) {
	events, unmapped := bitbucketHookEvents([]string{
		"repo:refs_changed", "repo:modified", "repo:forked", "pr:opened", "pr:merged",
		"pr:from_ref_updated", "pr:reviewer:approved", "pr:reviewer:updated", "pr:comment:added", "mirror:repo_synchronized", "repo:comment:added",
	})
	assert.Equal(t, webhook_module.HookEvents{
		Push: true, Create: true, Delete: true, Repository: true, Fork: true, PullRequest: true, PullRequestSync: true,
		PullRequestReview: true, PullRequestReviewRequest: true, PullRequestComment: true,
	}, events)
	assert.Equal(t, []string{"mirror:repo_synchronized", "repo:comment:added"}, unmapped)
}

func TestRedactedHookURL(t *testing.T) {
	u, err := url.Parse("https://user:pass@hooks.example.com:8443/path/x?token=secret#frag")
	require.NoError(t, err)
	assert.Equal(t, "https://hooks.example.com:8443/path/x", redactedHookURL(u))
}

func TestSameBitbucketRule(t *testing.T) {
	want := &git_model.ProtectedBranch{RuleName: "main", CanPush: true, EnableWhitelist: true, WhitelistUserIDs: []int64{2, 1}, ApplyToAdmins: true}
	have := &git_model.ProtectedBranch{ID: 7, RepoID: 3, RuleName: "main", CanPush: true, EnableWhitelist: true, WhitelistUserIDs: []int64{1, 2}, MergeWhitelistUserIDs: []int64{}, ApplyToAdmins: true}
	assert.True(t, sameBitbucketRule(want, have))
	have.RequireSignedCommits = true
	assert.False(t, sameBitbucketRule(want, have))
	have.RequireSignedCommits, have.StatusCheckContexts = false, []string{"ci"}
	assert.False(t, sameBitbucketRule(want, have))
	have.StatusCheckContexts, have.WhitelistUserIDs = nil, []int64{1}
	assert.False(t, sameBitbucketRule(want, have))
}
