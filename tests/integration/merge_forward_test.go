// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/gitrepo"
	api "forgejo.org/modules/structs"
	"forgejo.org/services/forms"
	"forgejo.org/services/mergeforward"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeForward(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, _ *url.URL) {
		defer tests.PrintCurrentTest(t)()
		name := bwRepo(t, "mf-chain")
		owner := loginUser(t, "user2")
		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user2"})
		ctx := NewAPITestContext(t, "user2", name, auth_model.AccessTokenScopeWriteOrganization, auth_model.AccessTokenScopeWriteRepository)
		for _, b := range []string{"develop", "release/1.9", "release/1.10", "release/2.0"} {
			testAPICreateBranch(t, owner, "org3", name, "master", b, http.StatusCreated)
		}
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{OwnerName: "org3", Name: name})
		setModel := func(t *testing.T, mergeForward bool) {
			t.Helper()
			req := NewRequestWithJSON(t, "PUT", "/api/v1/repos/org3/"+name+"/branching-model", &api.EditRepoBranchingModelOption{
				DevelopmentBranch: "develop", ProductionBranch: "master", ReleasePrefixes: []string{"release/"}, MergeForward: mergeForward,
			}).AddTokenAuth(ctx.Token)
			ctx.Session.MakeRequest(t, req, http.StatusOK)
		}
		// pull opens a pull request into base from a new branch with one new file and returns it
		pull := func(t *testing.T, base, branch, file, content string) api.PullRequest {
			t.Helper()
			testAPICreateBranch(t, owner, "org3", name, base, branch, http.StatusCreated)
			require.NoError(t, createOrReplaceFileInBranch(user2, repo, file, branch, content))
			pr, err := doAPICreatePullRequest(ctx, "org3", name, base, branch)(t)
			require.NoError(t, err)
			return pr
		}
		merge := func(t *testing.T, pr api.PullRequest) {
			t.Helper()
			doAPIMergePullRequest(ctx, "org3", name, pr.Index)(t)
		}
		has := func(t *testing.T, branch, file string) bool {
			t.Helper()
			req := NewRequest(t, "GET", fmt.Sprintf("/api/v1/repos/org3/%s/contents/%s?ref=%s", name, file, url.QueryEscape(branch))).AddTokenAuth(ctx.Token)
			return ctx.Session.MakeRequest(t, req, NoExpectedStatus).Code == http.StatusOK
		}
		comments := func(pr api.PullRequest, typ issues_model.CommentType) []*issues_model.Comment {
			var list []*issues_model.Comment
			require.NoError(t, db.GetEngine(t.Context()).Where("issue_id = ? AND type = ?", issueIDOf(t, repo, pr.Index), typ).OrderBy("id").Find(&list))
			return list
		}

		t.Run("off", func(t *testing.T) {
			setModel(t, false)
			merge(t, pull(t, "release/1.9", "fix-off", "off.txt", "off"))
			assert.False(t, has(t, "release/1.10", "off.txt"))
			assert.False(t, has(t, "develop", "off.txt"))
		})

		t.Run("clean cascade", func(t *testing.T) {
			setModel(t, true)
			since := auditMaxID(t)
			pr := pull(t, "release/1.9", "fix-1", "fix1.txt", "fix")
			merge(t, pr)
			for _, b := range []string{"release/1.10", "release/2.0", "develop"} {
				assert.True(t, has(t, b, "fix1.txt"), b)
			}
			assert.False(t, has(t, "master", "fix1.txt"), "the default branch is not the development branch here")

			gitRepo, err := gitrepo.OpenRepository(t.Context(), repo)
			require.NoError(t, err)
			defer gitRepo.Close()
			head, err := gitRepo.GetBranchCommit("release/1.10")
			require.NoError(t, err)
			assert.Equal(t, fmt.Sprintf("Automatic merge from release/1.9 -> release/1.10 (PR #%d)", pr.Index), strings.TrimSpace(head.Message()))
			assert.Equal(t, user2.NewGitSig().Name, head.Author.Name)
			assert.Equal(t, 2, head.ParentCount())

			list := comments(pr, issues_model.CommentTypePRMergedForward)
			require.Len(t, list, 1)
			info := list[0].MergeForwardInfo()
			require.Len(t, info.Steps, 3)
			assert.Equal(t, []string{"release/1.10", "release/2.0", "develop"}, []string{info.Steps[0].Branch, info.Steps[1].Branch, info.Steps[2].Branch})
			assert.Equal(t, head.ID.String(), info.Steps[0].SHA)
			assert.Equal(t, user2.ID, list[0].PosterID)

			entries := auditActions(t, since, "repo.branch.merged_forward")
			require.Len(t, entries, 3)
			assert.Equal(t, "release/1.10", entries[0].TargetName)
			assert.Equal(t, "release/1.9", auditData(t, entries[0])["from"])

			// the timeline renders the steps
			doc := NewHTMLParser(t, owner.MakeRequest(t, NewRequest(t, "GET", fmt.Sprintf("/org3/%s/pulls/%d", name, pr.Index)), http.StatusOK).Body)
			detail := doc.doc.Find(".merge-forward-detail").Text()
			for _, b := range []string{"release/1.10", "release/2.0", "develop"} {
				assert.Contains(t, detail, b)
			}
		})

		t.Run("a hotfix in the production branch reaches develop only", func(t *testing.T) {
			pr := pull(t, "master", "hotfix-1", "hotfix.txt", "fix")
			merge(t, pr)
			assert.True(t, has(t, "develop", "hotfix.txt"))
			assert.False(t, has(t, "release/2.0", "hotfix.txt"))
			list := comments(pr, issues_model.CommentTypePRMergedForward)
			require.Len(t, list, 1)
			assert.Equal(t, []issues_model.MergeForwardStep{{Branch: "develop", SHA: list[0].MergeForwardInfo().Steps[0].SHA}}, list[0].MergeForwardInfo().Steps)
		})

		t.Run("nothing to merge", func(t *testing.T) {
			yes := true
			req := NewRequestWithJSON(t, "PATCH", "/api/v1/repos/org3/"+name, &api.EditRepoOption{AllowFastForwardOnly: &yes}).AddTokenAuth(ctx.Token)
			ctx.Session.MakeRequest(t, req, http.StatusOK)
			// the change is in release/1.10, and through it in release/2.0 and develop, before it reaches release/1.9
			fix := pull(t, "release/1.10", "fix-2", "fix2.txt", "fix")
			merge(t, fix)
			require.True(t, has(t, "develop", "fix2.txt"))

			since := auditMaxID(t)
			pr, err := doAPICreatePullRequest(ctx, "org3", name, "release/1.9", "fix-2")(t)
			require.NoError(t, err)
			doAPIMergePullRequestForm(t, ctx, "org3", name, pr.Index, &forms.MergePullRequestForm{Do: string(repo_model.MergeStyleFastForwardOnly)})
			assert.True(t, has(t, "release/1.9", "fix2.txt"))
			assert.Empty(t, comments(pr, issues_model.CommentTypePRMergedForward))
			assert.Empty(t, comments(pr, issues_model.CommentTypePRMergeForwardStopped))
			assert.Empty(t, auditActions(t, since, "repo.branch.merged_forward"))
		})

		t.Run("conflict opens a pull request that continues", func(t *testing.T) {
			require.NoError(t, createOrReplaceFileInBranch(user2, repo, "conflict.txt", "release/2.0", "two"))
			since := auditMaxID(t)
			pr := pull(t, "release/1.9", "fix-3", "conflict.txt", "fix")
			merge(t, pr)
			assert.True(t, has(t, "release/1.10", "conflict.txt"))
			assert.False(t, has(t, "develop", "conflict.txt"))

			merged := comments(pr, issues_model.CommentTypePRMergedForward)
			require.Len(t, merged, 1)
			require.Len(t, merged[0].MergeForwardInfo().Steps, 1)
			stopped := comments(pr, issues_model.CommentTypePRMergeForwardStopped)
			require.Len(t, stopped, 1)
			assert.Equal(t, issues_model.MergeForwardInfo{Branch: "release/2.0", Reason: mergeforward.ReasonConflict}, stopped[0].MergeForwardInfo())

			branch := fmt.Sprintf("automerge/pr-%d/release-2.0", pr.Index)
			opened := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{BaseRepoID: repo.ID, HeadBranch: branch, BaseBranch: "release/2.0"})
			assert.Equal(t, opened.IssueID, stopped[0].RefIssueID)
			issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: opened.IssueID})
			assert.Equal(t, fmt.Sprintf("Automatic merge from release/1.10 -> release/2.0 (PR #%d)", pr.Index), issue.Title)
			require.NoError(t, issue.LoadAssignees(t.Context()))
			require.Len(t, issue.Assignees, 1)
			assert.Equal(t, user2.ID, issue.Assignees[0].ID)

			entries := auditActions(t, since, "repo.branch.merge_forward_stopped")
			require.Len(t, entries, 1)
			data := auditData(t, entries[0])
			assert.Equal(t, "conflict", data["reason"])
			assert.InDelta(t, float64(issue.Index), data["conflict_pull"], 0)

			doc := NewHTMLParser(t, owner.MakeRequest(t, NewRequest(t, "GET", fmt.Sprintf("/org3/%s/pulls/%d", name, pr.Index)), http.StatusOK).Body)
			assert.Contains(t, doc.doc.Find(".merge-forward-detail").Text(), "The merge has conflicts.")

			// resolve it the way release/2.0 has it, merge the pull request, and merging forward goes on to develop
			require.NoError(t, createOrReplaceFileInBranch(user2, repo, "conflict.txt", branch, "two"))
			merge(t, api.PullRequest{Index: issue.Index})
			assert.True(t, has(t, "develop", "conflict.txt"))
			cont := comments(api.PullRequest{Index: issue.Index}, issues_model.CommentTypePRMergedForward)
			require.Len(t, cont, 1)
			assert.Equal(t, "develop", cont[0].MergeForwardInfo().Steps[0].Branch)
		})

		t.Run("a branch rule refuses a step", func(t *testing.T) {
			bwProtect(t, ctx, name, "develop", parameterProtectBranch{})
			pr := pull(t, "release/2.0", "fix-4", "fix4.txt", "fix")
			merge(t, pr)
			assert.False(t, has(t, "develop", "fix4.txt"))
			stopped := comments(pr, issues_model.CommentTypePRMergeForwardStopped)
			require.Len(t, stopped, 1)
			assert.Equal(t, issues_model.MergeForwardInfo{Branch: "develop", Reason: mergeforward.ReasonRefused}, stopped[0].MergeForwardInfo())
			unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{BaseRepoID: repo.ID, HeadBranch: fmt.Sprintf("automerge/pr-%d/develop", pr.Index), BaseBranch: "develop"})
		})
	})
}

func issueIDOf(t *testing.T, repo *repo_model.Repository, index int64) int64 {
	t.Helper()
	return unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{RepoID: repo.ID, Index: index}).ID
}
