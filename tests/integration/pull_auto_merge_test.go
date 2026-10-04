// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"testing"
	"time"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	"forgejo.org/models/perm"
	pull_model "forgejo.org/models/pull"
	unit_model "forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	"forgejo.org/modules/gitrepo"
	"forgejo.org/modules/timeutil"
	"forgejo.org/services/automerge"
	app_context "forgejo.org/services/context"
	"forgejo.org/services/forms"
	pull_service "forgejo.org/services/pull"
	repo_service "forgejo.org/services/repository"
	files_service "forgejo.org/services/repository/files"
	"forgejo.org/tests"
	"forgejo.org/tests/forgery"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPullRemoveAutomerge(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		repo := forgery.CreateRepository(t, nil, &forgery.CreateRepositoryOptions{
			Files: forgery.FilesInit{},
		})
		forgery.EnableRepoUnits(t, repo, unit_model.TypeCode, unit_model.TypePullRequests)

		owner := repo.Owner
		ownerSession := loginUser(t, owner.Name)

		dstPath := t.TempDir()
		cloneURL, _ := url.Parse(fmt.Sprintf("%s%s.git", u.String(), repo.FullName()))
		cloneURL.User = url.UserPassword(owner.Name, userPassword)
		require.NoError(t, git.CloneWithArgs(t.Context(), nil, cloneURL.String(), dstPath, git.CloneRepoOptions{}))
		doGitSetRemoteURL(dstPath, "origin", cloneURL)(t)

		require.NoError(t, git.NewCommand(t.Context(), "switch", "-c", "new-fun-fact").Run(&git.RunOpts{Dir: dstPath}))

		require.NoError(t, os.WriteFile(path.Join(dstPath, "README.md"), []byte("The house of representative already had that in 1937."), 0o600))
		require.NoError(t, git.AddChanges(dstPath, true))
		require.NoError(t, git.CommitChanges(dstPath, git.CommitChangesOptions{
			Committer: &git.Signature{
				Email: "user2@example.com",
				Name:  "user2",
				When:  time.Now(),
			},
			Author: &git.Signature{
				Email: "user2@example.com",
				Name:  "user2",
				When:  time.Now(),
			},
			Message: "Update funfact.",
		}))

		require.NoError(t, git.NewCommand(t.Context(), "push", "origin", "HEAD:refs/for/main", "-o", "topic=new-fun-fact").Run(&git.RunOpts{Dir: dstPath}))

		// Create a protected branch rule for automerge.
		ownerSession.MakeRequest(t, NewRequestWithValues(t, "POST", fmt.Sprintf("/%s/settings/branches/edit", repo.FullName()), map[string]string{
			"rule_name":          "main",
			"required_approvals": "1",
		}), http.StatusSeeOther)

		// Start a automerge for new pull request.
		ownerSession.MakeRequest(t, NewRequestWithValues(t, "POST", fmt.Sprintf("/%s/pulls/1/merge", repo.FullName()), map[string]string{
			"merge_message_field":       "I love automation when it works",
			"do":                        "merge",
			"merge_when_checks_succeed": "true",
		}), http.StatusOK)

		t.Run("No permission", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()

			otherUser := forgery.CreateUser(t, nil)
			otherSession := loginUser(t, otherUser.Name)

			otherSession.MakeRequest(t, NewRequestWithValues(t, "POST", fmt.Sprintf("/%s/pulls/1/cancel_auto_merge", repo.FullName()), nil), http.StatusSeeOther)

			flashCookie := otherSession.GetCookie(app_context.CookieNameFlash)
			assert.NotNil(t, flashCookie)
			assert.Equal(t, "error%3DYou%2Bdo%2Bnot%2Bhave%2Bpermission%2Bto%2Bcancel%2Bthis%2Bpull%2Brequest%2527s%2Bauto%2Bmerge.", flashCookie.Value)
		})

		t.Run("Normal", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()

			ownerSession.MakeRequest(t, NewRequestWithValues(t, "POST", fmt.Sprintf("/%s/pulls/1/cancel_auto_merge", repo.FullName()), nil), http.StatusSeeOther)

			flashCookie := ownerSession.GetCookie(app_context.CookieNameFlash)
			assert.NotNil(t, flashCookie)
			assert.Equal(t, "success%3DThe%2Bauto%2Bmerge%2Bwas%2Bcanceled%2Bfor%2Bthis%2Bpull%2Brequest.", flashCookie.Value)
		})
	})
}

func TestPullAutoMergeFromFork(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, giteaURL *url.URL) {
		baseRepo := forgery.CreateRepository(t, nil, &forgery.CreateRepositoryOptions{
			Files: forgery.FilesInit{}, // ensure an initial commit is present
		})

		forkUser := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		forkRepo, err := repo_service.ForkRepositoryAndUpdates(t.Context(), forkUser, forkUser, repo_service.ForkRepoOptions{
			BaseRepo:    baseRepo,
			Name:        "repo-pr-update",
			Description: "desc",
		})
		require.NoError(t, err)
		assert.NotNil(t, forkRepo)

		_, err = files_service.ChangeRepoFiles(git.DefaultContext, forkRepo, forkUser, &files_service.ChangeRepoFilesOptions{
			Files: []*files_service.ChangeRepoFile{
				{
					Operation:     "create",
					TreePath:      "File_B",
					ContentReader: strings.NewReader("File B"),
				},
			},
			Message:   "Add File on PR branch",
			OldBranch: "main",
			NewBranch: "pull-request",
			Author: &files_service.IdentityOptions{
				Name:  forkUser.Name,
				Email: forkUser.Email,
			},
			Committer: &files_service.IdentityOptions{
				Name:  forkUser.Name,
				Email: forkUser.Email,
			},
			Dates: &files_service.CommitDateOptions{
				Author:    time.Now(),
				Committer: time.Now(),
			},
		})
		require.NoError(t, err)

		// Create a pull request to merge fork into base...
		pullIssue := &issues_model.Issue{
			RepoID:   baseRepo.ID,
			Title:    "Pull Fork into Base",
			PosterID: forkUser.ID,
			Poster:   forkUser,
			IsPull:   true,
		}
		pullRequest := &issues_model.PullRequest{
			BaseRepo:   baseRepo,
			BaseRepoID: baseRepo.ID,
			BaseBranch: "main",
			HeadRepo:   forkRepo,
			HeadRepoID: forkRepo.ID,
			HeadBranch: "pull-request",
			Type:       issues_model.PullRequestGitea,
		}
		err = pull_service.NewPullRequest(git.DefaultContext, baseRepo, pullIssue, nil, nil, pullRequest, nil)
		require.NoError(t, err)

		// Attempt incorrect access: via the API, try to merge when you're not a writer into the base repo:
		badSession := loginUser(t, forkRepo.OwnerName)
		badToken := getTokenForLoggedInUser(t, badSession, auth_model.AccessTokenScopeWriteRepository)
		req := NewRequestWithJSON(t, "POST", fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d/merge", baseRepo.OwnerName, baseRepo.Name, pullIssue.Index),
			&forms.MergePullRequestForm{
				Do:                     "squash",
				MergeWhenChecksSucceed: true, // automerge
			}).
			AddTokenAuth(badToken)
		MakeRequest(t, req, http.StatusMethodNotAllowed) // (odd status code for this case; seems like it should be a 403)

		// Owner of the base repo will trigger an auto-merge:
		session := loginUser(t, baseRepo.OwnerName)
		token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)
		req = NewRequestWithJSON(t, "POST", fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d/merge", baseRepo.OwnerName, baseRepo.Name, pullIssue.Index),
			&forms.MergePullRequestForm{
				Do:                     "squash",
				MergeWhenChecksSucceed: true, // automerge
			}).
			AddTokenAuth(token)
		MakeRequest(t, req, http.StatusCreated)

		// An AutoMerge record now exists which should proceed to a correct merge.  Before doing that, we'll modify that
		// record to simulate a situation - if someone was recently permitted to mark a PR for automerge but then had
		// their collaborator access removed, the merge should not proceed.
		am := unittest.AssertExistsAndLoadBean(t, &pull_model.AutoMerge{PullID: pullRequest.ID})
		assert.Equal(t, baseRepo.OwnerID, am.DoerID)
		am.DoerID = forkRepo.OwnerID // change to a user that doesn't have access to write to the base repo
		_, err = db.GetEngine(t.Context()).ID(am.ID).Update(am)
		require.NoError(t, err)

		// Trigger automerge background handler, then check PR is *not* merged because the automerge actor does not have
		// write permission on the repo:
		pullRequest = unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: pullRequest.ID})
		automerge.StartPRCheckAndAutoMerge(t.Context(), pullRequest)
		pullRequest = unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: pullRequest.ID})
		assert.Empty(t, pullRequest.MergedCommitID)

		// Restore the automerge actor to a user who does have write permission:
		am.DoerID = baseRepo.OwnerID
		_, err = db.GetEngine(t.Context()).ID(am.ID).Update(am)
		require.NoError(t, err)

		// Trigger automerge background handler, then check PR is merged:
		pullRequest = unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: pullRequest.ID})
		automerge.StartPRCheckAndAutoMerge(t.Context(), pullRequest)
		pullRequest = unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: pullRequest.ID})
		assert.NotEmpty(t, pullRequest.MergedCommitID)
	})
}

func TestPullScheduledAutoMerge(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		reviewer := forgery.CreateUser(t, nil)
		repo := forgery.CreateRepository(t, nil, &forgery.CreateRepositoryOptions{
			Files:         forgery.FilesInit{},
			Collaborators: map[*user_model.User]perm.AccessMode{reviewer: perm.AccessModeWrite},
		})
		forgery.EnableRepoUnits(t, repo, unit_model.TypeCode, unit_model.TypePullRequests)
		owner := repo.Owner
		session := loginUser(t, owner.Name)
		reviewerSession := loginUser(t, reviewer.Name)
		apiCtx := NewAPITestContext(t, owner.Name, repo.Name, auth_model.AccessTokenScopeWriteRepository)

		session.MakeRequest(t, NewRequestWithValues(t, "POST", fmt.Sprintf("/%s/settings/branches/edit", repo.FullName()), map[string]string{
			"rule_name":                 "main",
			"required_approvals":        "1",
			"block_on_rejected_reviews": "true",
		}), http.StatusSeeOther)

		gitRepo, err := gitrepo.OpenRepository(t.Context(), repo)
		require.NoError(t, err)
		defer gitRepo.Close()

		now := time.Now()
		defer timeutil.MockUnset()
		timeutil.MockSet(now)
		passTime := func() {
			now = now.Add(2 * time.Hour)
			timeutil.MockSet(now)
			require.NoError(t, automerge.StartDueScheduledAutoMerges(t.Context()))
		}

		newPull := func(t *testing.T, branch string) (*issues_model.PullRequest, string) {
			testEditFileToNewBranch(t, session, owner.Name, repo.Name, "main", branch, "README.md", branch)
			testPullCreate(t, session, owner.Name, repo.Name, true, "main", branch, branch)
			pr := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{BaseRepoID: repo.ID, HeadBranch: branch})
			sha, err := gitRepo.GetRefCommitID(pr.GetGitRefName())
			require.NoError(t, err)
			return pr, sha
		}
		review := func(t *testing.T, pr *issues_model.PullRequest, sha, typ string) {
			testSubmitReview(t, reviewerSession, owner.Name, repo.Name, strconv.FormatInt(pr.Index, 10), sha, typ, http.StatusOK)
		}
		schedule := func(t *testing.T, pr *issues_model.PullRequest, expectedCode int) {
			apiCtx.ExpectedCode = expectedCode
			doAPIMergePullRequestForm(t, apiCtx, owner.Name, repo.Name, pr.Index, &forms.MergePullRequestForm{
				Do:                     "merge",
				MergeWhenChecksSucceed: true,
				ScheduledAfter:         now.Add(time.Hour).Format(time.RFC3339),
			})
		}
		isMerged := func(pr *issues_model.PullRequest) bool {
			return unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: pr.ID}).HasMerged
		}

		t.Run("Invalid time", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			pr, _ := newPull(t, "invalid-time")
			apiCtx.ExpectedCode = http.StatusUnprocessableEntity
			for _, form := range []*forms.MergePullRequestForm{
				{Do: "merge", MergeWhenChecksSucceed: true, ScheduledAfter: now.Add(-time.Minute).Format(time.RFC3339)},
				{Do: "merge", MergeWhenChecksSucceed: true, ScheduledAfter: "tomorrow"},
				{Do: "merge", ScheduledAfter: now.Add(time.Hour).Format(time.RFC3339)},
			} {
				doAPIMergePullRequestForm(t, apiCtx, owner.Name, repo.Name, pr.Index, form)
			}
			unittest.AssertNotExistsBean(t, &pull_model.AutoMerge{PullID: pr.ID})
			assert.False(t, isMerged(pr))
		})

		t.Run("Approved merges at the time", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			pr, sha := newPull(t, "approved")
			review(t, pr, sha, "approve")
			schedule(t, pr, http.StatusCreated)
			am := unittest.AssertExistsAndLoadBean(t, &pull_model.AutoMerge{PullID: pr.ID})
			assert.Equal(t, timeutil.TimeStamp(now.Add(time.Hour).Unix()), am.ScheduledAfterUnix)
			assert.Equal(t, sha, am.HeadCommitID)

			automerge.StartPRCheckAndAutoMerge(t.Context(), pr)
			require.NoError(t, automerge.StartDueScheduledAutoMerges(t.Context()))
			assert.False(t, isMerged(pr))

			passTime()
			assert.True(t, isMerged(pr))
			unittest.AssertNotExistsBean(t, &pull_model.AutoMerge{PullID: pr.ID})
		})

		t.Run("Approved after the time", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			pr, sha := newPull(t, "approved-later")
			testPullMergeForm(t, session, http.StatusOK, owner.Name, repo.Name, strconv.FormatInt(pr.Index, 10), optionsPullMerge{
				"do":                        "merge",
				"merge_when_checks_succeed": "true",
				"head_commit_id":            sha,
				"scheduled_after":           now.Add(time.Hour).Format(time.RFC3339),
			})
			passTime()
			assert.False(t, isMerged(pr))

			review(t, pr, sha, "approve")
			assert.True(t, isMerged(pr))
		})

		t.Run("Changes requested", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			pr, sha := newPull(t, "changes-requested")
			review(t, pr, sha, "approve")
			schedule(t, pr, http.StatusCreated)
			review(t, pr, sha, "reject")
			passTime()
			assert.False(t, isMerged(pr))
			unittest.AssertExistsAndLoadBean(t, &pull_model.AutoMerge{PullID: pr.ID})
		})

		t.Run("New push is not merged", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			pr, sha := newPull(t, "pushed")
			review(t, pr, sha, "approve")
			schedule(t, pr, http.StatusCreated)

			testEditFile(t, session, owner.Name, repo.Name, "pushed", "README.md", "unreviewed")
			newSHA, err := gitRepo.GetRefCommitID(pr.GetGitRefName())
			require.NoError(t, err)
			require.NotEqual(t, sha, newSHA)

			passTime()
			automerge.StartPRCheckAndAutoMerge(t.Context(), unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: pr.ID}))
			assert.False(t, isMerged(pr))
			am := unittest.AssertExistsAndLoadBean(t, &pull_model.AutoMerge{PullID: pr.ID})
			assert.Equal(t, sha, am.HeadCommitID)

			// a merge box loaded before the push cannot schedule the new head
			resp := testPullMergeForm(t, session, http.StatusBadRequest, owner.Name, repo.Name, strconv.FormatInt(pr.Index, 10), optionsPullMerge{
				"do":                        "merge",
				"merge_when_checks_succeed": "true",
				"head_commit_id":            sha,
				"scheduled_after":           now.Add(time.Hour).Format(time.RFC3339),
			})
			assert.Contains(t, resp.Body.String(), "The pull request was updated.")
			assert.False(t, isMerged(pr))
			assert.Equal(t, am, unittest.AssertExistsAndLoadBean(t, &pull_model.AutoMerge{PullID: pr.ID}))
		})
	})
}
