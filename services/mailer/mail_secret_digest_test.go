// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package mailer

import (
	"fmt"
	"net/mail"
	"strings"
	"testing"

	"forgejo.org/models/db"
	"forgejo.org/models/perm"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	secretscan_model "forgejo.org/models/secretscan"
	"forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSendSecretDigest(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()
	var messages []*Message
	defer MockMailSettings(func(msgs ...*Message) { messages = append(messages, msgs...) })()

	// repository 1 is user2's. user4 administers it too, user5 writes to it, user10 only reads it
	collab := func(userID int64, mode perm.AccessMode) {
		t.Helper()
		require.NoError(t, db.Insert(ctx, &repo_model.Collaboration{RepoID: 1, UserID: userID, Mode: mode}))
		require.NoError(t, db.Insert(ctx, &access_model.Access{UserID: userID, RepoID: 1, Mode: mode}))
	}
	collab(4, perm.AccessModeAdmin)
	collab(5, perm.AccessModeWrite)
	collab(10, perm.AccessModeRead)
	setPref := func(id int64, pref string) {
		t.Helper()
		_, err := db.GetEngine(ctx).Exec("UPDATE `user` SET email_notifications_preference = ? WHERE id = ?", pref, id)
		require.NoError(t, err)
	}
	repo := func() *repo_model.Repository {
		r, err := repo_model.GetRepositoryByID(ctx, 1)
		require.NoError(t, err)
		return r
	}

	var boundary int64
	alert := func(index int64, rule string, places int, kinds ...string) {
		t.Helper()
		a := &secretscan_model.Alert{RepoID: 1, Index: index, RuleID: rule, Fingerprint: fmt.Sprintf("%064d", index*7919), LocationCount: places}
		require.NoError(t, db.Insert(ctx, a))
		for i, kind := range kinds {
			l := secretscan_model.NewLocation(1, kind, fmt.Sprintf("blob-%d-%d", index, i), fmt.Sprintf("config/file%d.env", index), i+10, i+10, "c", "n", "e", 0)
			l.AlertID = a.ID
			require.NoError(t, db.Insert(ctx, l))
		}
	}
	send := func(in SecretDigestInput) map[string]*Message {
		t.Helper()
		messages = nil
		in.AfterAlertID = boundary
		require.NoError(t, SendSecretDigest(ctx, repo(), in))
		by := map[string]*Message{}
		for _, m := range messages {
			addr, err := mail.ParseAddress(m.To)
			require.NoError(t, err)
			by[addr.Address] = m
		}
		return by
	}
	user2, user4, user5 := "user2@example.com", "user4@example.com", "user5@example.com"
	for _, id := range []int64{2, 4, 5} {
		setPref(id, user_model.EmailNotificationsEnabled)
	}

	alert(1, "github-pat-classic", 1, secretscan_model.KindRepo)
	_, err := db.GetEngine(ctx).Table("secret_scanning_alert").Select("MAX(id)").Get(&boundary)
	require.NoError(t, err)

	t.Run("nothing new: no mail", func(t *testing.T) {
		assert.Empty(t, send(SecretDigestInput{Ref: "main", Commit: "1a2b3c4d5e6f"}))
	})

	alert(2, "aws-access-key-id", 1, secretscan_model.KindRepo)
	t.Run("one secret: singular subject, admins and the pusher with their own subject", func(t *testing.T) {
		got := send(SecretDigestInput{Ref: "main", Commit: "1a2b3c4d5e6f", PusherID: 5})
		require.Len(t, got, 3, "%v", got)
		admin := got[user2]
		require.NotNil(t, admin)
		assert.Equal(t, "New secret found in user2/repo1", admin.Subject)
		assert.Contains(t, admin.Body, "A scan of main (commit 1a2b3c4) found 1 secret that was not in the alert list yet.")
		assert.Contains(t, admin.Body, "config/file2.env:10")
		assert.Contains(t, admin.Body, `href="https://try.gitea.io/user2/repo1/security/secrets"`)
		assert.Contains(t, admin.Body, "View secret alert<")
		assert.Contains(t, admin.Body, "You receive this because you can manage secret alerts in user2/repo1.")
		assert.NotContains(t, admin.Body, "Your push")
		assert.Contains(t, got[user4].Subject, "New secret found")
		pusher := got[user5]
		require.NotNil(t, pusher)
		assert.Equal(t, "Your push to user2/repo1 contained a secret", pusher.Subject)
		assert.Contains(t, pusher.Body, "Your push to main (commit 1a2b3c4) contains 1 secret.")
		assert.Contains(t, pusher.Body, "You receive this because you pushed these commits to user2/repo1.")
		assert.NotContains(t, pusher.Body, "administrator")
		for _, m := range got {
			assert.NotContains(t, m.Subject+m.Body, fmt.Sprintf("%064d", 2*7919), "no fingerprint")
			assert.NotContains(t, m.Subject+m.Body, "GitHub", "an alert below the boundary is not in it")
		}
	})

	t.Run("who is left out", func(t *testing.T) {
		got := send(SecretDigestInput{Ref: "main", Commit: "1a2b3c4d5e6f", PusherID: 10})
		assert.NotContains(t, got, "user10@example.com", "a reader isn't told: they can't see alerts")
		assert.NotContains(t, got, user5, "a writer who didn't push gets no mail")
		got = send(SecretDigestInput{Ref: "main", Commit: "1a2b3c4d5e6f", PusherID: 2})
		assert.Len(t, got, 2, "a pusher who administers the repository gets the digest only: %v", got)
		assert.NotContains(t, got[user2].Subject, "Your push")
		got = send(SecretDigestInput{Ref: "main", Commit: "1a2b3c4d5e6f", PusherID: -2})
		assert.Len(t, got, 2, "nobody pushed (an Actions task or a deploy key)")
		got = send(SecretDigestInput{Ref: "main", Commit: "1a2b3c4d5e6f", PusherID: 99999})
		assert.Len(t, got, 2, "a pusher who is gone")

		setPref(4, user_model.EmailNotificationsDisabled)
		assert.NotContains(t, send(SecretDigestInput{Ref: "main"}), user4, "mail turned off")
		setPref(4, user_model.EmailNotificationsOnMention)
		assert.Contains(t, send(SecretDigestInput{Ref: "main"}), user4, "a security mail isn't a mention-only preset's to drop")
		setPref(4, user_model.EmailNotificationsCustom)
		require.NoError(t, user_model.SaveNotificationSettings(ctx, 4, 0, map[user_model.NotifyEvent]bool{user_model.NotifyEventSecretAlert: false}))
		assert.NotContains(t, send(SecretDigestInput{Ref: "main"}), user4, "Custom with the event ticked off")
		require.NoError(t, user_model.SaveNotificationSettings(ctx, 4, 0, map[user_model.NotifyEvent]bool{user_model.NotifyEventSecretAlert: true}))
		assert.Contains(t, send(SecretDigestInput{Ref: "main"}), user4, "Custom with the event")
		setPref(4, user_model.EmailNotificationsEnabled)
		_, err := db.GetEngine(ctx).Exec("UPDATE `user` SET prohibit_login = ? WHERE id = 4", true)
		require.NoError(t, err)
		assert.NotContains(t, send(SecretDigestInput{Ref: "main"}), user4, "an account that can't sign in")
		_, err = db.GetEngine(ctx).Exec("UPDATE `user` SET prohibit_login = ? WHERE id = 4", false)
		require.NoError(t, err)
	})

	t.Run("several secrets, the cap and the wiki", func(t *testing.T) {
		alert(3, "github-token", 1, secretscan_model.KindWiki)
		alert(4, "aws-access-key-id", 3, secretscan_model.KindRepo, secretscan_model.KindRepo, secretscan_model.KindRepo)
		alert(5, "aws-access-key-id", 14, secretscan_model.KindRepo, secretscan_model.KindRepo, secretscan_model.KindRepo, secretscan_model.KindRepo, secretscan_model.KindRepo, secretscan_model.KindRepo, secretscan_model.KindRepo, secretscan_model.KindRepo)
		got := send(SecretDigestInput{Ref: "main", Commit: "1a2b3c4d5e6f", PusherID: 5})
		admin := got[user2]
		require.NotNil(t, admin)
		assert.Equal(t, "New secrets found in user2/repo1", admin.Subject)
		assert.Contains(t, admin.Body, "found 4 secrets that were not in the alert list yet.")
		assert.Contains(t, admin.Body, "Locations (10 of 19)")
		assert.Contains(t, admin.Body, "…and 9 more locations in the alerts.")
		assert.Equal(t, 10, strings.Count(admin.Body, "<li><code>"))
		assert.Contains(t, admin.Body, "(wiki)", "the wiki's place is listed for someone who reads the wiki")
		assert.Equal(t, "Your push to user2/repo1 contained secrets", got[user5].Subject)
		assert.Contains(t, got[user5].Body, "View secret alerts<")

		// the wiki unit off: nobody reads it, so the alert that is only there is not counted or listed
		_, err := db.GetEngine(ctx).Exec("DELETE FROM repo_unit WHERE repo_id = 1 AND type = ?", unit.TypeWiki)
		require.NoError(t, err)
		got = send(SecretDigestInput{Ref: "main", Commit: "1a2b3c4d5e6f"})
		admin = got[user2]
		require.NotNil(t, admin)
		assert.Contains(t, admin.Body, "found 3 secrets that were not")
		assert.NotContains(t, admin.Body, "(wiki)")
		assert.Contains(t, admin.Body, "Locations (10 of 18)")
	})

	t.Run("a wiki push names the wiki", func(t *testing.T) {
		got := send(SecretDigestInput{})
		assert.Contains(t, got[user2].Body, "A scan of the wiki found")
	})
}
