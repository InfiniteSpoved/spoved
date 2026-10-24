// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	issues_model "forgejo.org/models/issues"
	"forgejo.org/models/perm"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	api "forgejo.org/modules/structs"
	commitstatus_service "forgejo.org/services/repository/commitstatus"
	"forgejo.org/tests/forgery"

	"github.com/PuerkitoBio/goquery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stackPage loads the page of pull request pr, which is in repo.
func stackPage(t *testing.T, session *TestSession, repo *repo_model.Repository, pr *issues_model.PullRequest) *HTMLDoc {
	t.Helper()
	resp := session.MakeRequest(t, NewRequestf(t, "GET", "%s/pulls/%d", repo.Link(), pr.Index), http.StatusOK)
	return NewHTMLParser(t, resp.Body)
}

// stackFormHeads returns the "head" fields of the Merge stack dialog of the page.
func stackFormHeads(doc *HTMLDoc) []string {
	var heads []string
	doc.Find(`#merge-stack input[name="head"]`).Each(func(_ int, s *goquery.Selection) {
		heads = append(heads, s.AttrOr("value", ""))
	})
	return heads
}

// postMergeStack posts the Merge stack form for pr and returns the flash message of the page it redirects to.
func postMergeStack(t *testing.T, session *TestSession, repo *repo_model.Repository, pr *issues_model.PullRequest, values url.Values) string {
	t.Helper()
	link := fmt.Sprintf("%s/pulls/%d", repo.Link(), pr.Index)
	resp := session.MakeRequest(t, NewRequestWithURLValues(t, "POST", link+"/merge_stack", values), http.StatusSeeOther)
	assert.Equal(t, link, resp.Header().Get("Location"))
	resp = session.MakeRequest(t, NewRequest(t, "GET", resp.Header().Get("Location")), http.StatusOK)
	return strings.Join(strings.Fields(NewHTMLParser(t, resp.Body).Find(".flash-message").Text()), " ")
}

func stackValues(heads []string, deleteBranches bool) url.Values {
	values := url.Values{"do": {"merge"}, "head": heads}
	if deleteBranches {
		values.Set("delete_branch_after_merge", "on")
	}
	return values
}

func TestPullStackWeb(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
		owner := loginUser(t, "user2")
		reader := loginUser(t, "user4")

		t.Run("Panel", func(t *testing.T) {
			repo, prs := newPullStack(t, user2, map[*user_model.User]perm.AccessMode{user4: perm.AccessModeRead})
			stackCommit(t, user2, repo, "main", "alone", "create", "alone.txt", "alone")
			alone := stackPull(t, user2, repo, "main", "alone")

			for current, pr := range prs {
				doc := stackPage(t, owner, repo, pr)
				rows := doc.Find("details.pull-stack li.pull-stack-row")
				require.Equal(t, 3, rows.Length())
				assert.Contains(t, doc.Find("details.pull-stack summary").Text(), "3 pull requests into main")
				for i, p := range prs {
					row := rows.Eq(i)
					assert.Contains(t, row.Text(), fmt.Sprintf("#%d stack %s", p.Index, p.HeadBranch))
					assert.Equal(t, i == current, row.AttrOr("aria-current", "") == "true")
					assert.Equal(t, i == current, strings.Contains(row.Text(), "(this pull request)"))
					assert.Equal(t, i != current, row.Find("a").Length() > 0)
					assert.Contains(t, row.Text(), "Review pending")
					assert.Contains(t, row.Text(), "No checks")
				}
				assert.Equal(t, 1, doc.Find(`details.pull-stack [aria-current="true"]`).Length())
			}

			doc := stackPage(t, owner, repo, alone)
			assert.Zero(t, doc.Find("details.pull-stack").Length())
			assert.Zero(t, doc.Find("#pull-stack-merge").Length())
		})

		t.Run("Button", func(t *testing.T) {
			repo, prs := newPullStack(t, user2, map[*user_model.User]perm.AccessMode{user4: perm.AccessModeRead})
			heads := stackHeads(t, repo, prs...)

			doc := stackPage(t, owner, repo, prs[2])
			assert.Contains(t, doc.Find("#pull-stack-merge").Text(), fmt.Sprintf("This pull request is stacked on #%d", prs[1].Index))
			assert.Contains(t, doc.Find("#pull-stack-merge").Text(), "into b, not main")
			assert.Equal(t, "Merge stack (3 pull requests) into main", strings.Join(strings.Fields(doc.Find("#pull-stack-merge button").Text()), " "))
			_, disabled := doc.Find("#pull-stack-merge button").Attr("disabled")
			assert.False(t, disabled)
			assert.Equal(t, 3, doc.Find("#merge-stack li").Length())
			assert.Equal(t, []string{
				fmt.Sprintf("%d:%s", prs[0].Index, heads[0].HeadCommitID),
				fmt.Sprintf("%d:%s", prs[1].Index, heads[1].HeadCommitID),
				fmt.Sprintf("%d:%s", prs[2].Index, heads[2].HeadCommitID),
			}, stackFormHeads(doc))
			assert.Equal(t, "merge", doc.Find("#merge-stack-style option[selected]").AttrOr("value", ""))

			// the middle pull request merges the stack up to itself
			doc = stackPage(t, owner, repo, prs[1])
			assert.Contains(t, strings.Join(strings.Fields(doc.Find("#pull-stack-merge button").Text()), " "), "Merge stack (2 pull requests) into main")
			assert.Len(t, stackFormHeads(doc), 2)

			// the bottom has no parent, so nothing is stacked under it
			doc = stackPage(t, owner, repo, prs[0])
			assert.Zero(t, doc.Find("#pull-stack-merge").Length())
			assert.Equal(t, 1, doc.Find("details.pull-stack").Length())

			// a reader sees the notice but can't merge
			doc = stackPage(t, reader, repo, prs[2])
			assert.Equal(t, 1, doc.Find("#pull-stack-merge").Length())
			assert.Zero(t, doc.Find("#pull-stack-merge button").Length())
			assert.Zero(t, doc.Find("#merge-stack").Length())

			// a failing check the rules don't require is a warning only
			require.NoError(t, commitstatus_service.CreateCommitStatus(db.DefaultContext, repo, user2, heads[0].HeadCommitID,
				&git_model.CommitStatus{State: api.CommitStatusFailure, Context: "ci"}))
			doc = stackPage(t, owner, repo, prs[2])
			assert.Contains(t, doc.Find("#pull-stack-merge").Text(), fmt.Sprintf("#%d has failing checks that the rules don't require", prs[0].Index))
			_, disabled = doc.Find("#pull-stack-merge button").Attr("disabled")
			assert.False(t, disabled)
			assert.Equal(t, 1, doc.Find("#merge-stack").Length())
			assert.Contains(t, doc.Find("details.pull-stack li").First().Text(), "Checks failed")

			// a required one blocks the button and names the pull request
			require.NoError(t, git_model.UpdateProtectBranch(db.DefaultContext, repo, &git_model.ProtectedBranch{
				RepoID:              repo.ID,
				RuleName:            "main",
				EnableStatusCheck:   true,
				StatusCheckContexts: []string{"ci"},
			}, git_model.WhitelistOptions{}))
			doc = stackPage(t, owner, repo, prs[2])
			assert.Contains(t, doc.Find("#pull-stack-merge").Text(), fmt.Sprintf("#%d can't be merged yet (required checks failed)", prs[0].Index))
			assert.Equal(t, fmt.Sprintf("%s/pulls/%d", repo.Link(), prs[0].Index), doc.Find("#merge-stack-blocked a").AttrOr("href", ""))
			_, disabled = doc.Find("#pull-stack-merge button").Attr("disabled")
			assert.True(t, disabled)
			assert.Equal(t, "merge-stack-blocked", doc.Find("#pull-stack-merge button").AttrOr("aria-describedby", ""))
			assert.Zero(t, doc.Find("#merge-stack").Length())

			// a pending required one too
			require.NoError(t, commitstatus_service.CreateCommitStatus(db.DefaultContext, repo, user2, heads[0].HeadCommitID,
				&git_model.CommitStatus{State: api.CommitStatusPending, Context: "ci"}))
			doc = stackPage(t, owner, repo, prs[2])
			assert.Contains(t, doc.Find("#pull-stack-merge").Text(), fmt.Sprintf("#%d can't be merged yet (waiting for required checks)", prs[0].Index))
		})

		t.Run("Labels", func(t *testing.T) {
			repo, prs := newPullStack(t, user2, nil)
			doc := stackPage(t, owner, repo, prs[2])
			// the merge button of the box says it merges this pull request only
			assert.Contains(t, doc.Find("script").Text(), "Merge this pull request only")
			assert.Equal(t, "merge-stack-title", doc.Find("#merge-stack").AttrOr("aria-labelledby", ""))
			assert.Equal(t, 1, doc.Find("#merge-stack-title").Length())
			// the list announces its own numbers
			assert.True(t, strings.HasPrefix(strings.TrimSpace(doc.Find("details.pull-stack li.pull-stack-row").First().Text()), "#"))
		})

		t.Run("Branching", func(t *testing.T) {
			repo, prs := newPullStack(t, user2, nil)
			stackCommit(t, user2, repo, "a", "d", "create", "d.txt", "d")
			d := stackPull(t, user2, repo, "a", "d")

			// a stack indents only where it branches
			doc := stackPage(t, owner, repo, prs[0])
			rows := doc.Find("details.pull-stack li.pull-stack-row")
			require.Equal(t, 4, rows.Length())
			indented := []bool{}
			rows.Each(func(_ int, row *goquery.Selection) { indented = append(indented, row.HasClass("is-branch")) })
			assert.Equal(t, []bool{false, true, false, true}, indented) // a, b, c on b, d
			assert.Contains(t, rows.Eq(3).Text(), fmt.Sprintf("also on #%d", prs[0].Index))
			assert.NotContains(t, rows.Eq(1).Text(), "also on")

			// the dialog names the pull request the panel does not show, which moves too
			doc = stackPage(t, owner, repo, prs[2])
			text := strings.Join(strings.Fields(doc.Find("#merge-stack p").Text()), " ")
			assert.Contains(t, text, fmt.Sprintf("#%d stays open and moves onto main.", d.Index))
			assert.Equal(t, fmt.Sprintf("%s/pulls/%d", repo.Link(), d.Index), doc.Find("#merge-stack p a").AttrOr("href", ""))

			flash := postMergeStack(t, owner, repo, prs[2], stackValues(stackFormHeads(doc), false))
			assert.Equal(t, "Merged 3 pull requests into main.", flash)
			assert.Equal(t, "main", unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: d.ID}).BaseBranch)
		})

		t.Run("Stopped", func(t *testing.T) {
			repo := forgery.CreateRepository(t, user2, &forgery.CreateRepositoryOptions{
				Files: forgery.MapFS{"f.txt": forgery.MapFile("one\n")},
			})
			stackCommit(t, user2, repo, "main", "a", "update", "f.txt", "one\ntwo\n")
			stackCommit(t, user2, repo, "a", "a", "update", "f.txt", "one\ntwo\nthree\n")
			a := stackPull(t, user2, repo, "main", "a")
			stackCommit(t, user2, repo, "a", "b", "update", "f.txt", "one\nTWO\nthree\n")
			b := stackPull(t, user2, repo, "a", "b")

			// the squashed commit of a is no ancestor of b: b conflicts with main after the retarget
			values := stackValues(stackFormHeads(stackPage(t, owner, repo, b)), false)
			values.Set("do", "squash")
			flash := postMergeStack(t, owner, repo, b, values)
			assert.Equal(t, fmt.Sprintf("Merged 1 of 2 pull requests, then stopped at #%[2]d: it is not in a mergeable state, for example because of merge conflicts. #%[1]d is merged into main. #%[2]d is still open and now targets main. Fix #%[2]d, then merge it again.", a.Index, b.Index), flash)
		})

		t.Run("Conflict", func(t *testing.T) {
			repo := forgery.CreateRepository(t, user2, &forgery.CreateRepositoryOptions{
				Files: forgery.MapFS{"f.txt": forgery.MapFile("one\ntwo\nthree\n")},
			})
			stackCommit(t, user2, repo, "main", "a", "create", "a.txt", "a")
			stackPull(t, user2, repo, "main", "a")
			stackCommit(t, user2, repo, "a", "b", "update", "f.txt", "one\nB\nthree\n")
			stackPull(t, user2, repo, "a", "b")
			stackCommit(t, user2, repo, "b", "c", "create", "c.txt", "c")
			c := stackPull(t, user2, repo, "b", "c")
			stackCommit(t, user2, repo, "c", "c", "update", "f.txt", "one\nC\nthree\n")
			// b changes the same line as c, which is based on the old b
			stackCommit(t, user2, repo, "b", "b", "update", "f.txt", "one\nB again\nthree\n")
			require.True(t, assert.Eventually(t, func() bool {
				return unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: c.ID}).Status == issues_model.PullRequestStatusConflict
			}, 10*time.Second, 50*time.Millisecond))

			doc := stackPage(t, owner, repo, c)
			assert.Contains(t, doc.Find("details.pull-stack li").Last().Text(), "Merge conflicts")
			assert.Contains(t, doc.Find("#pull-stack-merge").Text(), fmt.Sprintf("#%d can't be merged yet (merge conflicts)", c.Index))
			_, disabled := doc.Find("#pull-stack-merge button").Attr("disabled")
			assert.True(t, disabled)
		})

		t.Run("Merge", func(t *testing.T) {
			repo, prs := newPullStack(t, user2, nil)
			doc := stackPage(t, owner, repo, prs[2])
			flash := postMergeStack(t, owner, repo, prs[2], stackValues(stackFormHeads(doc), true))
			assert.Equal(t, "Merged 3 pull requests into main.", flash)
			assertMerged(t, true, prs...)
			for _, p := range prs {
				assert.False(t, git.IsBranchExist(t.Context(), repo.RepoPath(), p.HeadBranch), p.HeadBranch)
			}
		})

		t.Run("MergeKeepsBranches", func(t *testing.T) {
			repo, prs := newPullStack(t, user2, nil)
			doc := stackPage(t, owner, repo, prs[1])
			flash := postMergeStack(t, owner, repo, prs[1], stackValues(stackFormHeads(doc), false))
			assert.Equal(t, "Merged 2 pull requests into main.", flash)
			assertMerged(t, true, prs[:2]...)
			assertMerged(t, false, prs[2])
			for _, p := range prs {
				assert.True(t, git.IsBranchExist(t.Context(), repo.RepoPath(), p.HeadBranch), p.HeadBranch)
			}
			// the pull request on top is based on main now
			top := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: prs[2].ID})
			assert.Equal(t, "main", top.BaseBranch)
		})

		t.Run("Reader", func(t *testing.T) {
			repo, prs := newPullStack(t, user2, map[*user_model.User]perm.AccessMode{user4: perm.AccessModeRead})
			heads := stackFormHeads(stackPage(t, owner, repo, prs[2]))
			flash := postMergeStack(t, reader, repo, prs[2], stackValues(heads, true))
			assert.Contains(t, flash, "Nothing was merged: #")
			assert.Contains(t, flash, "You are not allowed to update branch")
			assertMerged(t, false, prs...)
			for _, p := range prs {
				assert.True(t, git.IsBranchExist(t.Context(), repo.RepoPath(), p.HeadBranch), p.HeadBranch)
			}
		})

		t.Run("Stale", func(t *testing.T) {
			repo, prs := newPullStack(t, user2, nil)
			heads := stackFormHeads(stackPage(t, owner, repo, prs[2]))
			before := stackHeads(t, repo, prs[1])[0].HeadCommitID
			stackCommit(t, user2, repo, "b", "b", "update", "b.txt", "pushed later")
			assert.Eventually(t, func() bool { return stackHeads(t, repo, prs[1])[0].HeadCommitID != before }, 10*time.Second, 50*time.Millisecond)
			waitChecked(t, prs...)

			flash := postMergeStack(t, owner, repo, prs[2], stackValues(heads, true))
			assert.Equal(t, fmt.Sprintf("Nothing was merged: the stack changed since you opened this page (#%d got new commits). Review it and try again.", prs[1].Index), flash)
			assertMerged(t, false, prs...)

			// a list that is not the stack as it is now
			flash = postMergeStack(t, owner, repo, prs[2], stackValues(heads[:2], true))
			assert.Equal(t, "Nothing was merged: the stack changed since you opened this page.", flash)
			assertMerged(t, false, prs...)
		})

		t.Run("List", func(t *testing.T) {
			repo, prs := newPullStack(t, user2, nil)
			stackCommit(t, user2, repo, "main", "alone", "create", "alone.txt", "alone")
			alone := stackPull(t, user2, repo, "main", "alone")

			resp := owner.MakeRequest(t, NewRequestf(t, "GET", "%s/pulls", repo.Link()), http.StatusOK)
			doc := NewHTMLParser(t, resp.Body)
			mark := func(pr *issues_model.PullRequest) *goquery.Selection {
				return doc.Find("#issue-list .flex-item").FilterFunction(func(_ int, s *goquery.Selection) bool {
					return strings.Contains(s.Find("a.index").Text(), fmt.Sprintf("#%d", pr.Index))
				}).Find(".pull-stack-mark")
			}
			assert.Equal(t, "Bottom of a stack", strings.TrimSpace(mark(prs[0]).Text()))
			assert.Zero(t, mark(prs[0]).Find("a").Length())
			for i := 1; i < 3; i++ {
				assert.Equal(t, fmt.Sprintf("Stacked on #%d", prs[i-1].Index), strings.TrimSpace(mark(prs[i]).Text()))
				assert.Equal(t, fmt.Sprintf("%s/pulls/%d", repo.Link(), prs[i-1].Index), mark(prs[i]).AttrOr("href", ""))
			}
			assert.Zero(t, mark(alone).Length())
		})

		t.Run("Project", func(t *testing.T) {
			f := prepareRepoGroupWeb(t)
			prs := addPullStack(t, user2, f.pub)
			assert.Equal(t, "/org3/proj/pub", f.pub.Link())

			doc := stackPage(t, owner, f.pub, prs[1])
			rows := doc.Find("details.pull-stack li.pull-stack-row")
			require.Equal(t, 3, rows.Length())
			assert.Equal(t, fmt.Sprintf("/org3/proj/pub/pulls/%d", prs[0].Index), rows.Eq(0).Find("a").AttrOr("href", ""))
			assert.Equal(t, fmt.Sprintf("/org3/proj/pub/pulls/%d", prs[2].Index), rows.Eq(2).Find("a").AttrOr("href", ""))
			assert.Equal(t, fmt.Sprintf("/org3/proj/pub/pulls/%d", prs[0].Index), doc.Find("#pull-stack-merge a").AttrOr("href", ""))
			for _, p := range []*issues_model.PullRequest{prs[0], prs[2]} {
				owner.MakeRequest(t, NewRequestf(t, "GET", "/org3/proj/pub/pulls/%d", p.Index), http.StatusOK)
			}

			heads := stackFormHeads(doc)
			require.Len(t, heads, 2)
			flash := postMergeStack(t, owner, f.pub, prs[1], stackValues(heads, true))
			assert.Equal(t, "Merged 2 pull requests into main.", flash)
			assertMerged(t, true, prs[:2]...)
		})

		t.Run("MarksQueries", func(t *testing.T) {
			_, prs := newPullStack(t, user2, nil)
			load := func(n int) issues_model.IssueList {
				list := make(issues_model.IssueList, 0, n)
				for _, pr := range prs[:n] {
					pr = unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: pr.ID})
					require.NoError(t, pr.LoadIssue(db.DefaultContext))
					pr.Issue.PullRequest = pr
					list = append(list, pr.Issue)
				}
				return list
			}
			find := func(list issues_model.IssueList) int64 {
				return stableQueryCount(t, func() {
					marks, err := issues_model.FindStackMarks(db.DefaultContext, list)
					require.NoError(t, err)
					assert.Len(t, marks, len(list))
				})
			}
			assert.EqualValues(t, 1, find(load(1)))
			assert.EqualValues(t, 1, find(load(3)))
		})
	})
}
