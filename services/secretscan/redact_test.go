// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretscan

import (
	"fmt"
	"strings"
	"testing"

	activities_model "forgejo.org/models/activities"
	audit_model "forgejo.org/models/audit"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	secretscan_model "forgejo.org/models/secretscan"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/json"
	"forgejo.org/modules/secretdetection"
	"forgejo.org/modules/test"
	"forgejo.org/modules/util"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedactSpans(t *testing.T) {
	d, err := secretdetection.NewDetector(secretdetection.BuiltinRules())
	require.NoError(t, err)
	tok, other := token("redact-a"), token("redact-b")
	fp := secretdetection.Fingerprint([]byte(tok))

	t.Run("every copy of the secret, none of another", func(t *testing.T) {
		out, n, complete := redact(d, "a "+tok+"\nb "+other+"\nc ("+tok+")", "github-token", fp)
		assert.True(t, complete)
		assert.Equal(t, 2, n)
		assert.Equal(t, "a "+RedactedMark+"\nb "+other+"\nc ("+RedactedMark+")", out)
	})
	t.Run("only the alert's rule", func(t *testing.T) {
		out, n, _ := redact(d, "x "+tok, "generic-secret-assignment", fp)
		assert.Zero(t, n)
		assert.Equal(t, "x "+tok, out)
	})
	t.Run("text around it in other scripts stays whole", func(t *testing.T) {
		out, n, _ := redact(d, "Größe 🚀 日本語 "+tok+" ✓ ü", "github-token", fp)
		assert.Equal(t, 1, n)
		assert.Equal(t, "Größe 🚀 日本語 "+RedactedMark+" ✓ ü", out)
	})
	t.Run("gone", func(t *testing.T) {
		_, n, complete := redact(d, "nothing here", "github-token", fp)
		assert.Zero(t, n)
		assert.True(t, complete)
	})
	t.Run("text the detector would read as UTF-16 is not touched", func(t *testing.T) {
		out, n, complete := redact(d, "a\x00 "+tok, "github-token", fp)
		assert.Zero(t, n)
		assert.False(t, complete)
		assert.Equal(t, "a\x00 "+tok, out)
	})
}

// redactFixture is a repository with an issue whose title, body and comment hold secrets, scanned.
type redactFixture struct {
	repo    *repo_model.Repository
	doer    *user_model.User
	issue   *issues_model.Issue
	comment *issues_model.Comment
	tok     string
	alert   *secretscan_model.Alert
}

var readBoth = secretscan_model.TextAccess{Issues: true, Pulls: true}

func newRedactFixture(t *testing.T) *redactFixture {
	t.Helper()
	repo := enableText(t)
	tok, other := token("redact-1"), token("redact-2")
	issue := newIssue(t, repo, "leaked "+tok, "first "+tok+"\nother "+other+"\nagain "+tok)
	c := newComment(t, issue, "in a comment: "+tok)
	require.NoError(t, handleText(t.Context(), fmt.Sprintf("i:%d:0", issue.ID)))
	require.NoError(t, handleText(t.Context(), fmt.Sprintf("c:%d:0", c.ID)))
	a := alertFor(t, repo.ID, tok)
	require.NotNil(t, a)
	require.NotNil(t, alertFor(t, repo.ID, other))
	return &redactFixture{repo: repo, doer: unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2}), issue: issue, comment: c, tok: tok, alert: a}
}

func (f *redactFixture) redact(t *testing.T, target RedactTarget, access secretscan_model.TextAccess) error {
	t.Helper()
	return RedactText(audit_model.WithSystem(t.Context(), "test"), f.repo, f.doer, f.alert.Index, target, access, true)
}

func placesOf(t *testing.T, alertID int64, kind, field string) []*secretscan_model.Location {
	t.Helper()
	var out []*secretscan_model.Location
	for _, l := range locationsOf(t, alertID) {
		if l.Kind == kind && l.Field == field {
			out = append(out, l)
		}
	}
	return out
}

func TestRedactBody(t *testing.T) {
	f := newRedactFixture(t)
	other := token("redact-2")
	// an edit before: its version in the history holds the secret
	require.NoError(t, issues_model.SaveIssueContentHistory(t.Context(), 2, f.issue.ID, 0, f.issue.CreatedUnix, f.issue.Content, true))
	require.NoError(t, issues_model.SaveIssueContentHistory(t.Context(), 2, f.issue.ID, 0, f.issue.CreatedUnix+1, f.issue.Content+" v2", false))

	require.NoError(t, f.redact(t, RedactTarget{Issue: f.issue.Index, Field: "body"}, readBoth))

	issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: f.issue.ID})
	assert.Equal(t, "first "+RedactedMark+"\nother "+other+"\nagain "+RedactedMark, issue.Content, "both copies, never the other secret")
	assert.Equal(t, "leaked "+f.tok, issue.Title, "the title is another text")
	assert.Equal(t, f.issue.ContentVersion+1, issue.ContentVersion, "an edit that raced it fails on the version")

	var history []*issues_model.ContentHistory
	require.NoError(t, db.GetEngine(t.Context()).Where("issue_id = ? AND comment_id = 0", issue.ID).OrderBy("id").Find(&history))
	require.Len(t, history, 3)
	for _, h := range history[:2] {
		assert.True(t, h.IsDeleted)
		assert.Empty(t, h.ContentText)
	}
	assert.False(t, history[2].IsDeleted)
	assert.Equal(t, issue.Content, history[2].ContentText)
	assert.Equal(t, f.doer.ID, history[2].PosterID, "the edit is the alert manager's")

	for _, l := range placesOf(t, f.alert.ID, secretscan_model.KindIssue, "body") {
		assert.NotZero(t, l.RemovedUnix)
	}
	for _, l := range append(placesOf(t, f.alert.ID, secretscan_model.KindIssue, "title"), placesOf(t, f.alert.ID, secretscan_model.KindComment, "")...) {
		assert.Zero(t, l.RemovedUnix, "the places in the other texts stay")
	}
	assert.Equal(t, secretscan_model.StateOpen, alertFor(t, f.repo.ID, f.tok).State, "the alert stays open: the secret was exposed")

	var e audit_model.Entry
	has, err := db.GetEngine(t.Context()).Where("action = ?", "repo.secret_alert.text_redacted").Get(&e)
	require.NoError(t, err)
	require.True(t, has)
	assert.Equal(t, "secret_scanning_alert", e.TargetType)
	assert.Equal(t, f.alert.ID, e.TargetID)
	assert.Equal(t, f.repo.ID, e.RepoID)
	var data map[string]any
	require.NoError(t, json.Unmarshal([]byte(e.Data), &data))
	assert.EqualValues(t, f.alert.Index, data["index"])
	assert.EqualValues(t, f.issue.Index, data["issue"])
	assert.Equal(t, "body", data["field"])
	assert.EqualValues(t, 2, data["replaced"])
	assert.NotContains(t, e.Data, f.tok[4:])
	assert.NotContains(t, e.Data, "first")

	t.Run("again: the secret is gone", func(t *testing.T) {
		require.ErrorIs(t, f.redact(t, RedactTarget{Issue: f.issue.Index, Field: "body"}, readBoth), ErrRedactGone)
	})
	t.Run("the history shows the redaction, without the text", func(t *testing.T) {
		events, err := AlertHistory(t.Context(), f.repo.ID, f.alert.ID)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.True(t, events[0].Redacted)
	})
}

func TestRedactTitle(t *testing.T) {
	f := newRedactFixture(t)
	change := &issues_model.Comment{Type: issues_model.CommentTypeChangeTitle, IssueID: f.issue.ID, PosterID: 2, OldTitle: "draft " + f.tok, NewTitle: f.issue.Title}
	require.NoError(t, db.Insert(t.Context(), change))
	feed := &activities_model.Action{OpType: activities_model.ActionCreateIssue, RepoID: f.repo.ID, ActUserID: 2, Content: fmt.Sprintf(`["%d","%s"]`, f.issue.Index, f.issue.Title)}
	elsewhere := &activities_model.Action{OpType: activities_model.ActionCreateIssue, RepoID: f.repo.ID, ActUserID: 2, Content: fmt.Sprintf(`["%d","%s"]`, f.issue.Index+1, f.issue.Title)}
	require.NoError(t, db.Insert(t.Context(), feed))
	require.NoError(t, db.Insert(t.Context(), elsewhere))

	require.NoError(t, f.redact(t, RedactTarget{Issue: f.issue.Index, Field: "title"}, readBoth))

	issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: f.issue.ID})
	assert.Equal(t, "leaked "+RedactedMark, issue.Title)
	assert.Contains(t, issue.Content, f.tok, "the body is another text")
	change = unittest.AssertExistsAndLoadBean(t, &issues_model.Comment{ID: change.ID})
	assert.Equal(t, "draft "+RedactedMark, change.OldTitle)
	assert.Equal(t, "leaked "+RedactedMark, change.NewTitle)
	assert.Equal(t, fmt.Sprintf(`["%d","leaked %s"]`, f.issue.Index, RedactedMark), unittest.AssertExistsAndLoadBean(t, &activities_model.Action{ID: feed.ID}).Content)
	assert.Contains(t, unittest.AssertExistsAndLoadBean(t, &activities_model.Action{ID: elsewhere.ID}).Content, f.tok, "another issue's feed entry is left alone")
	for _, l := range placesOf(t, f.alert.ID, secretscan_model.KindIssue, "title") {
		assert.NotZero(t, l.RemovedUnix)
	}
	for _, l := range placesOf(t, f.alert.ID, secretscan_model.KindIssue, "body") {
		assert.Zero(t, l.RemovedUnix)
	}
}

func TestRedactComment(t *testing.T) {
	f := newRedactFixture(t)
	feed := &activities_model.Action{OpType: activities_model.ActionCommentIssue, RepoID: f.repo.ID, ActUserID: 3, CommentID: f.comment.ID, Content: fmt.Sprintf(`["%d","%s"]`, f.issue.Index, f.comment.Content)}
	require.NoError(t, db.Insert(t.Context(), feed))

	require.NoError(t, f.redact(t, RedactTarget{Comment: f.comment.ID}, readBoth))

	c := unittest.AssertExistsAndLoadBean(t, &issues_model.Comment{ID: f.comment.ID})
	assert.Equal(t, "in a comment: "+RedactedMark, c.Content)
	assert.NotContains(t, unittest.AssertExistsAndLoadBean(t, &activities_model.Action{ID: feed.ID}).Content, f.tok)
	var history []*issues_model.ContentHistory
	require.NoError(t, db.GetEngine(t.Context()).Where("comment_id = ?", c.ID).OrderBy("id").Find(&history))
	require.Len(t, history, 2, "a text never edited gets an empty first version, so it shows as edited")
	assert.True(t, history[0].IsDeleted)
	assert.Empty(t, history[0].ContentText)
	assert.True(t, history[0].IsFirstCreated)
	assert.Equal(t, c.Content, history[1].ContentText)
	for _, l := range placesOf(t, f.alert.ID, secretscan_model.KindComment, "") {
		assert.NotZero(t, l.RemovedUnix)
	}
	assert.Contains(t, unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: f.issue.ID}).Content, f.tok)
}

func TestRedactRefusals(t *testing.T) {
	f := newRedactFixture(t)
	body := RedactTarget{Issue: f.issue.Index, Field: "body"}
	unchanged := func(t *testing.T) {
		t.Helper()
		assert.Equal(t, f.issue.Content, unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: f.issue.ID}).Content)
	}

	t.Run("a text the doer can't read is missing", func(t *testing.T) {
		require.ErrorIs(t, f.redact(t, body, secretscan_model.TextAccess{}), util.ErrNotExist)
		require.ErrorIs(t, f.redact(t, body, secretscan_model.TextAccess{Pulls: true}), util.ErrNotExist)
		unchanged(t)
	})
	t.Run("targets that name no text", func(t *testing.T) {
		for _, target := range []RedactTarget{{}, {Issue: f.issue.Index}, {Issue: f.issue.Index, Field: "path"}, {Comment: f.comment.ID, Field: "body"}, {Issue: f.issue.Index, Field: "title", Comment: f.comment.ID}} {
			require.ErrorIs(t, f.redact(t, target, readBoth), ErrRedactTarget, "%+v", target)
		}
	})
	t.Run("unknown texts and texts without a place of the alert are missing", func(t *testing.T) {
		require.ErrorIs(t, f.redact(t, RedactTarget{Issue: 99999, Field: "body"}, readBoth), util.ErrNotExist)
		require.ErrorIs(t, f.redact(t, RedactTarget{Comment: 99999999}, readBoth), util.ErrNotExist)
		foreign := newComment(t, newIssue(t, unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 2}), "t", "b"), f.tok) // in another repository
		require.ErrorIs(t, f.redact(t, RedactTarget{Comment: foreign.ID}, readBoth), util.ErrNotExist)
		plain := newIssue(t, f.repo, "plain", "no secret, but "+f.tok[:10])
		require.ErrorIs(t, f.redact(t, RedactTarget{Issue: plain.Index, Field: "body"}, readBoth), util.ErrNotExist)
	})
	t.Run("an alert the doer can't see", func(t *testing.T) {
		err := RedactText(t.Context(), f.repo, f.doer, 99999, body, readBoth, true)
		require.ErrorIs(t, err, util.ErrNotExist)
	})
	t.Run("archived", func(t *testing.T) {
		archived := *f.repo
		archived.IsArchived = true
		require.ErrorIs(t, RedactText(t.Context(), &archived, f.doer, f.alert.Index, body, readBoth, true), ErrArchived)
		unchanged(t)
	})
	t.Run("an edit while it reads the text", func(t *testing.T) {
		during := func(query string) func() {
			return test.MockVariableValue(&scanContent, func(d *secretdetection.Detector, content []byte) ([]secretdetection.Finding, bool) {
				_, err := db.GetEngine(t.Context()).Exec(query, f.issue.ID)
				require.NoError(t, err)
				return d.Scan(content)
			})
		}
		restore := during("UPDATE issue SET content_version = content_version + 1 WHERE id = ?")
		require.ErrorIs(t, f.redact(t, body, readBoth), ErrRedactChanged)
		restore()
		unchanged(t)
		restore = during("UPDATE issue SET name = name || ' (edited)' WHERE id = ?")
		require.ErrorIs(t, f.redact(t, RedactTarget{Issue: f.issue.Index, Field: "title"}, readBoth), ErrRedactChanged)
		restore()
		assert.Equal(t, f.issue.Title+" (edited)", unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: f.issue.ID}).Title, "the other edit is kept")
		n, err := db.GetEngine(t.Context()).Where("action = ?", "repo.secret_alert.text_redacted").Count(new(audit_model.Entry))
		require.NoError(t, err)
		assert.Zero(t, n, "nothing is recorded")
		for _, l := range locationsOf(t, f.alert.ID) {
			assert.Zero(t, l.RemovedUnix)
		}
	})
	t.Run("the text changed since: the secret is gone", func(t *testing.T) {
		setText(t, "comment", f.comment.ID, "content", "edited out")
		require.ErrorIs(t, f.redact(t, RedactTarget{Comment: f.comment.ID}, readBoth), ErrRedactGone)
	})
	t.Run("a text too long to read whole", func(t *testing.T) {
		long := strings.Repeat("x", 5000) + f.tok // past the size the detector reads: not found, as by the scan
		setText(t, "comment", f.comment.ID, "content", long)
		require.ErrorIs(t, f.redact(t, RedactTarget{Comment: f.comment.ID}, readBoth), ErrRedactManual)
	})
}
