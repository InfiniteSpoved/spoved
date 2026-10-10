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
	git_model "forgejo.org/models/git"
	issues_model "forgejo.org/models/issues"
	"forgejo.org/models/perm"
	pull_model "forgejo.org/models/pull"
	repo_model "forgejo.org/models/repo"
	unit_model "forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	"forgejo.org/modules/gitrepo"
	repo_module "forgejo.org/modules/repository"
	api "forgejo.org/modules/structs"
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

		// Trigger automerge background handler, then check PR is *not* merged and the auto merge is cancelled because
		// the automerge actor does not have write permission on the repo:
		pullRequest = unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: pullRequest.ID})
		automerge.StartPRCheckAndAutoMerge(t.Context(), pullRequest)
		pullRequest = unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: pullRequest.ID})
		assert.Empty(t, pullRequest.MergedCommitID)
		unittest.AssertNotExistsBean(t, &pull_model.AutoMerge{PullID: pullRequest.ID})
		unittest.AssertExistsAndLoadBean(t, &issues_model.Comment{IssueID: pullRequest.IssueID, Type: issues_model.CommentTypePRUnScheduledToAutoMerge, PosterID: forkRepo.OwnerID})

		// Schedule it again by a user who does have write permission:
		req = NewRequestWithJSON(t, "POST", fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d/merge", baseRepo.OwnerName, baseRepo.Name, pullIssue.Index),
			&forms.MergePullRequestForm{Do: "squash", MergeWhenChecksSucceed: true}).
			AddTokenAuth(token)
		MakeRequest(t, req, http.StatusCreated)

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

		scheduleWithDeadline := func(t *testing.T, pr *issues_model.PullRequest) {
			apiCtx.ExpectedCode = http.StatusCreated
			doAPIMergePullRequestForm(t, apiCtx, owner.Name, repo.Name, pr.Index, &forms.MergePullRequestForm{
				Do:                     "merge",
				MergeWhenChecksSucceed: true,
				ScheduledAfter:         now.Add(time.Hour).Format(time.RFC3339),
				GiveUpAfter:            now.Add(3 * time.Hour).Format(time.RFC3339),
			})
		}
		isCancelled := func(t *testing.T, pr *issues_model.PullRequest) bool {
			return unittest.GetCount(t, &issues_model.Comment{IssueID: pr.IssueID, Type: issues_model.CommentTypePRUnScheduledToAutoMerge}) > 0
		}

		t.Run("Invalid give up time", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			pr, _ := newPull(t, "invalid-give-up")
			apiCtx.ExpectedCode = http.StatusUnprocessableEntity
			after := now.Add(time.Hour)
			for _, giveUp := range []string{after.Add(-time.Minute).Format(time.RFC3339), after.Format(time.RFC3339), "later"} {
				doAPIMergePullRequestForm(t, apiCtx, owner.Name, repo.Name, pr.Index, &forms.MergePullRequestForm{
					Do: "merge", MergeWhenChecksSucceed: true, ScheduledAfter: after.Format(time.RFC3339), GiveUpAfter: giveUp,
				})
			}
			doAPIMergePullRequestForm(t, apiCtx, owner.Name, repo.Name, pr.Index, &forms.MergePullRequestForm{
				Do: "merge", MergeWhenChecksSucceed: true, GiveUpAfter: after.Format(time.RFC3339),
			})
			unittest.AssertNotExistsBean(t, &pull_model.AutoMerge{PullID: pr.ID})
		})

		t.Run("Gives up at the deadline", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			pr, sha := newPull(t, "gives-up")
			testPullMergeForm(t, session, http.StatusOK, owner.Name, repo.Name, strconv.FormatInt(pr.Index, 10), optionsPullMerge{
				"do":                        "merge",
				"merge_when_checks_succeed": "true",
				"head_commit_id":            sha,
				"scheduled_after":           now.Add(time.Hour).Format(time.RFC3339),
				"give_up_after":             now.Add(3 * time.Hour).Format(time.RFC3339),
			})
			am := unittest.AssertExistsAndLoadBean(t, &pull_model.AutoMerge{PullID: pr.ID})
			assert.Equal(t, timeutil.TimeStamp(now.Add(3*time.Hour).Unix()), am.GiveUpAfterUnix)
			resp := session.MakeRequest(t, NewRequest(t, "GET", fmt.Sprintf("/%s/pulls/%d", repo.FullName(), pr.Index)), http.StatusOK)
			assert.Contains(t, resp.Body.String(), "It is canceled if it has not merged by")

			// started at its time, then waiting for an approval
			passTime()
			am = unittest.AssertExistsAndLoadBean(t, &pull_model.AutoMerge{PullID: pr.ID})
			assert.Zero(t, am.ScheduledAfterUnix)
			assert.False(t, isCancelled(t, pr))

			// an approval after the deadline does not merge, even before the cron cancels it
			now = now.Add(2 * time.Hour)
			timeutil.MockSet(now)
			review(t, pr, sha, "approve")
			assert.False(t, isMerged(pr))

			require.NoError(t, automerge.StartDueScheduledAutoMerges(t.Context()))
			unittest.AssertNotExistsBean(t, &pull_model.AutoMerge{PullID: pr.ID})
			assert.True(t, isCancelled(t, pr))
			assert.False(t, isMerged(pr))
		})

		t.Run("Merged before the deadline", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			pr, sha := newPull(t, "merged-before-deadline")
			review(t, pr, sha, "approve")
			scheduleWithDeadline(t, pr)
			passTime()
			assert.True(t, isMerged(pr))
			passTime()
			assert.False(t, isCancelled(t, pr))
		})

		t.Run("Time and deadline both passed", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			pr, sha := newPull(t, "both-passed")
			review(t, pr, sha, "approve")
			scheduleWithDeadline(t, pr)
			now = now.Add(4 * time.Hour)
			timeutil.MockSet(now)
			require.NoError(t, automerge.StartDueScheduledAutoMerges(t.Context()))
			assert.False(t, isMerged(pr))
			assert.True(t, isCancelled(t, pr))
		})
	})
}

func TestAPIPullScheduledAfter(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		reader := forgery.CreateUser(t, nil)
		outsider := forgery.CreateUser(t, nil)
		repo := forgery.CreateRepository(t, nil, &forgery.CreateRepositoryOptions{
			Files:         forgery.FilesInit{},
			IsPrivate:     true,
			Collaborators: map[*user_model.User]perm.AccessMode{reader: perm.AccessModeRead},
		})
		forgery.EnableRepoUnits(t, repo, unit_model.TypeCode, unit_model.TypePullRequests)
		owner := repo.Owner
		session := loginUser(t, owner.Name)
		apiCtx := NewAPITestContext(t, owner.Name, repo.Name, auth_model.AccessTokenScopeWriteRepository)
		readerToken := getUserToken(t, reader.Name, auth_model.AccessTokenScopeReadRepository)
		outsiderToken := getUserToken(t, outsider.Name, auth_model.AccessTokenScopeReadRepository)

		now := time.Now()
		defer timeutil.MockUnset()
		timeutil.MockSet(now)
		scheduledAfter := now.Add(time.Hour).Truncate(time.Second)
		giveUpAfter := now.Add(3 * time.Hour).Truncate(time.Second)

		newPull := func(t *testing.T, branch string) *issues_model.PullRequest {
			testEditFileToNewBranch(t, session, owner.Name, repo.Name, "main", branch, "README.md", branch)
			testPullCreate(t, session, owner.Name, repo.Name, true, "main", branch, branch)
			return unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{BaseRepoID: repo.ID, HeadBranch: branch})
		}
		getPull := func(t *testing.T, pr *issues_model.PullRequest, token string, expectedCode int) *api.PullRequest {
			t.Helper()
			req := NewRequestf(t, "GET", "/api/v1/repos/%s/pulls/%d", repo.FullName(), pr.Index).AddTokenAuth(token)
			resp := MakeRequest(t, req, expectedCode)
			if expectedCode != http.StatusOK {
				return nil
			}
			var apiPR api.PullRequest
			DecodeJSON(t, resp, &apiPR)
			return &apiPR
		}

		scheduled := newPull(t, "scheduled")
		unscheduled := newPull(t, "unscheduled")
		apiCtx.ExpectedCode = http.StatusCreated
		doAPIMergePullRequestForm(t, apiCtx, owner.Name, repo.Name, scheduled.Index, &forms.MergePullRequestForm{
			Do:                     "merge",
			MergeWhenChecksSucceed: true,
			ScheduledAfter:         scheduledAfter.Format(time.RFC3339),
			GiveUpAfter:            giveUpAfter.Format(time.RFC3339),
		})

		t.Run("Single", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			apiPR := getPull(t, scheduled, apiCtx.Token, http.StatusOK)
			require.NotNil(t, apiPR.ScheduledAfter)
			assert.Equal(t, scheduledAfter.Unix(), apiPR.ScheduledAfter.Unix())
			require.NotNil(t, apiPR.GiveUpAfter)
			assert.Equal(t, giveUpAfter.Unix(), apiPR.GiveUpAfter.Unix())
			apiPR = getPull(t, unscheduled, apiCtx.Token, http.StatusOK)
			assert.Nil(t, apiPR.ScheduledAfter)
			assert.Nil(t, apiPR.GiveUpAfter)
		})

		t.Run("List", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			req := NewRequestf(t, "GET", "/api/v1/repos/%s/pulls?state=open", repo.FullName()).AddTokenAuth(apiCtx.Token)
			var apiPRs []*api.PullRequest
			DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &apiPRs)
			require.Len(t, apiPRs, 2)
			for _, apiPR := range apiPRs {
				if apiPR.ID == scheduled.ID {
					require.NotNil(t, apiPR.ScheduledAfter)
					assert.Equal(t, scheduledAfter.Unix(), apiPR.ScheduledAfter.Unix())
					require.NotNil(t, apiPR.GiveUpAfter)
					assert.Equal(t, giveUpAfter.Unix(), apiPR.GiveUpAfter.Unix())
				} else {
					assert.Nil(t, apiPR.ScheduledAfter)
					assert.Nil(t, apiPR.GiveUpAfter)
				}
			}
		})

		t.Run("Readers only", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			apiPR := getPull(t, scheduled, readerToken, http.StatusOK)
			require.NotNil(t, apiPR.ScheduledAfter)
			assert.Equal(t, scheduledAfter.Unix(), apiPR.ScheduledAfter.Unix())
			require.NotNil(t, apiPR.GiveUpAfter)
			assert.Equal(t, giveUpAfter.Unix(), apiPR.GiveUpAfter.Unix())
			getPull(t, scheduled, outsiderToken, http.StatusNotFound)
		})

		t.Run("Cancelled", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			doAPIMergePullRequestForm(t, apiCtx, owner.Name, repo.Name, unscheduled.Index, &forms.MergePullRequestForm{
				Do:                     "merge",
				MergeWhenChecksSucceed: true,
				ScheduledAfter:         scheduledAfter.Format(time.RFC3339),
			})
			require.NotNil(t, getPull(t, unscheduled, apiCtx.Token, http.StatusOK).ScheduledAfter)
			req := NewRequestf(t, "DELETE", "/api/v1/repos/%s/pulls/%d/merge", repo.FullName(), unscheduled.Index).AddTokenAuth(apiCtx.Token)
			MakeRequest(t, req, http.StatusNoContent)
			assert.Nil(t, getPull(t, unscheduled, apiCtx.Token, http.StatusOK).ScheduledAfter)
		})

		t.Run("Merged", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			timeutil.MockSet(now.Add(2 * time.Hour))
			require.NoError(t, automerge.StartDueScheduledAutoMerges(t.Context()))
			apiPR := getPull(t, scheduled, apiCtx.Token, http.StatusOK)
			assert.True(t, apiPR.HasMerged)
			assert.Nil(t, apiPR.ScheduledAfter)
			assert.Nil(t, apiPR.GiveUpAfter)
		})
	})
}

func TestPullAutoMergePermissionLoss(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		owner := forgery.CreateUser(t, nil)
		org := forgery.CreateOrganisation(t, owner)
		reviewer := forgery.CreateUser(t, nil)
		repo := forgery.CreateRepository(t, org.AsUser(), &forgery.CreateRepositoryOptions{
			Files:         forgery.FilesInit{},
			Collaborators: map[*user_model.User]perm.AccessMode{reviewer: perm.AccessModeWrite},
		})
		forgery.EnableRepoUnits(t, repo, unit_model.TypeCode, unit_model.TypePullRequests)
		session := loginUser(t, owner.Name)
		reviewerSession := loginUser(t, reviewer.Name)
		statusCtx := APITestContext{Session: session, Token: getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository), Username: org.Name, Reponame: repo.Name}

		session.MakeRequest(t, NewRequestWithValues(t, "POST", fmt.Sprintf("/%s/settings/branches/edit", repo.FullName()), map[string]string{
			"rule_name":             "main",
			"required_approvals":    "1",
			"enable_status_check":   "true",
			"status_check_contexts": "ci",
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

		pulls := 0
		newPull := func(t *testing.T) (*issues_model.PullRequest, string) {
			pulls++
			branch := fmt.Sprintf("pull-%d", pulls)
			testEditFileToNewBranch(t, session, org.Name, repo.Name, "main", branch, "README.md", branch)
			testPullCreate(t, session, org.Name, repo.Name, true, "main", branch, branch)
			pr := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{BaseRepoID: repo.ID, HeadBranch: branch})
			sha, err := gitRepo.GetRefCommitID(pr.GetGitRefName())
			require.NoError(t, err)
			return pr, sha
		}
		approve := func(t *testing.T, pr *issues_model.PullRequest, sha string) {
			testSubmitReview(t, reviewerSession, org.Name, repo.Name, strconv.FormatInt(pr.Index, 10), sha, "approve", http.StatusOK)
		}
		ciPasses := func(t *testing.T, sha string) {
			doAPICreateCommitStatus(statusCtx, sha, api.CreateStatusOption{State: api.CommitStatusSuccess, Context: "ci"})(t)
		}
		newScheduler := func(t *testing.T) *user_model.User {
			scheduler := forgery.CreateUser(t, nil)
			require.NoError(t, repo_module.AddCollaborator(t.Context(), repo, scheduler))
			return scheduler
		}
		schedule := func(t *testing.T, scheduler *user_model.User, pr *issues_model.PullRequest, at bool) {
			apiCtx := NewAPITestContext(t, scheduler.Name, repo.Name, auth_model.AccessTokenScopeWriteRepository)
			apiCtx.ExpectedCode = http.StatusCreated
			form := &forms.MergePullRequestForm{Do: "merge", MergeWhenChecksSucceed: true}
			if at {
				form.ScheduledAfter = now.Add(time.Hour).Format(time.RFC3339)
			}
			doAPIMergePullRequestForm(t, apiCtx, org.Name, repo.Name, pr.Index, form)
			unittest.AssertExistsAndLoadBean(t, &pull_model.AutoMerge{PullID: pr.ID, DoerID: scheduler.ID})
		}
		isMerged := func(pr *issues_model.PullRequest) bool {
			return unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: pr.ID}).HasMerged
		}
		assertCancelled := func(t *testing.T, pr *issues_model.PullRequest, doerID int64) {
			t.Helper()
			unittest.AssertNotExistsBean(t, &pull_model.AutoMerge{PullID: pr.ID})
			unittest.AssertExistsAndLoadBean(t, &issues_model.Comment{IssueID: pr.IssueID, Type: issues_model.CommentTypePRUnScheduledToAutoMerge, PosterID: doerID})
			assert.False(t, isMerged(pr))
		}

		t.Run("Permission kept merges", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			scheduler := newScheduler(t)
			pr, sha := newPull(t)
			approve(t, pr, sha)
			ciPasses(t, sha)
			schedule(t, scheduler, pr, true)
			passTime()
			assert.True(t, isMerged(pr))
		})

		for _, c := range []struct {
			name    string
			lose    func(t *testing.T, scheduler *user_model.User)
			restore func(t *testing.T)
		}{
			{name: "lost removed", lose: func(t *testing.T, scheduler *user_model.User) {
				require.NoError(t, repo_module.DeleteCollaboration(t.Context(), repo, scheduler.ID))
			}},
			{name: "lost read", lose: func(t *testing.T, scheduler *user_model.User) {
				require.NoError(t, repo_module.ChangeCollaborationAccessMode(t.Context(), repo, scheduler.ID, perm.AccessModeRead))
			}},
			{name: "lost deactivated", lose: func(t *testing.T, scheduler *user_model.User) {
				require.NoError(t, user_model.UpdateUserCols(t.Context(), &user_model.User{ID: scheduler.ID, IsActive: false}, "is_active"))
			}},
			{name: "lost prohibited", lose: func(t *testing.T, scheduler *user_model.User) {
				require.NoError(t, user_model.UpdateUserCols(t.Context(), &user_model.User{ID: scheduler.ID, ProhibitLogin: true}, "prohibit_login"))
			}},
			{name: "lost whitelist", lose: func(t *testing.T, scheduler *user_model.User) {
				pb, err := git_model.GetFirstMatchProtectedBranchRule(t.Context(), repo.ID, "main")
				require.NoError(t, err)
				pb.EnableMergeWhitelist = true
				pb.MergeWhitelistUserIDs = []int64{reviewer.ID}
				_, err = db.GetEngine(t.Context()).ID(pb.ID).Cols("enable_merge_whitelist", "merge_whitelist_user_i_ds").Update(pb)
				require.NoError(t, err)
			}, restore: func(t *testing.T) {
				_, err := db.GetEngine(t.Context()).Table("protected_branch").Where("repo_id = ?", repo.ID).Update(map[string]any{"enable_merge_whitelist": false})
				require.NoError(t, err)
			}},
			{name: "lost two factor", lose: func(t *testing.T, scheduler *user_model.User) {
				require.NoError(t, user_model.UpdateUserCols(t.Context(), &user_model.User{ID: org.ID, RequireTwoFactor: true}, "require_two_factor"))
			}, restore: func(t *testing.T) {
				require.NoError(t, user_model.UpdateUserCols(t.Context(), &user_model.User{ID: org.ID, RequireTwoFactor: false}, "require_two_factor"))
			}},
		} {
			t.Run(c.name, func(t *testing.T) {
				defer tests.PrintCurrentTest(t)()

				t.Run("Scheduled", func(t *testing.T) {
					scheduler := newScheduler(t)
					pr, sha := newPull(t)
					approve(t, pr, sha)
					ciPasses(t, sha)
					schedule(t, scheduler, pr, true)
					c.lose(t, scheduler)
					if c.restore != nil {
						defer c.restore(t)
					}
					passTime()
					assertCancelled(t, pr, scheduler.ID)
				})

				t.Run("Plain", func(t *testing.T) {
					scheduler := newScheduler(t)
					pr, sha := newPull(t)
					ciPasses(t, sha)
					schedule(t, scheduler, pr, false)
					c.lose(t, scheduler)
					if c.restore != nil {
						defer c.restore(t)
					}
					approve(t, pr, sha)
					assertCancelled(t, pr, scheduler.ID)
				})

				t.Run("Checks pending", func(t *testing.T) {
					scheduler := newScheduler(t)
					pr, sha := newPull(t)
					approve(t, pr, sha)
					schedule(t, scheduler, pr, true)
					c.lose(t, scheduler)
					if c.restore != nil {
						defer c.restore(t)
					}
					passTime()
					assertCancelled(t, pr, scheduler.ID)
				})
			})
		}

		t.Run("Scheduler gone", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			scheduler := newScheduler(t)
			pr, sha := newPull(t)
			approve(t, pr, sha)
			ciPasses(t, sha)
			schedule(t, scheduler, pr, true)
			// a purged user stays as a hidden tombstone row, see services/user DeleteUser
			_, err := db.GetEngine(t.Context()).ID(scheduler.ID).Cols("trashed_unix").Update(&user_model.User{TrashedUnix: 1})
			require.NoError(t, err)
			passTime()
			assertCancelled(t, pr, user_model.GhostUserID)
		})
	})
}

func TestPullAutoMergeCancelOnPush(t *testing.T) {
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
			"rule_name":          "main",
			"required_approvals": "1",
			"enable_push":        "all",
		}), http.StatusSeeOther)

		gitRepo, err := gitrepo.OpenRepository(t.Context(), repo)
		require.NoError(t, err)
		defer gitRepo.Close()

		dstPath := t.TempDir()
		cloneURL, _ := url.Parse(fmt.Sprintf("%s%s.git", u.String(), repo.FullName()))
		cloneURL.User = url.UserPassword(owner.Name, userPassword)
		doGitClone(dstPath, cloneURL)(t)

		now := time.Now()
		defer timeutil.MockUnset()
		timeutil.MockSet(now)
		passTime := func() {
			now = now.Add(2 * time.Hour)
			timeutil.MockSet(now)
			require.NoError(t, automerge.StartDueScheduledAutoMerges(t.Context()))
		}

		pulls := 0
		newPull := func(t *testing.T) (*issues_model.PullRequest, string) {
			pulls++
			branch := fmt.Sprintf("pull-%d", pulls)
			testEditFileToNewBranch(t, session, owner.Name, repo.Name, "main", branch, "README.md", branch)
			testPullCreate(t, session, owner.Name, repo.Name, true, "main", branch, branch)
			pr := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{BaseRepoID: repo.ID, HeadBranch: branch})
			sha, err := gitRepo.GetRefCommitID(pr.GetGitRefName())
			require.NoError(t, err)
			return pr, sha
		}
		approve := func(t *testing.T, pr *issues_model.PullRequest, sha string) {
			testSubmitReview(t, reviewerSession, owner.Name, repo.Name, strconv.FormatInt(pr.Index, 10), sha, "approve", http.StatusOK)
		}
		schedule := func(t *testing.T, pr *issues_model.PullRequest, at bool) {
			apiCtx.ExpectedCode = http.StatusCreated
			form := &forms.MergePullRequestForm{Do: "merge", MergeWhenChecksSucceed: true}
			if at {
				form.ScheduledAfter = now.Add(time.Hour).Format(time.RFC3339)
			}
			doAPIMergePullRequestForm(t, apiCtx, owner.Name, repo.Name, pr.Index, form)
			unittest.AssertExistsAndLoadBean(t, &pull_model.AutoMerge{PullID: pr.ID})
		}
		headMoves := func(t *testing.T, pr *issues_model.PullRequest, sha string) {
			t.Helper()
			assert.Eventually(t, func() bool {
				newSHA, err := gitRepo.GetRefCommitID(pr.GetGitRefName())
				return err == nil && newSHA != sha
			}, 10*time.Second, 50*time.Millisecond)
		}
		isMerged := func(pr *issues_model.PullRequest) bool {
			return unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: pr.ID}).HasMerged
		}
		isCancelled := func(t *testing.T, pr *issues_model.PullRequest) bool {
			return unittest.GetCount(t, &issues_model.Comment{IssueID: pr.IssueID, Type: issues_model.CommentTypePRUnScheduledToAutoMerge}) > 0
		}
		assertCancelledBy := func(t *testing.T, pr *issues_model.PullRequest, pusherID int64) {
			t.Helper()
			assert.Eventually(t, func() bool {
				return unittest.GetCount(t, &pull_model.AutoMerge{PullID: pr.ID}) == 0
			}, 10*time.Second, 50*time.Millisecond)
			unittest.AssertExistsAndLoadBean(t, &issues_model.Comment{IssueID: pr.IssueID, Type: issues_model.CommentTypePRUnScheduledToAutoMerge, PosterID: pusherID})
			passTime()
			assert.False(t, isMerged(pr))
		}
		pullsConfig := func(t *testing.T) *repo_model.PullRequestsConfig {
			repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: repo.ID})
			u, err := repo.GetUnit(t.Context(), unit_model.TypePullRequests)
			require.NoError(t, err)
			return u.PullRequestsConfig()
		}

		t.Run("Keeps by default", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			assert.False(t, pullsConfig(t).CancelAutoMergeOnPush)

			pr, sha := newPull(t)
			approve(t, pr, sha)
			schedule(t, pr, true)
			testEditFile(t, session, owner.Name, repo.Name, pr.HeadBranch, "README.md", "unreviewed")
			headMoves(t, pr, sha)
			passTime()
			am := unittest.AssertExistsAndLoadBean(t, &pull_model.AutoMerge{PullID: pr.ID})
			assert.Equal(t, sha, am.HeadCommitID)
			assert.False(t, isCancelled(t, pr))
			assert.False(t, isMerged(pr))
		})

		t.Run("Head moved after validation", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			pr, sha := newPull(t)
			schedule(t, pr, false)
			// move the head branch without the hooks, so refs/pull/N/head still has the reviewed commit
			testEditFileToNewBranch(t, session, owner.Name, repo.Name, pr.HeadBranch, "unreviewed", "README.md", "unreviewed")
			unreviewed, err := gitRepo.GetBranchCommitID("unreviewed")
			require.NoError(t, err)
			_, _, err = git.NewCommand(t.Context(), "update-ref").AddDynamicArguments(git.BranchPrefix+pr.HeadBranch, unreviewed).RunStdString(&git.RunOpts{Dir: repo.RepoPath()})
			require.NoError(t, err)

			approve(t, pr, sha)
			assert.False(t, isMerged(pr))
		})

		t.Run("Setting", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			session.MakeRequest(t, NewRequestWithValues(t, "POST", fmt.Sprintf("/%s/settings/units", repo.FullName()), map[string]string{
				"enable_code": "on", "enable_pulls": "on", "pulls_allow_merge": "on", "pulls_default_merge_style": "merge",
				"cancel_auto_merge_on_push": "on",
			}), http.StatusSeeOther)
			assert.True(t, pullsConfig(t).CancelAutoMergeOnPush)
			resp := session.MakeRequest(t, NewRequest(t, "GET", fmt.Sprintf("/%s/settings/units", repo.FullName())), http.StatusOK)
			NewHTMLParser(t, resp.Body).AssertElement(t, `input[name="cancel_auto_merge_on_push"][checked]`, true)
		})

		t.Run("Cancels scheduled", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			pr, sha := newPull(t)
			approve(t, pr, sha)
			schedule(t, pr, true)
			testEditFile(t, session, owner.Name, repo.Name, pr.HeadBranch, "README.md", "unreviewed")
			assertCancelledBy(t, pr, owner.ID)
		})

		t.Run("Cancels plain", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			pr, sha := newPull(t)
			schedule(t, pr, false)
			testEditFile(t, session, owner.Name, repo.Name, pr.HeadBranch, "README.md", "unreviewed")
			headMoves(t, pr, sha)
			assertCancelledBy(t, pr, owner.ID)
			approve(t, pr, sha)
			assert.False(t, isMerged(pr))
		})

		t.Run("Base push keeps, update branch cancels", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			pr, sha := newPull(t)
			approve(t, pr, sha)
			schedule(t, pr, true)
			_, _, err := git.NewCommand(t.Context(), "fetch", "origin").RunStdString(&git.RunOpts{Dir: dstPath})
			require.NoError(t, err)
			doGitCheckoutBranch(dstPath, "-B", "main", "origin/main")(t)
			doGitAddSomeCommits(dstPath, "main")(t)
			doGitPushTestRepository(dstPath, "origin", "main")(t)
			unittest.AssertExistsAndLoadBean(t, &pull_model.AutoMerge{PullID: pr.ID})

			session.MakeRequest(t, NewRequestf(t, "POST", "/%s/pulls/%d/update", repo.FullName(), pr.Index), http.StatusSeeOther)
			assertCancelledBy(t, pr, owner.ID)
		})

		t.Run("Cancels force push", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			pr, sha := newPull(t)
			approve(t, pr, sha)
			schedule(t, pr, true)
			_, _, err := git.NewCommand(t.Context(), "fetch", "origin").RunStdString(&git.RunOpts{Dir: dstPath})
			require.NoError(t, err)
			doGitCheckoutBranch(dstPath, "-b", pr.HeadBranch, "origin/"+pr.HeadBranch)(t)
			_, _, err = git.NewCommand(t.Context(), "reset", "--hard", "HEAD~1").RunStdString(&git.RunOpts{Dir: dstPath})
			require.NoError(t, err)
			doGitAddSomeCommits(dstPath, pr.HeadBranch)(t)
			doGitPushTestRepository(dstPath, "--force", "origin", pr.HeadBranch)(t)
			assertCancelledBy(t, pr, owner.ID)
		})

		t.Run("Cancels AGit push", func(t *testing.T) {
			defer tests.PrintCurrentTest(t)()
			doGitCheckoutBranch(dstPath, "-b", "agit", "origin/main")(t)
			doGitAddSomeCommits(dstPath, "agit")(t)
			doGitPushTestRepository(dstPath, "origin", "HEAD:refs/for/main", "-o", "topic=agit")(t)
			pr := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{BaseRepoID: repo.ID, HeadBranch: strings.ToLower(owner.Name) + "/agit"})
			schedule(t, pr, false)
			doGitAddSomeCommits(dstPath, "agit")(t)
			doGitPushTestRepository(dstPath, "origin", "HEAD:refs/for/main", "-o", "topic=agit")(t)
			assertCancelledBy(t, pr, owner.ID)
		})
	})
}
