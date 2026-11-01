// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretscan

import (
	"context"
	"testing"
	"time"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	secretscan_model "forgejo.org/models/secretscan"
	"forgejo.org/modules/test"
	"forgejo.org/services/mailer"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureDigests replaces the mailer with a list of what it was asked to send.
func captureDigests(t *testing.T) *[]mailer.SecretDigestInput {
	t.Helper()
	var got []mailer.SecretDigestInput
	t.Cleanup(test.MockVariableValue(&sendDigest, func(_ context.Context, _ *repo_model.Repository, in mailer.SecretDigestInput) error {
		got = append(got, in)
		return nil
	}))
	return &got
}

// A push marks the repository, the scan after it mails the alerts it opened once, and a push that opened none, the
// first scan and a scan that goes on in a later slice don't mail.
func TestDigestAfterPush(t *testing.T) {
	repo := enable(t)
	sent := captureDigests(t)
	w := newWork(t, repo)
	ctx := t.Context()

	// the first scan: the row doesn't exist yet, so nothing is marked and nothing is mailed
	MarkPush(ctx, repo, 2, "main", "c0")
	w.commit(map[string]string{"first.env": "T=" + token("first") + "\n"})
	w.pushDefault()
	require.NoError(t, Scan(ctx, repo.ID))
	assert.Empty(t, *sent, "the first scan never mails")
	assert.Zero(t, scanOf(t, repo.ID, secretscan_model.KindRepo).NotifyAfterUnix)
	require.Len(t, alertsOf(t, repo.ID), 1)
	boundary := alertsOf(t, repo.ID)[0].ID

	// a push without a secret: the mark is cleared, nothing is mailed
	MarkPush(ctx, repo, 2, "main", "c1")
	assert.NotZero(t, scanOf(t, repo.ID, secretscan_model.KindRepo).NotifyAfterUnix)
	w.commit(map[string]string{"plain.txt": "nothing\n"})
	w.pushDefault()
	require.NoError(t, Scan(ctx, repo.ID))
	assert.Empty(t, *sent)
	assert.Zero(t, scanOf(t, repo.ID, secretscan_model.KindRepo).NotifyAfterUnix, "the scan took the mark")

	// a push with a secret: one digest, with the boundary and the pusher
	MarkPush(ctx, repo, 2, "main", "c2")
	w.commit(map[string]string{"second.env": "T=" + token("second") + "\n"})
	w.pushDefault()
	require.NoError(t, Scan(ctx, repo.ID))
	require.Len(t, *sent, 1)
	assert.Equal(t, mailer.SecretDigestInput{Ref: "main", Commit: "c2", PusherID: 2, AfterAlertID: boundary}, (*sent)[0])
	require.NoError(t, Scan(ctx, repo.ID))
	require.NoError(t, Scan(ctx, repo.ID))
	assert.Len(t, *sent, 1, "at most once: the mark is gone")
}

func TestDigestMarkRules(t *testing.T) {
	repo := enable(t)
	sent := captureDigests(t)
	w := newWork(t, repo)
	ctx := t.Context()
	w.commit(map[string]string{"a.txt": "x\n"})
	w.pushDefault()
	require.NoError(t, Scan(ctx, repo.ID))
	mark := func() *secretscan_model.Scan { return scanOf(t, repo.ID, secretscan_model.KindRepo) }

	t.Run("nothing is marked while scanning is off for the repository", func(t *testing.T) {
		_, err := db.GetEngine(ctx).Exec("UPDATE repository SET secret_scanning_disabled = ? WHERE id = ?", true, repo.ID)
		require.NoError(t, err)
		off, err := repo_model.GetRepositoryByID(ctx, repo.ID)
		require.NoError(t, err)
		MarkPush(ctx, off, 2, "main", "c")
		assert.Zero(t, mark().NotifyAfterUnix)
		_, err = db.GetEngine(ctx).Exec("UPDATE repository SET secret_scanning_disabled = ? WHERE id = ?", false, repo.ID)
		require.NoError(t, err)
	})

	t.Run("a backfill or a retry under way is not marked", func(t *testing.T) {
		_, err := db.GetEngine(ctx).Exec("UPDATE secret_scan SET retry_unix = ? WHERE repo_id = ?", time.Now().Unix()+3600, repo.ID)
		require.NoError(t, err)
		MarkPush(ctx, repo, 2, "main", "c")
		assert.Zero(t, mark().NotifyAfterUnix)
		_, err = db.GetEngine(ctx).Exec("UPDATE secret_scan SET retry_unix = 0 WHERE repo_id = ?", repo.ID)
		require.NoError(t, err)
	})

	t.Run("a push that comes while a scan runs keeps its mark", func(t *testing.T) {
		MarkPush(ctx, repo, 2, "main", "c1")
		m := readMark(ctx, repo.ID)
		require.NotNil(t, m)
		MarkPush(ctx, repo, 2, "main", "c2") // after the scan read the mark
		w.commit(map[string]string{"b.env": "T=" + token("racing") + "\n"})
		w.pushDefault()
		finishDigest(ctx, repo, m)
		assert.Empty(t, *sent, "the clear was refused")
		assert.NotZero(t, mark().NotifyAfterUnix)
		require.NoError(t, Scan(ctx, repo.ID))
		require.Len(t, *sent, 1, "the next scan mails it")
		assert.Equal(t, "c2", (*sent)[0].Commit)
	})

	t.Run("a scan that isn't complete waits for the last slice", func(t *testing.T) {
		*sent = nil
		MarkPush(ctx, repo, 2, "main", "c3")
		w.commit(map[string]string{"c.env": "T=" + token("slices") + "\n"})
		w.pushDefault()
		m := readMark(ctx, repo.ID)
		_, err := db.GetEngine(ctx).Exec("UPDATE secret_scan SET retry_unix = ? WHERE repo_id = ? AND kind = ?", time.Now().Unix()+3600, repo.ID, secretscan_model.KindPull)
		require.NoError(t, err)
		finishDigest(ctx, repo, m)
		assert.Empty(t, *sent)
		assert.NotZero(t, mark().NotifyAfterUnix, "the mark stays")
		_, err = db.GetEngine(ctx).Exec("UPDATE secret_scan SET retry_unix = 0 WHERE repo_id = ?", repo.ID)
		require.NoError(t, err)
		require.NoError(t, Scan(ctx, repo.ID))
		assert.Len(t, *sent, 1)
	})

	t.Run("an old mark is dropped without a mail", func(t *testing.T) {
		*sent = nil
		MarkPush(ctx, repo, 2, "main", "c4")
		_, err := db.GetEngine(ctx).Exec("UPDATE secret_scan SET notify_after_unix = ? WHERE repo_id = ? AND kind = ?", time.Now().Add(-48*time.Hour).Unix(), repo.ID, secretscan_model.KindRepo)
		require.NoError(t, err)
		w.commit(map[string]string{"d.env": "T=" + token("old") + "\n"})
		w.pushDefault()
		require.NoError(t, Scan(ctx, repo.ID))
		assert.Empty(t, *sent)
		assert.Zero(t, mark().NotifyAfterUnix)
	})

	t.Run("a rescan removes the mark", func(t *testing.T) {
		MarkPush(ctx, repo, 2, "main", "c5")
		require.NotZero(t, mark().NotifyAfterUnix)
		_, err := secretscan_model.DeleteScans(ctx, repo.ID)
		require.NoError(t, err)
		*sent = nil
		require.NoError(t, Scan(ctx, repo.ID))
		assert.Empty(t, *sent, "the rescan is a first scan again")
	})
}
