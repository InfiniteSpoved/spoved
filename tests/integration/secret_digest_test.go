// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	audit_model "forgejo.org/models/audit"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	"forgejo.org/models/perm"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	secretscan_model "forgejo.org/models/secretscan"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	"forgejo.org/modules/setting"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/test"
	"forgejo.org/services/mailer"
	secretscan_service "forgejo.org/services/secretscan"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A push marks the repository, the scan after it mails the repository's administrators and the pusher, and what the
// mail says is types, counts and places: the secret and its fingerprint are in no byte of it.
func TestSecretDigestAfterPush(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		defer test.MockVariableValue(&setting.SecretScanning.Enabled, true)()
		sys := audit_model.WithSystem(db.DefaultContext, "test")
		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		repo := depNewRepo(t, user2, user2, "digest-push", false, 0)
		require.NoError(t, db.Insert(sys, &repo_model.Collaboration{RepoID: repo.ID, UserID: 5, Mode: perm.AccessModeWrite}))
		require.NoError(t, db.Insert(sys, &access_model.Access{UserID: 5, RepoID: repo.ID, Mode: perm.AccessModeWrite}))
		for _, id := range []int64{2, 5} {
			_, err := db.GetEngine(sys).Exec("UPDATE `user` SET email_notifications_preference = 'enabled' WHERE id = ?", id)
			require.NoError(t, err)
		}
		// the first scan: the baseline, no mail
		require.NoError(t, secretscan_service.Scan(sys, repo.ID))
		require.NotNil(t, scanRow(t, repo.ID))

		var mu sync.Mutex
		var sent []*mailer.Message
		defer test.MockVariableValue(&mailer.SendAsync, func(msgs ...*mailer.Message) {
			mu.Lock()
			defer mu.Unlock()
			sent = append(sent, msgs...)
		})()
		mailsTo := func() map[string]*mailer.Message {
			mu.Lock()
			defer mu.Unlock()
			by := map[string]*mailer.Message{}
			for _, m := range sent {
				if !strings.Contains(m.Subject, "digest-push") {
					continue
				}
				addr, err := mail.ParseAddress(m.To)
				require.NoError(t, err)
				by[addr.Address] = m
			}
			return by
		}

		// user5 pushes a secret and a file name that is no secret
		canary := "ghp" + "_" + "Zq8Yx7Wv6Ut5" + "Sr4Qp3On2Ml1Kj0" + "Ih9Gf8Ed7Cb6"
		dir := t.TempDir()
		clone := *u
		clone.User = url.UserPassword("user5", userPassword)
		clone.Path = "/user2/digest-push.git"
		require.NoError(t, git.CloneWithArgs(t.Context(), nil, clone.String(), dir, git.CloneRepoOptions{}))
		doGitSetRemoteURL(dir, "origin", &clone)(t) // the clone drops the credentials
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "config"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config", "prod.env"), []byte("export TOKEN="+canary+"\n"), 0o644))
		for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=U5", "-c", "user.email=user5@example.com", "commit", "-q", "-m", "add config"}, {"push", "-q", "origin", "HEAD:main"}} {
			_, _, err := git.NewCommand(t.Context()).AddArguments(git.ToTrustedCmdArgs(args)...).RunStdString(&git.RunOpts{Dir: dir})
			require.NoError(t, err)
		}

		require.Eventually(t, func() bool { return len(mailsTo()) >= 2 }, 20*time.Second, 100*time.Millisecond)
		got := mailsTo()
		admin, pusher := got["user2@example.com"], got["user5@example.com"]
		require.NotNil(t, admin, "%v", got)
		require.NotNil(t, pusher, "%v", got)
		assert.Equal(t, "New secret found in user2/digest-push", admin.Subject)
		assert.Equal(t, "Your push to user2/digest-push contained a secret", pusher.Subject)
		assert.Contains(t, admin.Body, "config/prod.env:1")
		assert.Contains(t, admin.Body, "(commit ")
		assert.Contains(t, admin.Body, "/user2/digest-push/security/secrets")
		assert.Len(t, got, 2)
		for _, m := range got {
			for _, needle := range []string{canary, strings.TrimPrefix(canary, "ghp"+"_"), canary[:16]} {
				assert.NotContains(t, m.Subject+m.Body, needle, "no secret byte in the mail")
			}
			for _, a := range alertRows(t, repo.ID) {
				assert.NotContains(t, m.Subject+m.Body, a.Fingerprint, "no fingerprint in the mail")
			}
		}
		require.Eventually(t, func() bool { return scanRow(t, repo.ID).NotifyAfterUnix == 0 }, 5*time.Second, 50*time.Millisecond)
		time.Sleep(500 * time.Millisecond)
		assert.Len(t, mailsTo(), 2, "one digest per push")
	})
}

// The mail can be switched off in the notification settings, per event.
func TestSecretDigestNotificationSetting(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	session := loginUser(t, "user2")
	page := NewHTMLParser(t, session.MakeRequest(t, NewRequest(t, "GET", "/user/settings/notifications"), http.StatusOK).Body)
	page.AssertElement(t, `input[name="event_secret_alert"]`, true)
	assert.Contains(t, page.Find("body").Text(), "A scan finds new secrets in a repository I administer or push to")
	token := getUserToken(t, "user2", auth_model.AccessTokenScopeReadUser, auth_model.AccessTokenScopeWriteUser)
	var got api.NotificationSettings
	DecodeJSON(t, MakeRequest(t, NewRequest(t, "GET", "/api/v1/user/settings/notifications").AddTokenAuth(token), http.StatusOK), &got)
	assert.Contains(t, got.Events, "secret_alert")
}

func scanRow(t *testing.T, repoID int64) *secretscan_model.Scan {
	t.Helper()
	s, err := secretscan_model.GetScan(t.Context(), repoID, secretscan_model.KindRepo)
	require.NoError(t, err)
	return s
}

func alertRows(t *testing.T, repoID int64) []*secretscan_model.Alert {
	t.Helper()
	var alerts []*secretscan_model.Alert
	require.NoError(t, db.GetEngine(t.Context()).Where("repo_id = ?", repoID).Find(&alerts))
	return alerts
}
