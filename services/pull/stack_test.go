// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package pull

import (
	"fmt"
	"testing"

	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newStackPR inserts an open pull request of user2/repo1 from head into base; mod can change it before.
func newStackPR(t *testing.T, base, head string, mod ...func(*issues_model.PullRequest)) *issues_model.PullRequest {
	t.Helper()
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	issue := &issues_model.Issue{RepoID: repo.ID, Title: head, PosterID: user.ID, Poster: user, IsPull: true}
	pr := &issues_model.PullRequest{
		HeadRepoID: repo.ID,
		BaseRepoID: repo.ID,
		HeadBranch: head,
		BaseBranch: base,
		Type:       issues_model.PullRequestGitea,
		Flow:       issues_model.PullRequestFlowGithub,
	}
	for _, m := range mod {
		m(pr)
	}
	require.NoError(t, issues_model.NewPullRequest(db.DefaultContext, repo, issue, nil, nil, pr))
	return pr
}

// stackOf returns the stack of pr as "head<parent head@depth" entries, the current one marked with "*".
func stackOf(t *testing.T, pr *issues_model.PullRequest) []string {
	t.Helper()
	pr = unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: pr.ID})
	stack, err := GetStack(db.DefaultContext, pr)
	require.NoError(t, err)
	if len(stack.Entries) == 0 {
		return nil
	}
	heads := map[int64]string{}
	out := make([]string, 0, len(stack.Entries))
	for _, e := range stack.Entries {
		heads[e.PR.Index] = e.PR.HeadBranch
		s := fmt.Sprintf("%s<%s@%d", e.PR.HeadBranch, heads[e.ParentIndex], e.Depth)
		if e.PR.ID == pr.ID {
			s += "*"
		}
		require.NotNil(t, e.PR.Issue)
		out = append(out, s)
	}
	return out
}

func closePR(t *testing.T, pr *issues_model.PullRequest) {
	t.Helper()
	_, err := db.GetEngine(db.DefaultContext).Exec("UPDATE issue SET is_closed = ? WHERE id = ?", true, pr.IssueID)
	require.NoError(t, err)
}

func TestGetStack(t *testing.T) {
	t.Run("Linear", func(t *testing.T) {
		require.NoError(t, unittest.PrepareTestDatabase())
		a := newStackPR(t, "main", "a")
		b := newStackPR(t, "a", "b")
		c := newStackPR(t, "b", "c")

		assert.Equal(t, []string{"a<@0*", "b<a@1", "c<b@2"}, stackOf(t, a))
		assert.Equal(t, []string{"a<@0", "b<a@1*", "c<b@2"}, stackOf(t, b))
		assert.Equal(t, []string{"a<@0", "b<a@1", "c<b@2*"}, stackOf(t, c))

		stack, err := GetStack(db.DefaultContext, c)
		require.NoError(t, err)
		assert.Equal(t, "main", stack.BaseBranch)
		assert.False(t, stack.Truncated)
		assert.EqualValues(t, 0, stack.Entries[0].ParentIndex)
		assert.Equal(t, a.Index, stack.Entries[1].ParentIndex)
		assert.Equal(t, b.Index, stack.Entries[2].ParentIndex)
	})

	t.Run("Alone", func(t *testing.T) {
		require.NoError(t, unittest.PrepareTestDatabase())
		assert.Nil(t, stackOf(t, newStackPR(t, "main", "alone")))
	})

	t.Run("Branching", func(t *testing.T) {
		require.NoError(t, unittest.PrepareTestDatabase())
		a := newStackPR(t, "main", "a")
		newStackPR(t, "a", "b")
		c := newStackPR(t, "b", "c")
		d := newStackPR(t, "b", "d")
		newStackPR(t, "c", "c2")

		assert.Equal(t, []string{"a<@0*", "b<a@1", "c<b@2", "c2<c@3", "d<b@2"}, stackOf(t, a))
		// siblings are not part of the stack of a pull request
		assert.Equal(t, []string{"a<@0", "b<a@1", "c<b@2*", "c2<c@3"}, stackOf(t, c))
		assert.Equal(t, []string{"a<@0", "b<a@1", "d<b@2*"}, stackOf(t, d))
	})

	t.Run("AmbiguousParent", func(t *testing.T) {
		require.NoError(t, unittest.PrepareTestDatabase())
		a1 := newStackPR(t, "main", "a")
		newStackPR(t, "other", "a")
		b := newStackPR(t, "a", "b")
		c := newStackPR(t, "b", "c")

		assert.Nil(t, stackOf(t, a1))
		assert.Equal(t, []string{"b<@0*", "c<b@1"}, stackOf(t, b))
		assert.Equal(t, []string{"b<@0", "c<b@1*"}, stackOf(t, c))
	})

	t.Run("Cycle", func(t *testing.T) {
		require.NoError(t, unittest.PrepareTestDatabase())
		x := newStackPR(t, "p", "q")
		y := newStackPR(t, "q", "p")

		assert.Equal(t, []string{"p<@0", "q<p@1*"}, stackOf(t, x))
		assert.Equal(t, []string{"q<@0", "p<q@1*"}, stackOf(t, y))
	})

	t.Run("ClosedAndMerged", func(t *testing.T) {
		require.NoError(t, unittest.PrepareTestDatabase())
		a := newStackPR(t, "main", "a")
		b := newStackPR(t, "a", "b")
		c := newStackPR(t, "b", "c")
		closePR(t, newStackPR(t, "b", "closed"))
		closePR(t, newStackPR(t, "c", "c2"))

		assert.Equal(t, []string{"a<@0", "b<a@1*", "c<b@2"}, stackOf(t, b))

		_, err := db.GetEngine(db.DefaultContext).Exec("UPDATE pull_request SET has_merged = ? WHERE id = ?", true, a.ID)
		require.NoError(t, err)
		assert.Nil(t, stackOf(t, a))
		assert.Equal(t, []string{"b<@0", "c<b@1*"}, stackOf(t, c))

		closePR(t, c)
		assert.Nil(t, stackOf(t, b))
		assert.Nil(t, stackOf(t, c))
	})

	t.Run("AGitAndFork", func(t *testing.T) {
		require.NoError(t, unittest.PrepareTestDatabase())
		agit := func(pr *issues_model.PullRequest) { pr.Flow = issues_model.PullRequestFlowAGit }
		fork := func(pr *issues_model.PullRequest) { pr.HeadRepoID = 11 }

		// an AGit head is no branch: no parent
		newStackPR(t, "main", "a", agit)
		b := newStackPR(t, "a", "b")
		// nor is a fork's head branch
		newStackPR(t, "b", "x", fork)
		newStackPR(t, "x", "y")
		// both can be children
		newStackPR(t, "b", "z", agit)

		assert.Equal(t, []string{"b<@0*", "x<b@1", "z<b@1"}, stackOf(t, b))
	})

	t.Run("Truncated", func(t *testing.T) {
		require.NoError(t, unittest.PrepareTestDatabase())
		prs := make([]*issues_model.PullRequest, 0, MaxStackSize+5)
		for i := range MaxStackSize + 5 {
			prs = append(prs, newStackPR(t, fmt.Sprintf("s%d", i), fmt.Sprintf("s%d", i+1)))
		}

		for _, pr := range []*issues_model.PullRequest{prs[0], prs[len(prs)-1]} {
			stack, err := GetStack(db.DefaultContext, pr)
			require.NoError(t, err)
			assert.True(t, stack.Truncated)
			assert.Len(t, stack.Entries, MaxStackSize)
		}
		// seen from the top, the pull request itself is always in it
		stack, err := GetStack(db.DefaultContext, prs[len(prs)-1])
		require.NoError(t, err)
		assert.Equal(t, prs[len(prs)-1].ID, stack.Entries[MaxStackSize-1].PR.ID)
	})
}
