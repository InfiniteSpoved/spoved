// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"errors"
	"fmt"
	"maps"
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
	notify_service "forgejo.org/services/notify"
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

// retargetRecorder records the old target branch of the notified pull requests by index.
type retargetRecorder struct {
	notify_service.NullNotifier
	mu  sync.Mutex
	old map[int64]string
}

func (n *retargetRecorder) PullRequestChangeTargetBranch(_ context.Context, _ *user_model.User, pr *issues_model.PullRequest, oldBranch string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.old == nil {
		n.old = map[int64]string{}
	}
	n.old[pr.Index] = oldBranch
}

func (n *retargetRecorder) get() map[int64]string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return maps.Clone(n.old)
}

// approvePull submits an approval of reviewer for the current head of pr.
func approvePull(t *testing.T, repo *repo_model.Repository, pr *issues_model.PullRequest, reviewer *user_model.User) *issues_model.Review {
	t.Helper()
	gitRepo, err := gitrepo.OpenRepository(t.Context(), repo)
	require.NoError(t, err)
	defer gitRepo.Close()
	sha, err := gitRepo.GetRefCommitID(pr.GetGitRefName())
	require.NoError(t, err)
	pr = unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: pr.ID})
	require.NoError(t, pr.LoadIssue(db.DefaultContext))
	review, _, err := pull_service.SubmitReview(db.DefaultContext, reviewer, gitRepo, pr.Issue, issues_model.ReviewTypeApprove, "", sha, nil)
	require.NoError(t, err)
	return review
}

// waitChecked waits until the check queued by a push to the head of pr is done.
func waitChecked(t *testing.T, prs ...*issues_model.PullRequest) {
	t.Helper()
	assert.Eventually(t, func() bool {
		for _, pr := range prs {
			pr, err := issues_model.GetPullRequestByID(db.DefaultContext, pr.ID)
			if err != nil || pr.Status == issues_model.PullRequestStatusChecking {
				return false
			}
		}
		return true
	}, 10*time.Second, 50*time.Millisecond)
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
			// nothing is left based on the merged branch
			assert.Equal(t, "main", unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: prs[2].ID}).BaseBranch)
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

			// every head is checked before anything is merged
			merged, err := mergeStack(t, user2, repo, prs[2], repo_model.MergeStyleMerge, heads)
			assert.Empty(t, merged)
			assert.True(t, models.IsErrSHADoesNotMatch(requireStackMergeError(t, err, prs[2].Index)))
			assertMerged(t, false, prs...)
		})

		t.Run("PushToMiddle", func(t *testing.T) {
			repo, prs := newPullStack(t, user2, nil)
			heads := stackHeads(t, repo, prs...)
			stackCommit(t, user2, repo, "b", "b", "update", "b.txt", "pushed later")
			assert.Eventually(t, func() bool {
				return stackHeads(t, repo, prs[1])[0].HeadCommitID != heads[1].HeadCommitID
			}, 10*time.Second, 50*time.Millisecond)
			waitChecked(t, prs...)

			merged, err := mergeStack(t, user2, repo, prs[2], repo_model.MergeStyleMerge, heads)
			assert.Empty(t, merged)
			assert.True(t, models.IsErrSHADoesNotMatch(requireStackMergeError(t, err, prs[1].Index)))
			assertMerged(t, false, prs...)
		})

		t.Run("TitleRule", func(t *testing.T) {
			repo, prs := newPullStack(t, user2, nil)
			// refuse mode: a retarget to main is refused for a title that does not match
			require.NoError(t, git_model.UpsertPushRule(t.Context(), &git_model.PushRule{RepoID: repo.ID, PRTitleRegex: `^OK`, PRTitleBranches: "main"}))
			merged, err := mergeStack(t, user2, repo, prs[2], repo_model.MergeStyleMerge, stackHeads(t, repo, prs...))
			assert.Empty(t, merged)
			assert.True(t, git_model.IsErrPRTitleRule(requireStackMergeError(t, err, prs[1].Index)))
			assertMerged(t, false, prs...)
		})

		t.Run("ApprovalsOnBase", func(t *testing.T) {
			approver := forgery.CreateUser(t, nil)
			writer := forgery.CreateUser(t, nil)
			repo, prs := newPullStack(t, user2, map[*user_model.User]perm.AccessMode{approver: perm.AccessModeWrite, writer: perm.AccessModeWrite})
			require.NoError(t, git_model.UpdateProtectBranch(db.DefaultContext, repo, &git_model.ProtectedBranch{
				RepoID:                   repo.ID,
				RuleName:                 "main",
				RequiredApprovals:        1,
				EnableApprovalsWhitelist: true,
			}, git_model.WhitelistOptions{ApprovalsUserIDs: []int64{approver.ID}}))
			approvePull(t, repo, prs[0], approver)
			// a and b have no rule: anyone with write access reviews officially, but not on main
			reviews := []*issues_model.Review{approvePull(t, repo, prs[1], writer), approvePull(t, repo, prs[2], writer)}
			assert.True(t, reviews[0].Official)
			assert.True(t, reviews[1].Official)

			merged, err := mergeStack(t, user2, repo, prs[2], repo_model.MergeStyleMerge, stackHeads(t, repo, prs...))
			assert.Empty(t, merged)
			assert.True(t, models.IsErrDisallowedToMerge(requireStackMergeError(t, err, prs[1].Index)))
			assertMerged(t, false, prs...)
			// the check leaves the reviews as they were
			for _, r := range reviews {
				assert.True(t, unittest.AssertExistsAndLoadBean(t, &issues_model.Review{ID: r.ID}).Official)
			}
		})

		t.Run("Notified", func(t *testing.T) {
			repo, prs := newPullStack(t, user2, nil)
			n := &retargetRecorder{}
			notify_service.RegisterNotifier(n)
			defer notify_service.UnregisterNotifier(n)
			_, err := mergeStack(t, user2, repo, prs[1], repo_model.MergeStyleMerge, stackHeads(t, repo, prs[:2]...))
			require.NoError(t, err)
			// the second one is retargeted by the merge, the third one as a child of the last merged
			assert.Equal(t, map[int64]string{prs[1].Index: "a", prs[2].Index: "b"}, n.get())
		})

		t.Run("SameHeadOnBase", func(t *testing.T) {
			repo, prs := newPullStack(t, user2, nil)
			// b already has a pull request to main: the retarget would be refused, so nothing is merged
			stackPull(t, user2, repo, "main", "b")
			merged, err := mergeStack(t, user2, repo, prs[1], repo_model.MergeStyleMerge, stackHeads(t, repo, prs[:2]...))
			assert.Empty(t, merged)
			assert.True(t, issues_model.IsErrPullRequestAlreadyExists(requireStackMergeError(t, err, prs[1].Index)))
			assertMerged(t, false, prs...)
		})

		t.Run("BaseIsHead", func(t *testing.T) {
			repo := forgery.CreateRepository(t, user2, &forgery.CreateRepositoryOptions{
				Files: forgery.MapFS{"README.md": forgery.MapFile("stack")},
			})
			stackCommit(t, user2, repo, "main", "a", "create", "a.txt", "a")
			stackCommit(t, user2, repo, "main", "b", "create", "b.txt", "b")
			// two pull requests pointing at each other
			p1 := stackPull(t, user2, repo, "b", "a")
			p2 := stackPull(t, user2, repo, "a", "b")
			merged, err := mergeStack(t, user2, repo, p1, repo_model.MergeStyleMerge, stackHeads(t, repo, p2, p1))
			assert.Empty(t, merged)
			require.Error(t, requireStackMergeError(t, err, p1.Index))
			assertMerged(t, false, p1, p2)
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

// forcePushBranch points branch at a new commit made from main with the given file and queues the checks of the pull
// requests like the push hook does, synchronously.
func forcePushBranch(t *testing.T, user *user_model.User, repo *repo_model.Repository, branch, tmpBranch, path, content string) {
	t.Helper()
	gitRepo, err := gitrepo.OpenRepository(t.Context(), repo)
	require.NoError(t, err)
	defer gitRepo.Close()
	oldID, err := gitRepo.GetBranchCommitID(branch)
	require.NoError(t, err)
	stackCommit(t, user, repo, "main", tmpBranch, "create", path, content)
	newID, err := gitRepo.GetBranchCommitID(tmpBranch)
	require.NoError(t, err)
	_, _, err = git.NewCommand(t.Context(), "update-ref").AddDynamicArguments("refs/heads/"+branch, newID).RunStdString(&git.RunOpts{Dir: repo.RepoPath()})
	require.NoError(t, err)
	pull_service.TestPullRequest(t.Context(), user, repo.ID, time.Now().UnixNano(), branch, true, oldID, newID)
}

func TestPullStackStaleReviews(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		isStale := func(r *issues_model.Review) bool {
			return unittest.AssertExistsAndLoadBean(t, &issues_model.Review{ID: r.ID}).Stale
		}
		// main <- a (a.txt, evil.txt) <- b (b.txt), approved by an approver of main
		setup := func(t *testing.T, dismissOnA bool) (*repo_model.Repository, []*issues_model.PullRequest, *user_model.User, *issues_model.Review) {
			approver := forgery.CreateUser(t, nil)
			repo := forgery.CreateRepository(t, user2, &forgery.CreateRepositoryOptions{
				Files:         forgery.MapFS{"README.md": forgery.MapFile("stack")},
				Collaborators: map[*user_model.User]perm.AccessMode{approver: perm.AccessModeWrite},
			})
			stackCommit(t, user2, repo, "main", "a", "create", "a.txt", "a")
			stackCommit(t, user2, repo, "a", "a", "create", "evil.txt", "evil")
			stackCommit(t, user2, repo, "a", "b", "create", "b.txt", "b")
			a := stackPull(t, user2, repo, "main", "a")
			b := stackPull(t, user2, repo, "a", "b")
			require.NoError(t, git_model.UpdateProtectBranch(db.DefaultContext, repo, &git_model.ProtectedBranch{
				RepoID:                   repo.ID,
				RuleName:                 "main",
				RequiredApprovals:        1,
				EnableApprovalsWhitelist: true,
				IgnoreStaleApprovals:     true,
			}, git_model.WhitelistOptions{ApprovalsUserIDs: []int64{approver.ID}}))
			if dismissOnA {
				require.NoError(t, git_model.UpdateProtectBranch(db.DefaultContext, repo, &git_model.ProtectedBranch{
					RepoID:                repo.ID,
					RuleName:              "a",
					DismissStaleApprovals: true,
				}, git_model.WhitelistOptions{}))
			}
			waitChecked(t, a, b)
			review := approvePull(t, repo, b, approver)
			assert.False(t, isStale(review))
			return repo, []*issues_model.PullRequest{a, b}, approver, review
		}

		t.Run("BaseForcePushed", func(t *testing.T) {
			repo, prs, approver, review := setup(t, false)
			forcePushBranch(t, user2, repo, "a", "a-new", "a.txt", "a")
			assert.True(t, isStale(review))
			assert.False(t, unittest.AssertExistsAndLoadBean(t, &issues_model.Review{ID: review.ID}).Dismissed)

			// a is approved again at its new head, b only has the stale approval
			waitChecked(t, prs...)
			approvePull(t, repo, prs[0], approver)
			merged, err := mergeStack(t, user2, repo, prs[1], repo_model.MergeStyleMerge, stackHeads(t, repo, prs...))
			assert.Empty(t, merged)
			innerErr := requireStackMergeError(t, err, prs[1].Index)
			assert.True(t, models.IsErrDisallowedToMerge(innerErr), "%v", innerErr)
			assertMerged(t, false, prs...)
		})

		t.Run("BaseForcePushedDismiss", func(t *testing.T) {
			repo, _, _, review := setup(t, true)
			forcePushBranch(t, user2, repo, "a", "a-new", "a.txt", "a")
			r := unittest.AssertExistsAndLoadBean(t, &issues_model.Review{ID: review.ID})
			assert.True(t, r.Stale)
			assert.True(t, r.Dismissed)
		})

		t.Run("BaseMovedForward", func(t *testing.T) {
			repo, _, _, review := setup(t, false)
			gitRepo, err := gitrepo.OpenRepository(t.Context(), repo)
			require.NoError(t, err)
			defer gitRepo.Close()
			oldID, err := gitRepo.GetBranchCommitID("a")
			require.NoError(t, err)
			stackCommit(t, user2, repo, "a", "a", "create", "more.txt", "more")
			newID, err := gitRepo.GetBranchCommitID("a")
			require.NoError(t, err)
			pull_service.TestPullRequest(t.Context(), user2, repo.ID, time.Now().UnixNano(), "a", true, oldID, newID)
			r := unittest.AssertExistsAndLoadBean(t, &issues_model.Review{ID: review.ID})
			assert.False(t, r.Stale)
			assert.False(t, r.Dismissed)
		})

		t.Run("Retarget", func(t *testing.T) {
			repo, prs, _, review := setup(t, false)
			b := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: prs[1].ID})
			require.NoError(t, b.LoadIssue(db.DefaultContext))
			b.Issue.Repo = repo
			// main lacks the commits of a: the pull request now contributes them
			require.NoError(t, pull_service.ChangeTargetBranch(db.DefaultContext, b, user2, "main"))
			assert.True(t, isStale(review))
		})

		t.Run("MergedParentKeepsApprovals", func(t *testing.T) {
			reviewer := forgery.CreateUser(t, nil)
			repo, prs := newPullStack(t, user2, map[*user_model.User]perm.AccessMode{reviewer: perm.AccessModeWrite})
			review := approvePull(t, repo, prs[1], reviewer)
			// a lands by a merge commit and b is retargeted to main: its merge base is still the head of a
			merged, err := mergeStack(t, user2, repo, prs[0], repo_model.MergeStyleMerge, stackHeads(t, repo, prs[0]))
			require.NoError(t, err)
			assert.Equal(t, indexes(prs[0]), merged)
			assert.Equal(t, "main", unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: prs[1].ID}).BaseBranch)
			assert.False(t, isStale(review))
		})

		t.Run("SharedCommitsMergedFirst", func(t *testing.T) {
			reviewer := forgery.CreateUser(t, nil)
			repo := forgery.CreateRepository(t, user2, &forgery.CreateRepositoryOptions{
				Files:         forgery.MapFS{"README.md": forgery.MapFile("stack")},
				Collaborators: map[*user_model.User]perm.AccessMode{reviewer: perm.AccessModeWrite},
			})
			// x is built on y, both target main
			stackCommit(t, user2, repo, "main", "y", "create", "y.txt", "y")
			stackCommit(t, user2, repo, "y", "x", "create", "x.txt", "x")
			y := stackPull(t, user2, repo, "main", "y")
			x := stackPull(t, user2, repo, "main", "x")
			waitChecked(t, y, x)
			review := approvePull(t, repo, x, reviewer)

			gitRepo, err := gitrepo.OpenRepository(t.Context(), repo)
			require.NoError(t, err)
			defer gitRepo.Close()
			oldID, err := gitRepo.GetBranchCommitID("main")
			require.NoError(t, err)
			merged, err := mergeStack(t, user2, repo, y, repo_model.MergeStyleMerge, stackHeads(t, repo, y))
			require.NoError(t, err)
			assert.Equal(t, indexes(y), merged)
			newID, err := gitRepo.GetBranchCommitID("main")
			require.NoError(t, err)
			// the merge base of x moved forward to the head of y: x only lost commits
			pull_service.TestPullRequest(t.Context(), user2, repo.ID, time.Now().UnixNano(), "main", true, oldID, newID)
			assert.False(t, isStale(review))
		})
	})
}
