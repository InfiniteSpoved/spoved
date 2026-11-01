// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretscan

import (
	"fmt"
	"strings"
	"testing"

	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	secretscan_model "forgejo.org/models/secretscan"
	"forgejo.org/modules/queue"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// enableText is enable with the text scan on and a small text limit.
func enableText(t *testing.T) *repo_model.Repository {
	t.Helper()
	repo := enable(t)
	t.Cleanup(test.MockVariableValue(&setting.SecretScanning.ScanText, true))
	t.Cleanup(test.MockVariableValue(&setting.SecretScanning.MaxTextBytes, 4096))
	return repo
}

var nextIndex = int64(1000)

func newIssue(t *testing.T, repo *repo_model.Repository, title, body string) *issues_model.Issue {
	t.Helper()
	nextIndex++
	issue := &issues_model.Issue{RepoID: repo.ID, Index: nextIndex, PosterID: 2, Title: title, Content: body}
	require.NoError(t, db.Insert(t.Context(), issue))
	return issue
}

func newComment(t *testing.T, issue *issues_model.Issue, body string) *issues_model.Comment {
	t.Helper()
	c := &issues_model.Comment{Type: issues_model.CommentTypeComment, IssueID: issue.ID, PosterID: 3, Content: body}
	require.NoError(t, db.Insert(t.Context(), c))
	return c
}

func setText(t *testing.T, table string, id int64, col, text string) {
	t.Helper()
	_, err := db.GetEngine(t.Context()).Table(table).Where("id = ?", id).Update(map[string]any{col: text})
	require.NoError(t, err)
}

func textPlaces(t *testing.T, repoID int64) []*secretscan_model.Location {
	t.Helper()
	var locs []*secretscan_model.Location
	require.NoError(t, db.GetEngine(t.Context()).Where("repo_id = ? AND kind IN (?, ?)", repoID, secretscan_model.KindIssue, secretscan_model.KindComment).OrderBy("id").Find(&locs))
	return locs
}

func TestScanTextIssueAndComment(t *testing.T) {
	repo := enableText(t)
	tok := token("text-body")
	titleTok := token("text-title")
	issue := newIssue(t, repo, "leaked "+titleTok, "first line\nsecond: "+tok+"\n")
	c := newComment(t, issue, "also here\n\n"+tok)

	require.NoError(t, handleText(t.Context(), fmt.Sprintf("i:%d", issue.ID)))
	require.NoError(t, handleText(t.Context(), fmt.Sprintf("c:%d", c.ID)))

	a := alertFor(t, repo.ID, tok)
	require.NotNil(t, a)
	assert.True(t, a.ViaPull, "an alert that only text reaches shares the cap of the pull refs")
	assert.Equal(t, secretscan_model.StateOpen, a.State)
	assert.NotNil(t, alertFor(t, repo.ID, titleTok))

	locs := locationsOf(t, a.ID)
	require.Len(t, locs, 2)
	byKind := map[string]*secretscan_model.Location{}
	for _, l := range locs {
		byKind[l.Kind] = l
		assert.Empty(t, l.CommitID)
		assert.Zero(t, l.RemovedUnix)
	}
	assert.Equal(t, secretscan_model.KindIssue, byKind["issue"].Kind)
	assert.Equal(t, "body", byKind["issue"].Field)
	assert.Equal(t, 2, byKind["issue"].StartLine)
	assert.Equal(t, issue.ID, byKind["issue"].SourceID)
	assert.Equal(t, issue.ID, byKind["issue"].IssueID)
	assert.EqualValues(t, 2, byKind["issue"].PosterID)
	assert.Equal(t, 3, byKind["comment"].StartLine)
	assert.Equal(t, c.ID, byKind["comment"].SourceID)
	assert.Equal(t, issue.ID, byKind["comment"].IssueID)
	assert.EqualValues(t, 3, byKind["comment"].PosterID)
	assert.Equal(t, 1, locationsOfTitle(t, alertFor(t, repo.ID, titleTok).ID).StartLine)

	t.Run("the places in text are not shown by the readers of places", func(t *testing.T) {
		first, err := secretscan_model.FirstLocations(t.Context(), repo.ID, []int64{a.ID}, false)
		require.NoError(t, err)
		assert.Empty(t, first)
		counts, err := secretscan_model.CountLocations(t.Context(), repo.ID, []int64{a.ID}, false)
		require.NoError(t, err)
		assert.Empty(t, counts)
		found, err := secretscan_model.FindLocations(t.Context(), repo.ID, a.ID, 10)
		require.NoError(t, err)
		assert.Empty(t, found)
		has, err := secretscan_model.HasCodeLocation(t.Context(), repo.ID, a.ID)
		require.NoError(t, err)
		assert.False(t, has)
	})

	t.Run("the same text again stores nothing new", func(t *testing.T) {
		require.NoError(t, handleText(t.Context(), fmt.Sprintf("i:%d", issue.ID)))
		assert.Len(t, locationsOf(t, a.ID), 2)
		assert.Len(t, alertsOf(t, repo.ID), 2)
	})

	t.Run("an edit that removes the secret keeps the alert and marks the place", func(t *testing.T) {
		setText(t, "issue", issue.ID, "content", "nothing here")
		require.NoError(t, handleText(t.Context(), fmt.Sprintf("i:%d", issue.ID)))
		for _, l := range locationsOf(t, a.ID) {
			if l.Kind == secretscan_model.KindIssue {
				assert.NotZero(t, l.RemovedUnix)
			} else {
				assert.Zero(t, l.RemovedUnix, "the comment still holds it")
			}
		}
		assert.Equal(t, secretscan_model.StateOpen, alertFor(t, repo.ID, tok).State)
		// pasted again: the place is back
		setText(t, "issue", issue.ID, "content", "again\nsecond: "+tok)
		require.NoError(t, handleText(t.Context(), fmt.Sprintf("i:%d", issue.ID)))
		for _, l := range locationsOf(t, a.ID) {
			assert.Zero(t, l.RemovedUnix)
		}
	})

	t.Run("a deleted comment and a deleted issue mark their places", func(t *testing.T) {
		require.NoError(t, handleText(t.Context(), fmt.Sprintf("x:comment:%d:%d", repo.ID, c.ID)))
		for _, l := range locationsOf(t, a.ID) {
			assert.Equal(t, l.Kind == secretscan_model.KindComment, l.RemovedUnix != 0, l.Kind)
		}
		require.NoError(t, handleText(t.Context(), fmt.Sprintf("x:issue:%d:%d", repo.ID, issue.ID)))
		for _, l := range textPlaces(t, repo.ID) {
			assert.NotZero(t, l.RemovedUnix, l.Kind)
		}
		assert.Equal(t, secretscan_model.StateOpen, alertFor(t, repo.ID, tok).State, "the secret was exposed: the alert stays")
	})
}

func locationsOfTitle(t *testing.T, alertID int64) *secretscan_model.Location {
	t.Helper()
	for _, l := range locationsOf(t, alertID) {
		if l.Field == "title" {
			return l
		}
	}
	require.FailNow(t, "no place in the title")
	return nil
}

func TestScanTextSwitches(t *testing.T) {
	repo := enableText(t)
	issue := newIssue(t, repo, "t", "key "+token("switch"))
	scan := func() { require.NoError(t, handleText(t.Context(), fmt.Sprintf("i:%d", issue.ID))) }

	t.Run("the text switch", func(t *testing.T) {
		defer test.MockVariableValue(&setting.SecretScanning.ScanText, false)()
		scan()
		assert.Empty(t, alertsOf(t, repo.ID))
	})
	t.Run("the site switch", func(t *testing.T) {
		defer test.MockVariableValue(&setting.SecretScanning.Enabled, false)()
		scan()
		assert.Empty(t, alertsOf(t, repo.ID))
	})
	t.Run("the repository switch", func(t *testing.T) {
		_, err := db.GetEngine(t.Context()).Table("repository").Where("id = ?", repo.ID).Update(map[string]any{"secret_scanning_disabled": true})
		require.NoError(t, err)
		scan()
		assert.Empty(t, alertsOf(t, repo.ID))
		_, err = db.GetEngine(t.Context()).Table("repository").Where("id = ?", repo.ID).Update(map[string]any{"secret_scanning_disabled": false})
		require.NoError(t, err)
	})
	t.Run("on", func(t *testing.T) {
		scan()
		assert.Len(t, alertsOf(t, repo.ID), 1)
	})
	t.Run("a deleted text and a bad item are no failure", func(t *testing.T) {
		require.NoError(t, handleText(t.Context(), "i:999999"))
		require.NoError(t, handleText(t.Context(), "c:999999"))
		require.NoError(t, handleText(t.Context(), "nonsense"))
		require.NoError(t, handleText(t.Context(), "i:abc"))
	})
}

func TestScanTextPendingReview(t *testing.T) {
	repo := enableText(t)
	issue := newIssue(t, repo, "pr", "")
	review := &issues_model.Review{Type: issues_model.ReviewTypePending, IssueID: issue.ID, ReviewerID: 3}
	require.NoError(t, db.Insert(t.Context(), review))
	c := &issues_model.Comment{Type: issues_model.CommentTypeCode, IssueID: issue.ID, PosterID: 3, ReviewID: review.ID, Content: "draft " + token("draft"), TreePath: "a.go", Line: 1}
	require.NoError(t, db.Insert(t.Context(), c))

	require.NoError(t, handleText(t.Context(), fmt.Sprintf("c:%d", c.ID)))
	assert.Empty(t, alertsOf(t, repo.ID), "a draft is not scanned: it is not published")

	// the backfill skips it too, then the submission scans it
	require.NoError(t, Scan(t.Context(), repo.ID))
	assert.Empty(t, alertsOf(t, repo.ID))

	_, err := db.GetEngine(t.Context()).Table("review").Where("id = ?", review.ID).Update(map[string]any{"type": issues_model.ReviewTypeComment})
	require.NoError(t, err)
	require.NoError(t, handleText(t.Context(), fmt.Sprintf("c:%d", c.ID)))
	require.Len(t, alertsOf(t, repo.ID), 1)
	assert.Equal(t, secretscan_model.KindComment, textPlaces(t, repo.ID)[0].Kind)
}

func TestScanTextLimits(t *testing.T) {
	repo := enableText(t)
	tok := token("limits")

	t.Run("text over the limit is cut", func(t *testing.T) {
		defer test.MockVariableValue(&setting.SecretScanning.MaxTextBytes, 200)()
		issue := newIssue(t, repo, "t", strings.Repeat("x", 300)+"\n"+tok)
		require.NoError(t, handleText(t.Context(), fmt.Sprintf("i:%d", issue.ID)))
		assert.Nil(t, alertFor(t, repo.ID, tok), "the part past the limit is not scanned")
	})
	t.Run("a data URI payload is skipped, the rest of the text is not", func(t *testing.T) {
		defer test.MockVariableValue(&setting.SecretScanning.MaxTextBytes, 1<<20)()
		payload := strings.Repeat("QUJD", 400) + tok + strings.Repeat("QUJD", 400)
		issue := newIssue(t, repo, "t", "![x](data:image/png;base64,"+payload+")\nreal: "+tok)
		require.NoError(t, handleText(t.Context(), fmt.Sprintf("i:%d", issue.ID)))
		a := alertFor(t, repo.ID, tok)
		require.NotNil(t, a)
		locs := locationsOf(t, a.ID)
		require.Len(t, locs, 1)
		assert.Equal(t, 2, locs[0].StartLine)
	})
	t.Run("places per alert are capped", func(t *testing.T) {
		defer test.MockVariableValue(&setting.SecretScanning.MaxLocationsPerAlert, 2)()
		other := token("many")
		for range 4 {
			issue := newIssue(t, repo, "t", other)
			require.NoError(t, handleText(t.Context(), fmt.Sprintf("i:%d", issue.ID)))
		}
		a := alertFor(t, repo.ID, other)
		require.NotNil(t, a)
		assert.Len(t, locationsOf(t, a.ID), 2)
		assert.Equal(t, 4, a.LocationCount)
	})
	t.Run("alerts are capped like the pull refs' share", func(t *testing.T) {
		defer test.MockVariableValue(&setting.SecretScanning.MaxAlertsPerRepo, 4)()
		before := len(alertsOf(t, repo.ID))
		var body []string
		for i := range 10 {
			body = append(body, token(fmt.Sprintf("cap-%d", i)))
		}
		issue := newIssue(t, repo, "t", strings.Join(body, "\n"))
		require.NoError(t, handleText(t.Context(), fmt.Sprintf("i:%d", issue.ID)))
		assert.LessOrEqual(t, len(alertsOf(t, repo.ID))-before, 4)
	})
}

func TestScanTextBackfill(t *testing.T) {
	repo := enableText(t)
	var toks []string
	for i := range 3 {
		tok := token(fmt.Sprintf("back-%d", i))
		toks = append(toks, tok)
		issue := newIssue(t, repo, "t", "x")
		newComment(t, issue, "plain")
		newComment(t, issue, "secret "+tok)
	}
	// text of another repository is not read
	otherRepo := unittestRepo(t, 2)
	newIssue(t, otherRepo, "t", token("elsewhere"))

	defer test.MockVariableValue(&maxTextItems, 2)()
	slices := 0
	for {
		requeue, err := scan(t.Context(), repo.ID)
		require.NoError(t, err)
		slices++
		s := scanOf(t, repo.ID, secretscan_model.KindIssue)
		require.NotNil(t, s)
		if !requeue {
			break
		}
		assert.Equal(t, secretscan_model.StatusIncomplete, s.Status)
		assert.NotZero(t, s.RetryUnix)
		require.Less(t, slices, 40)
	}
	assert.Greater(t, slices, 2, "the budget cuts the backfill into slices")
	for _, tok := range toks {
		require.NotNil(t, alertFor(t, repo.ID, tok), tok)
	}
	assert.Nil(t, alertFor(t, repo.ID, token("elsewhere")))
	assert.Len(t, textPlaces(t, repo.ID), 3)
	s := scanOf(t, repo.ID, secretscan_model.KindIssue)
	assert.Zero(t, s.RetryUnix)
	assert.Equal(t, secretscan_model.StatusOK, s.Status)
	assert.True(t, s.Completed)

	t.Run("a finished backfill is not run again", func(t *testing.T) {
		newIssue(t, repo, "t", token("later"))
		requeue, err := scan(t.Context(), repo.ID)
		require.NoError(t, err)
		assert.False(t, requeue)
		assert.Nil(t, alertFor(t, repo.ID, token("later")), "new text comes through the notifier")
	})
}

func unittestRepo(t *testing.T, id int64) *repo_model.Repository {
	t.Helper()
	repo, err := repo_model.GetRepositoryByID(t.Context(), id)
	require.NoError(t, err)
	return repo
}

func TestScanTextBackfillHoles(t *testing.T) {
	repo := enableText(t)
	defer test.MockVariableValue(&setting.SecretScanning.MaxTextBytes, 1<<10)()
	newIssue(t, repo, "t", strings.Repeat("y", 3000))
	_, err := scan(t.Context(), repo.ID)
	require.NoError(t, err)
	s := scanOf(t, repo.ID, secretscan_model.KindIssue)
	require.NotNil(t, s)
	assert.EqualValues(t, 1, s.Holes, "the text over the limit is a hole")
	assert.Equal(t, secretscan_model.StatusIncomplete, s.Status)
	assert.Equal(t, stopPartial, s.Error)
	assert.Zero(t, s.RetryUnix)
}

func TestScanTextNotifierQueuesItems(t *testing.T) {
	repo := enableText(t)
	var items []string
	t.Cleanup(test.MockVariableValue(&textQueue, &queue.WorkerPoolQueue[string]{}))
	t.Cleanup(test.MockVariableValue(&pushTextQueue, func(item string) error { items = append(items, item); return nil }))
	issue := newIssue(t, repo, "t", "b")
	c := newComment(t, issue, "c")
	n := &notifier{}
	ctx := t.Context()

	n.NewIssue(ctx, issue, nil)
	n.IssueChangeTitle(ctx, nil, issue, "old")
	n.IssueChangeContent(ctx, nil, issue, "old")
	n.CreateIssueComment(ctx, nil, repo, issue, c, nil)
	n.UpdateComment(ctx, nil, c, "old")
	n.PullRequestReview(ctx, nil, nil, c, nil)
	n.PullRequestCodeComment(ctx, nil, c, nil)
	n.DeleteComment(ctx, nil, c)
	n.DeleteIssue(ctx, nil, issue)
	i, cs := fmt.Sprintf("i:%d", issue.ID), fmt.Sprintf("c:%d", c.ID)
	assert.Equal(t, []string{
		i, i, i, cs, cs, cs, cs,
		fmt.Sprintf("x:comment:%d:%d", repo.ID, c.ID), fmt.Sprintf("x:issue:%d:%d", repo.ID, issue.ID),
	}, items)

	t.Run("a comment that carries no text, and a site switched off, queue nothing", func(t *testing.T) {
		items = nil
		n.CreateIssueComment(ctx, nil, repo, issue, &issues_model.Comment{ID: 5, Type: issues_model.CommentTypeClose}, nil)
		defer test.MockVariableValue(&setting.SecretScanning.Enabled, false)()
		n.NewIssue(ctx, issue, nil)
		assert.Empty(t, items)
	})
}

func TestStripDataURIs(t *testing.T) {
	long := strings.Repeat("A", 1200)
	assert.Equal(t, "see data:image/png;base64, here", stripDataURIs("see data:image/png;base64,"+long+" here"))
	short := "data:image/png;base64," + strings.Repeat("A", 100)
	assert.Equal(t, short, stripDataURIs(short), "a short payload is kept")
	assert.Equal(t, "no uri "+long, stripDataURIs("no uri "+long))
}
