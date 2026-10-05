// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	issues_model "forgejo.org/models/issues"
	"forgejo.org/models/perm"
	repo_model "forgejo.org/models/repo"
	unit_model "forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/gitrepo"
	api "forgejo.org/modules/structs"
	"forgejo.org/services/automerge"
	"forgejo.org/services/forms"
	"forgejo.org/tests"
	"forgejo.org/tests/forgery"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type conversationCheckPR struct {
	repo    *repo_model.Repository
	pr      *issues_model.PullRequest
	headSHA string
}

func prepareConversationCheckPR(t *testing.T, collaborators map[*user_model.User]perm.AccessMode) *conversationCheckPR {
	t.Helper()
	repo := forgery.CreateRepository(t, nil, &forgery.CreateRepositoryOptions{
		Files:         forgery.FilesInit{},
		Collaborators: collaborators,
	})
	forgery.EnableRepoUnits(t, repo, unit_model.TypeCode, unit_model.TypePullRequests)
	pr := createPullRequest(t, repo.Owner, repo, "feature", "conversation check")

	gitRepo, err := gitrepo.OpenRepository(t.Context(), repo)
	require.NoError(t, err)
	defer gitRepo.Close()
	headSHA, err := gitRepo.GetRefCommitID(pr.GetGitRefName())
	require.NoError(t, err)

	return &conversationCheckPR{repo: repo, pr: pr, headSHA: headSHA}
}

func (c *conversationCheckPR) link() string {
	return fmt.Sprintf("/%s/pulls/%d", c.repo.FullName(), c.pr.Index)
}

func (c *conversationCheckPR) apiLink() string {
	return fmt.Sprintf("/api/v1/repos/%s/pulls/%d", c.repo.FullName(), c.pr.Index)
}

func (c *conversationCheckPR) protect(t *testing.T, pb git_model.ProtectedBranch) {
	t.Helper()
	pb.RepoID = c.repo.ID
	pb.RuleName = "main"
	if old, err := git_model.GetProtectedBranchRuleByName(t.Context(), c.repo.ID, "main"); err == nil && old != nil {
		pb.ID = old.ID
	}
	require.NoError(t, git_model.UpdateProtectBranch(t.Context(), c.repo, &pb, git_model.WhitelistOptions{}))
}

// codeComment posts a single diff comment on line 1 of README.md and returns it.
func (c *conversationCheckPR) codeComment(t *testing.T, session *TestSession, content string, expectedStatus int) *issues_model.Comment {
	t.Helper()
	session.MakeRequest(t, NewRequestWithValues(t, "POST", c.link()+"/files/reviews/comments", map[string]string{
		"origin":           "diff",
		"content":          content,
		"side":             "proposed",
		"line":             "1",
		"path":             "README.md",
		"single_review":    "true",
		"latest_commit_id": c.headSHA,
	}), expectedStatus)
	if expectedStatus != http.StatusOK {
		unittest.AssertNotExistsBean(t, &issues_model.Comment{IssueID: c.pr.IssueID, Content: content})
		return nil
	}
	return unittest.AssertExistsAndLoadBean(t, &issues_model.Comment{IssueID: c.pr.IssueID, Type: issues_model.CommentTypeCode, Content: content})
}

func (c *conversationCheckPR) resolve(t *testing.T, session *TestSession, comment *issues_model.Comment, expectedStatus int) {
	t.Helper()
	session.MakeRequest(t, NewRequestWithValues(t, "POST", fmt.Sprintf("/%s/issues/resolve_conversation", c.repo.FullName()), map[string]string{
		"origin":     "timeline",
		"action":     "Resolve",
		"comment_id": strconv.FormatInt(comment.ID, 10),
	}), expectedStatus)
}

func (c *conversationCheckPR) mergeableReasons(t *testing.T, token string) []string {
	t.Helper()
	resp := MakeRequest(t, NewRequest(t, "GET", c.apiLink()).AddTokenAuth(token), http.StatusOK)
	var apiPR api.PullRequest
	DecodeJSON(t, resp, &apiPR)
	return apiPR.MergeableReasons
}

func (c *conversationCheckPR) hasMerged(t *testing.T) bool {
	t.Helper()
	return unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: c.pr.ID}).HasMerged
}

// AC1: the two options are saved and shown in the web form and in the API.
func TestPullConversationCheckOptions(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		c := prepareConversationCheckPR(t, nil)
		session := loginUser(t, c.repo.Owner.Name)
		token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)

		session.MakeRequest(t, NewRequestWithValues(t, "POST", fmt.Sprintf("/%s/settings/branches/edit", c.repo.FullName()), map[string]string{
			"rule_name":                         "main",
			"block_on_unresolved_conversations": "on",
			"ignore_outdated_conversations":     "on",
		}), http.StatusSeeOther)
		pb, err := git_model.GetProtectedBranchRuleByName(t.Context(), c.repo.ID, "main")
		require.NoError(t, err)
		assert.True(t, pb.BlockOnUnresolvedConversations)
		assert.True(t, pb.IgnoreOutdatedConversations)

		resp := session.MakeRequest(t, NewRequest(t, "GET", fmt.Sprintf("/%s/settings/branches/edit?rule_name=main", c.repo.FullName())), http.StatusOK)
		doc := NewHTMLParser(t, resp.Body)
		doc.AssertElementChecked(t, `input[name="block_on_unresolved_conversations"]`)
		doc.AssertElementChecked(t, `input[name="ignore_outdated_conversations"]`)

		branchLink := fmt.Sprintf("/api/v1/repos/%s/branch_protections/main", c.repo.FullName())
		resp = MakeRequest(t, NewRequest(t, "GET", branchLink).AddTokenAuth(token), http.StatusOK)
		var bp api.BranchProtection
		DecodeJSON(t, resp, &bp)
		assert.True(t, bp.BlockOnUnresolvedConversations)
		assert.True(t, bp.IgnoreOutdatedConversations)

		resp = MakeRequest(t, NewRequestWithJSON(t, "PATCH", branchLink, &api.EditBranchProtectionOption{
			IgnoreOutdatedConversations: new(false),
		}).AddTokenAuth(token), http.StatusOK)
		DecodeJSON(t, resp, &bp)
		assert.True(t, bp.BlockOnUnresolvedConversations)
		assert.False(t, bp.IgnoreOutdatedConversations)

		resp = MakeRequest(t, NewRequestWithJSON(t, "POST", fmt.Sprintf("/api/v1/repos/%s/branch_protections", c.repo.FullName()), &api.CreateBranchProtectionOption{
			RuleName:                       "release",
			BlockOnUnresolvedConversations: true,
			IgnoreOutdatedConversations:    true,
		}).AddTokenAuth(token), http.StatusCreated)
		DecodeJSON(t, resp, &bp)
		assert.True(t, bp.BlockOnUnresolvedConversations)
		assert.True(t, bp.IgnoreOutdatedConversations)
	})
}

// AC2, AC3, AC5, AC12: an unresolved diff thread blocks web, API and auto-merge
// until the PR author and the comment author resolve their threads.
func TestPullConversationCheckBlocksMerge(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		reviewer := forgery.CreateUser(t, nil)
		outsider := forgery.CreateUser(t, nil)
		c := prepareConversationCheckPR(t, nil)
		owner := c.repo.Owner
		ownerSession := loginUser(t, owner.Name)
		reviewerSession := loginUser(t, reviewer.Name)
		token := getTokenForLoggedInUser(t, ownerSession, auth_model.AccessTokenScopeWriteRepository)

		// with neither option on, nothing changes
		assert.Empty(t, c.mergeableReasons(t, token))
		c.protect(t, git_model.ProtectedBranch{BlockOnUnresolvedConversations: true})

		// a top-level comment never counts
		reviewerSession.MakeRequest(t, NewRequestWithValues(t, "POST", c.link()+"/comments", map[string]string{
			"content": "top-level remark",
		}), http.StatusOK)
		assert.Empty(t, c.mergeableReasons(t, token))

		first := c.codeComment(t, reviewerSession, "first thread", http.StatusOK)
		second := c.codeComment(t, reviewerSession, "second thread", http.StatusOK)
		assert.Equal(t, []string{"unresolved_conversations"}, c.mergeableReasons(t, token))

		t.Run("merge box lists the threads", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			resp := ownerSession.MakeRequest(t, NewRequest(t, "GET", c.link()), http.StatusOK)
			assert.Contains(t, resp.Body.String(), "2 unresolved conversations")
			doc := NewHTMLParser(t, resp.Body)
			for _, comment := range []*issues_model.Comment{first, second} {
				assert.Equal(t, 1, doc.Find(fmt.Sprintf(`.unresolved-conversations a[href$="#issuecomment-%d"]`, comment.ID)).Length())
			}
		})

		t.Run("merges are refused", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			testPullMergeForm(t, ownerSession, http.StatusBadRequest, owner.Name, c.repo.Name, strconv.FormatInt(c.pr.Index, 10), optionsPullMerge{"do": "merge"})
			MakeRequest(t, NewRequestWithJSON(t, "POST", c.apiLink()+"/merge", &forms.MergePullRequestForm{Do: "merge"}).AddTokenAuth(token), http.StatusMethodNotAllowed)

			scheduled, err := automerge.ScheduleAutoMerge(db.DefaultContext, owner, c.pr, repo_model.MergeStyleMerge, "auto merge", false, 0, 0, "")
			require.NoError(t, err)
			assert.True(t, scheduled)
			automerge.StartPRCheckAndAutoMerge(db.DefaultContext, c.pr)
			assert.False(t, c.hasMerged(t))
		})

		t.Run("someone without rights cannot resolve", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			c.resolve(t, loginUser(t, outsider.Name), first, http.StatusForbidden)
			// a reply doesn't make its poster the thread's author
			c.resolve(t, loginUser(t, outsider.Name), second, http.StatusForbidden)
			assert.Zero(t, unittest.AssertExistsAndLoadBean(t, &issues_model.Comment{ID: first.ID}).ResolveDoerID)
		})

		t.Run("PR author and comment author resolve, then auto-merge merges", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			c.resolve(t, ownerSession, first, http.StatusOK)
			assert.Equal(t, []string{"unresolved_conversations"}, c.mergeableReasons(t, token))
			assert.False(t, c.hasMerged(t))

			c.resolve(t, reviewerSession, second, http.StatusOK)
			assert.True(t, c.hasMerged(t))
		})
	})
}

// AC4: an outdated thread blocks unless outdated conversations are ignored.
func TestPullConversationCheckOutdated(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		reviewer := forgery.CreateUser(t, nil)
		c := prepareConversationCheckPR(t, nil)
		token := getTokenForLoggedInUser(t, loginUser(t, c.repo.Owner.Name), auth_model.AccessTokenScopeReadRepository)
		comment := c.codeComment(t, loginUser(t, reviewer.Name), "outdated thread", http.StatusOK)
		require.NoError(t, issues_model.UpdateCommentInvalidate(t.Context(), &issues_model.Comment{ID: comment.ID, Invalidated: true}))

		c.protect(t, git_model.ProtectedBranch{BlockOnUnresolvedConversations: true})
		assert.Equal(t, []string{"unresolved_conversations"}, c.mergeableReasons(t, token))

		c.protect(t, git_model.ProtectedBranch{BlockOnUnresolvedConversations: true, IgnoreOutdatedConversations: true})
		assert.Empty(t, c.mergeableReasons(t, token))
	})
}

// AC11: repo admins merge past the check only when ApplyToAdmins is off.
func TestPullConversationCheckApplyToAdmins(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		admin := forgery.CreateUser(t, nil)
		reviewer := forgery.CreateUser(t, nil)
		c := prepareConversationCheckPR(t, map[*user_model.User]perm.AccessMode{admin: perm.AccessModeAdmin})
		c.codeComment(t, loginUser(t, reviewer.Name), "unresolved", http.StatusOK)
		token := getTokenForLoggedInUser(t, loginUser(t, admin.Name), auth_model.AccessTokenScopeWriteRepository)
		merge := &forms.MergePullRequestForm{Do: "merge", ForceMerge: true}

		c.protect(t, git_model.ProtectedBranch{BlockOnUnresolvedConversations: true, ApplyToAdmins: true})
		MakeRequest(t, NewRequestWithJSON(t, "POST", c.apiLink()+"/merge", merge).AddTokenAuth(token), http.StatusMethodNotAllowed)
		assert.False(t, c.hasMerged(t))

		c.protect(t, git_model.ProtectedBranch{BlockOnUnresolvedConversations: true})
		MakeRequest(t, NewRequestWithJSON(t, "POST", c.apiLink()+"/merge", merge).AddTokenAuth(token), http.StatusOK)
		assert.True(t, c.hasMerged(t))
	})
}

// Security: blocked users and readers of a locked PR can neither open diff threads
// (which would block merges) nor resolve their own.
func TestPullConversationCheckLockedAndBlocked(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		reader := forgery.CreateUser(t, nil)
		blocked := forgery.CreateUser(t, nil)
		c := prepareConversationCheckPR(t, nil)
		readerSession := loginUser(t, reader.Name)
		blockedSession := loginUser(t, blocked.Name)
		readerToken := getTokenForLoggedInUser(t, readerSession, auth_model.AccessTokenScopeWriteRepository)
		blockedToken := getTokenForLoggedInUser(t, blockedSession, auth_model.AccessTokenScopeWriteRepository)
		review := &api.CreatePullReviewOptions{
			Event:    api.ReviewStateComment,
			CommitID: c.headSHA,
			Comments: []api.CreatePullReviewComment{{Path: "README.md", Body: "api thread", NewLineNum: 1}},
		}

		readerComment := c.codeComment(t, readerSession, "reader thread", http.StatusOK)
		blockedComment := c.codeComment(t, blockedSession, "blocked thread", http.StatusOK)

		t.Run("blocked", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			BlockUser(t, c.repo.Owner, blocked)
			c.codeComment(t, blockedSession, "blocked again", http.StatusForbidden)
			blockedSession.MakeRequest(t, NewRequestWithValues(t, "POST", c.link()+"/files/reviews/submit", map[string]string{
				"commit_id": c.headSHA,
				"content":   "blocked review",
				"type":      "comment",
			}), http.StatusForbidden)
			MakeRequest(t, NewRequestWithJSON(t, "POST", c.apiLink()+"/reviews", review).AddTokenAuth(blockedToken), http.StatusForbidden)
			c.resolve(t, blockedSession, blockedComment, http.StatusForbidden)
		})

		t.Run("locked", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			require.NoError(t, c.pr.LoadIssue(t.Context()))
			require.NoError(t, issues_model.LockIssue(t.Context(), &issues_model.IssueLockOptions{Doer: c.repo.Owner, Issue: c.pr.Issue}))
			c.codeComment(t, readerSession, "locked thread", http.StatusSeeOther)
			readerSession.MakeRequest(t, NewRequestWithValues(t, "POST", c.link()+"/files/reviews/submit", map[string]string{
				"commit_id": c.headSHA,
				"content":   "locked review",
				"type":      "comment",
			}), http.StatusSeeOther)
			unittest.AssertNotExistsBean(t, &issues_model.Review{IssueID: c.pr.IssueID, Content: "locked review"})
			MakeRequest(t, NewRequestWithJSON(t, "POST", c.apiLink()+"/reviews", review).AddTokenAuth(readerToken), http.StatusForbidden)
			c.resolve(t, readerSession, readerComment, http.StatusForbidden)
			assert.Zero(t, unittest.AssertExistsAndLoadBean(t, &issues_model.Comment{ID: readerComment.ID}).ResolveDoerID)
		})
	})
}
