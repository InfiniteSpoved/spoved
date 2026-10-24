// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"forgejo.org/models"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	issues_model "forgejo.org/models/issues"
	"forgejo.org/models/perm"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	"forgejo.org/modules/gitrepo"
	api "forgejo.org/modules/structs"
	pull_service "forgejo.org/services/pull"
	commitstatus_service "forgejo.org/services/repository/commitstatus"
	files_service "forgejo.org/services/repository/files"
	"forgejo.org/tests/forgery"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stackCommit commits content to path on newBranch, created from oldBranch when they differ.
func stackCommit(t *testing.T, user *user_model.User, repo *repo_model.Repository, oldBranch, newBranch, operation, path, content string) {
	t.Helper()
	gitRepo, err := gitrepo.OpenRepository(t.Context(), repo)
	require.NoError(t, err)
	lastCommitID, err := gitRepo.GetBranchCommitID(oldBranch)
	gitRepo.Close()
	require.NoError(t, err)
	_, err = files_service.ChangeRepoFiles(git.DefaultContext, repo, user, &files_service.ChangeRepoFilesOptions{
		Files:        []*files_service.ChangeRepoFile{{Operation: operation, TreePath: path, ContentReader: strings.NewReader(content)}},
		Message:      "change " + path,
		OldBranch:    oldBranch,
		NewBranch:    newBranch,
		LastCommitID: lastCommitID,
		Author:       &files_service.IdentityOptions{Name: user.Name, Email: user.Email},
		Committer:    &files_service.IdentityOptions{Name: user.Name, Email: user.Email},
		Dates:        &files_service.CommitDateOptions{Author: time.Now(), Committer: time.Now()},
	})
	require.NoError(t, err)
}

func stackPull(t *testing.T, user *user_model.User, repo *repo_model.Repository, base, head string) *issues_model.PullRequest {
	t.Helper()
	issue := &issues_model.Issue{RepoID: repo.ID, Title: "stack " + head, PosterID: user.ID, Poster: user, IsPull: true}
	pr := &issues_model.PullRequest{
		HeadRepoID: repo.ID,
		BaseRepoID: repo.ID,
		HeadBranch: head,
		BaseBranch: base,
		HeadRepo:   repo,
		BaseRepo:   repo,
		Type:       issues_model.PullRequestGitea,
	}
	require.NoError(t, pull_service.NewPullRequest(git.DefaultContext, repo, issue, nil, nil, pr, nil))
	return pr
}

// newPullStack creates the stack main <- a <- b <- c, each branch adding its own file.
func newPullStack(t *testing.T, user *user_model.User, collaborators map[*user_model.User]perm.AccessMode) (*repo_model.Repository, []*issues_model.PullRequest) {
	t.Helper()
	repo := forgery.CreateRepository(t, user, &forgery.CreateRepositoryOptions{
		Files:         forgery.MapFS{"README.md": forgery.MapFile("stack")},
		Collaborators: collaborators,
	})
	prs := make([]*issues_model.PullRequest, 0, 3)
	base := "main"
	for _, head := range []string{"a", "b", "c"} {
		stackCommit(t, user, repo, base, head, "create", head+".txt", head)
		prs = append(prs, stackPull(t, user, repo, base, head))
		base = head
	}
	return repo, prs
}

func stackHeads(t *testing.T, repo *repo_model.Repository, prs ...*issues_model.PullRequest) []pull_service.StackHead {
	t.Helper()
	gitRepo, err := gitrepo.OpenRepository(t.Context(), repo)
	require.NoError(t, err)
	defer gitRepo.Close()
	heads := make([]pull_service.StackHead, len(prs))
	for i, pr := range prs {
		sha, err := gitRepo.GetRefCommitID(pr.GetGitRefName())
		require.NoError(t, err)
		heads[i] = pull_service.StackHead{Index: pr.Index, HeadCommitID: sha}
	}
	return heads
}

func mergeStack(t *testing.T, doer *user_model.User, repo *repo_model.Repository, top *issues_model.PullRequest, style repo_model.MergeStyle, heads []pull_service.StackHead) ([]int64, error) {
	t.Helper()
	permission, err := access_model.GetUserRepoPermission(db.DefaultContext, repo, doer)
	require.NoError(t, err)
	top = unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: top.ID})
	return pull_service.MergeStack(db.DefaultContext, doer, &permission, top, style, heads)
}

func assertMerged(t *testing.T, merged bool, prs ...*issues_model.PullRequest) {
	t.Helper()
	for _, pr := range prs {
		pr = unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: pr.ID})
		assert.Equal(t, merged, pr.HasMerged, "pull request #%d", pr.Index)
	}
}

func indexes(prs ...*issues_model.PullRequest) []int64 {
	out := make([]int64, len(prs))
	for i, pr := range prs {
		out[i] = pr.Index
	}
	return out
}

func requireStackMergeError(t *testing.T, err error, index int64) error {
	t.Helper()
	var stackErr pull_service.ErrStackMerge
	require.ErrorAs(t, err, &stackErr)
	assert.Equal(t, index, stackErr.Index)
	return stackErr.Err
}

func TestPullStack(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

		t.Run("API", func(t *testing.T) {
			repo, prs := newPullStack(t, user2, nil)
			stackCommit(t, user2, repo, "main", "alone", "create", "alone.txt", "alone")
			alone := stackPull(t, user2, repo, "main", "alone")
			_, err := db.GetEngine(db.DefaultContext).Exec("UPDATE issue SET name = ? WHERE id = ?", "WIP: stack c", prs[2].IssueID)
			require.NoError(t, err)
			heads := stackHeads(t, repo, prs...)
			require.NoError(t, commitstatus_service.CreateCommitStatus(db.DefaultContext, repo, user2, heads[1].HeadCommitID,
				&git_model.CommitStatus{State: api.CommitStatusFailure, Context: "ci"}))

			token := getUserToken(t, user2.Name, auth_model.AccessTokenScopeReadRepository)
			getStack := func(pr *issues_model.PullRequest) *api.PullRequestStack {
				req := NewRequestf(t, "GET", "/api/v1/repos/%s/%s/pulls/%d/stack", repo.OwnerName, repo.Name, pr.Index).AddTokenAuth(token)
				resp := MakeRequest(t, req, http.StatusOK)
				stack := &api.PullRequestStack{}
				DecodeJSON(t, resp, stack)
				return stack
			}

			stack := getStack(prs[1])
			assert.Equal(t, "main", stack.BaseBranch)
			assert.False(t, stack.Truncated)
			require.Len(t, stack.PullRequests, 3)
			for i, sp := range stack.PullRequests {
				assert.Equal(t, prs[i].Index, sp.Number)
				assert.Equal(t, i, sp.Depth)
				assert.Equal(t, prs[i].HeadBranch, sp.HeadBranch)
				assert.Equal(t, prs[i].BaseBranch, sp.BaseBranch)
				assert.Equal(t, i == 1, sp.Current)
				assert.Equal(t, i == 2, sp.Draft)
				assert.Equal(t, "pending", sp.ReviewState)
				assert.Equal(t, fmt.Sprintf("%s/pulls/%d", repo.HTMLURL(), prs[i].Index), sp.HTMLURL)
			}
			assert.EqualValues(t, 0, stack.PullRequests[0].ParentNumber)
			assert.Equal(t, prs[0].Index, stack.PullRequests[1].ParentNumber)
			assert.Equal(t, prs[1].Index, stack.PullRequests[2].ParentNumber)
			assert.Equal(t, "stack a", stack.PullRequests[0].Title)
			assert.Empty(t, stack.PullRequests[0].CheckState)
			assert.Equal(t, api.CommitStatusFailure, stack.PullRequests[1].CheckState)

			stack = getStack(alone)
			assert.Empty(t, stack.BaseBranch)
			assert.NotNil(t, stack.PullRequests)
			assert.Empty(t, stack.PullRequests)

			req := NewRequestf(t, "GET", "/api/v1/repos/%s/%s/pulls/%d/stack", repo.OwnerName, repo.Name, 999).AddTokenAuth(token)
			MakeRequest(t, req, http.StatusNotFound)
		})

		t.Run("MergeAll", func(t *testing.T) {
			repo, prs := newPullStack(t, user2, nil)
			merged, err := mergeStack(t, user2, repo, prs[2], repo_model.MergeStyleMerge, stackHeads(t, repo, prs...))
			require.NoError(t, err)
			assert.Equal(t, indexes(prs...), merged)
			assertMerged(t, true, prs...)
			for _, pr := range prs {
				pr = unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: pr.ID})
				assert.Equal(t, "main", pr.BaseBranch)
			}

			gitRepo, err := gitrepo.OpenRepository(t.Context(), repo)
			require.NoError(t, err)
			defer gitRepo.Close()
			commit, err := gitRepo.GetBranchCommit("main")
			require.NoError(t, err)
			for _, f := range []string{"a.txt", "b.txt", "c.txt"} {
				has, err := commit.HasFile(f)
				require.NoError(t, err)
				assert.True(t, has, f)
			}
		})

		t.Run("PartOfStack", func(t *testing.T) {
			repo, prs := newPullStack(t, user2, nil)
			merged, err := mergeStack(t, user2, repo, prs[1], repo_model.MergeStyleRebase, stackHeads(t, repo, prs[:2]...))
			require.NoError(t, err)
			assert.Equal(t, indexes(prs[:2]...), merged)
			assertMerged(t, true, prs[:2]...)
			assertMerged(t, false, prs[2])
		})

		t.Run("FailingCheck", func(t *testing.T) {
			repo, prs := newPullStack(t, user2, nil)
			require.NoError(t, git_model.UpdateProtectBranch(db.DefaultContext, repo, &git_model.ProtectedBranch{
				RepoID:              repo.ID,
				RuleName:            "main",
				EnableStatusCheck:   true,
				StatusCheckContexts: []string{"ci"},
			}, git_model.WhitelistOptions{}))
			heads := stackHeads(t, repo, prs...)
			for i, state := range []api.CommitStatusState{api.CommitStatusSuccess, api.CommitStatusFailure, api.CommitStatusSuccess} {
				require.NoError(t, commitstatus_service.CreateCommitStatus(db.DefaultContext, repo, user2, heads[i].HeadCommitID,
					&git_model.CommitStatus{State: state, Context: "ci"}))
			}

			// every pull request is checked against main before anything is merged
			merged, err := mergeStack(t, user2, repo, prs[2], repo_model.MergeStyleMerge, heads)
			assert.Empty(t, merged)
			assert.True(t, models.IsErrDisallowedToMerge(requireStackMergeError(t, err, prs[1].Index)))
			assertMerged(t, false, prs...)
		})

		t.Run("PushToTop", func(t *testing.T) {
			repo, prs := newPullStack(t, user2, nil)
			heads := stackHeads(t, repo, prs...)
			stackCommit(t, user2, repo, "c", "c", "update", "c.txt", "pushed later")
			// the push starts a check of the pull request; the merge checks refuse while it runs
			assert.Eventually(t, func() bool {
				pr, err := issues_model.GetPullRequestByID(db.DefaultContext, prs[2].ID)
				return err == nil && pr.Status != issues_model.PullRequestStatusChecking
			}, 10*time.Second, 50*time.Millisecond)

			merged, err := mergeStack(t, user2, repo, prs[2], repo_model.MergeStyleMerge, heads)
			assert.Equal(t, indexes(prs[:2]...), merged)
			assert.True(t, models.IsErrSHADoesNotMatch(requireStackMergeError(t, err, prs[2].Index)))
			assertMerged(t, true, prs[:2]...)
			assertMerged(t, false, prs[2])
		})

		t.Run("NotAllowed", func(t *testing.T) {
			reader := forgery.CreateUser(t, nil)
			repo, prs := newPullStack(t, user2, map[*user_model.User]perm.AccessMode{reader: perm.AccessModeRead})
			merged, err := mergeStack(t, reader, repo, prs[2], repo_model.MergeStyleMerge, stackHeads(t, repo, prs...))
			assert.Empty(t, merged)
			require.ErrorIs(t, requireStackMergeError(t, err, prs[0].Index), pull_service.ErrUserNotAllowedToMerge)
			assertMerged(t, false, prs...)
		})

		t.Run("Changed", func(t *testing.T) {
			repo, prs := newPullStack(t, user2, nil)
			heads := stackHeads(t, repo, prs...)
			for _, expected := range [][]pull_service.StackHead{
				heads[1:],
				{heads[0], heads[2], heads[1]},
				{heads[0], heads[1], {Index: heads[2].Index}},
				append(heads[:3:3], heads[0]),
			} {
				merged, err := mergeStack(t, user2, repo, prs[2], repo_model.MergeStyleMerge, expected)
				assert.Empty(t, merged)
				require.ErrorIs(t, err, pull_service.ErrStackChanged)
			}
			assertMerged(t, false, prs...)
		})

		t.Run("Concurrent", func(t *testing.T) {
			repo, prs := newPullStack(t, user2, nil)
			heads := stackHeads(t, repo, prs...)
			permission, err := access_model.GetUserRepoPermission(db.DefaultContext, repo, user2)
			require.NoError(t, err)

			var wg sync.WaitGroup
			merged := make([][]int64, 2)
			errs := make([]error, 2)
			for i := range 2 {
				top := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: prs[2].ID})
				wg.Go(func() {
					merged[i], errs[i] = pull_service.MergeStack(db.DefaultContext, user2, &permission, top, repo_model.MergeStyleMerge, heads)
				})
			}
			wg.Wait()

			// one call merges the stack, the other one merges nothing
			winner := 0
			if errs[0] != nil {
				winner = 1
			}
			require.NoError(t, errs[winner])
			assert.Equal(t, indexes(prs...), merged[winner])
			loser := errs[1-winner]
			assert.Empty(t, merged[1-winner])
			assert.True(t, errors.Is(loser, pull_service.ErrStackChanged) || errors.Is(loser, pull_service.ErrHasMerged), "%v", loser)
			assertMerged(t, true, prs...)
		})

		t.Run("SquashConflict", func(t *testing.T) {
			repo := forgery.CreateRepository(t, user2, &forgery.CreateRepositoryOptions{
				Files: forgery.MapFS{"f.txt": forgery.MapFile("one\n")},
			})
			stackCommit(t, user2, repo, "main", "a", "update", "f.txt", "one\ntwo\n")
			stackCommit(t, user2, repo, "a", "a", "update", "f.txt", "one\ntwo\nthree\n")
			a := stackPull(t, user2, repo, "main", "a")
			stackCommit(t, user2, repo, "a", "b", "update", "f.txt", "one\nTWO\nthree\n")
			b := stackPull(t, user2, repo, "a", "b")

			// the squashed commit of a is no ancestor of b: b conflicts with main after the retarget
			merged, err := mergeStack(t, user2, repo, b, repo_model.MergeStyleSquash, stackHeads(t, repo, a, b))
			assert.Equal(t, indexes(a), merged)
			require.ErrorIs(t, requireStackMergeError(t, err, b.Index), pull_service.ErrNotMergeableState)
			assertMerged(t, true, a)
			assertMerged(t, false, b)
			b = unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: b.ID})
			assert.Equal(t, "main", b.BaseBranch)
			assert.Equal(t, issues_model.PullRequestStatusConflict, b.Status)
		})
	})
}

func TestPullRetargetOfficialReview(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		writer := forgery.CreateUser(t, nil)
		approver := forgery.CreateUser(t, nil)
		repo := forgery.CreateRepository(t, user2, &forgery.CreateRepositoryOptions{
			Files:         forgery.MapFS{"README.md": forgery.MapFile("stack")},
			Collaborators: map[*user_model.User]perm.AccessMode{writer: perm.AccessModeWrite, approver: perm.AccessModeWrite},
		})
		stackCommit(t, user2, repo, "main", "a", "create", "a.txt", "a")
		stackCommit(t, user2, repo, "a", "b", "create", "b.txt", "b")
		pr := stackPull(t, user2, repo, "a", "b")
		require.NoError(t, git_model.UpdateProtectBranch(db.DefaultContext, repo, &git_model.ProtectedBranch{
			RepoID:                   repo.ID,
			RuleName:                 "main",
			RequiredApprovals:        1,
			EnableApprovalsWhitelist: true,
		}, git_model.WhitelistOptions{ApprovalsUserIDs: []int64{approver.ID}}))

		gitRepo, err := gitrepo.OpenRepository(t.Context(), repo)
		require.NoError(t, err)
		defer gitRepo.Close()
		sha, err := gitRepo.GetRefCommitID(pr.GetGitRefName())
		require.NoError(t, err)
		pr = unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: pr.ID})
		require.NoError(t, pr.LoadIssue(db.DefaultContext))
		reviews := map[*user_model.User]*issues_model.Review{}
		for _, reviewer := range []*user_model.User{writer, approver} {
			review, _, err := pull_service.SubmitReview(db.DefaultContext, reviewer, gitRepo, pr.Issue, issues_model.ReviewTypeApprove, "", sha, nil)
			require.NoError(t, err)
			// a has no rule: anyone with write access makes official reviews
			assert.True(t, review.Official)
			reviews[reviewer] = review
		}

		pr.Issue.Repo = repo
		require.NoError(t, pull_service.ChangeTargetBranch(db.DefaultContext, pr, user2, "main"))

		assert.False(t, unittest.AssertExistsAndLoadBean(t, &issues_model.Review{ID: reviews[writer].ID}).Official)
		assert.True(t, unittest.AssertExistsAndLoadBean(t, &issues_model.Review{ID: reviews[approver].ID}).Official)
		rule, err := git_model.GetFirstMatchProtectedBranchRule(db.DefaultContext, repo.ID, "main")
		require.NoError(t, err)
		pr = unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: pr.ID})
		assert.EqualValues(t, 1, issues_model.GetGrantedApprovalsCount(db.DefaultContext, rule, pr))

		// never raised again when the target branch changes back
		require.NoError(t, pr.LoadIssue(db.DefaultContext))
		pr.Issue.Repo = repo
		require.NoError(t, pull_service.ChangeTargetBranch(db.DefaultContext, pr, user2, "a"))
		assert.False(t, unittest.AssertExistsAndLoadBean(t, &issues_model.Review{ID: reviews[writer].ID}).Official)
	})
}
