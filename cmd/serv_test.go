// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package cmd

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	asymkey_model "forgejo.org/models/asymkey"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/log"
	"forgejo.org/modules/private"
	repo_module "forgejo.org/modules/repository"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"
	"forgejo.org/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetKeyCheckMessage(t *testing.T) {
	for _, tt := range []struct {
		name        string
		key         *asymkey_model.PublicKey
		user        *user_model.User
		appName     string
		expectedMsg string
	}{
		{
			name: "deploy key",
			key: &asymkey_model.PublicKey{
				Type:    asymkey_model.KeyTypeDeploy,
				Name:    "deploy-test-key",
				Content: "key contents",
			},
			user: &user_model.User{
				Name: "test-user",
			},
			appName: "Codeberg",
			expectedMsg: "Hi there! You've successfully authenticated with the deploy key named " +
				"deploy-test-key, but Codeberg does not provide shell access.\nIf this is " +
				"unexpected, please log in with password and setup Codeberg under another user.",
		},
		{
			name: "principal",
			key: &asymkey_model.PublicKey{
				Type:    asymkey_model.KeyTypePrincipal,
				Name:    "principal-test-key",
				Content: "key contents",
			},
			user: &user_model.User{
				Name: "test-user",
			},
			appName: "Forgejo: Beyond coding. We forge.",
			expectedMsg: "Hi there! You've successfully authenticated with the principal " +
				"key contents, but this Forgejo instance does not provide shell access.\n" +
				"If this is unexpected, please log in with password and setup this Forgejo " +
				"instance under another user.",
		},
		{
			name: "user",
			key: &asymkey_model.PublicKey{
				Type:    asymkey_model.KeyTypeUser,
				Name:    "user-test-key",
				Content: "key contents",
			},
			user: &user_model.User{
				Name: "test-user",
			},
			appName: "Social Coding",
			expectedMsg: "Hi there, test-user! You've successfully authenticated with the key " +
				"named user-test-key, but Social Coding does not provide shell access.\n" +
				"If this is unexpected, please log in with password and setup Social Coding " +
				"under another user.",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gotMsg := getKeyCheckMessage(tt.key, tt.user, tt.appName)
			if gotMsg != tt.expectedMsg {
				log.Fatal("expected key success message '%s' but got '%s'.", tt.expectedMsg, gotMsg)
			}
		})
	}
}

func TestLFSTokenExpiry(t *testing.T) {
	defer test.MockVariableValue(&setting.LFS.HTTPAuthExpiry, 24*time.Hour)()
	now := time.Unix(1_800_000_000, 0)

	assert.Equal(t, now.Add(24*time.Hour).Unix(), lfsTokenExpiry(now, 0).Unix(), "key without expiry")
	assert.Equal(t, now.Add(time.Hour).Unix(), lfsTokenExpiry(now, timeutil.TimeStamp(now.Add(time.Hour).Unix())).Unix(), "capped at the key expiry")
	assert.Equal(t, now.Add(24*time.Hour).Unix(), lfsTokenExpiry(now, timeutil.TimeStamp(now.Add(48*time.Hour).Unix())).Unix(), "key expires after the token")
}

// AC7: SSH takes the client IP from SSH_CONNECTION, which sshd and the built-in server set
func TestSSHClientIP(t *testing.T) {
	assert.Equal(t, "198.51.100.7", sshClientIP("198.51.100.7 51234 10.0.0.1 22"))
	assert.Equal(t, "2001:db8::1", sshClientIP("2001:db8::1 51234 2001:db8::2 22"))
	assert.Empty(t, sshClientIP(""))
	assert.Empty(t, sshClientIP("   "))
}

// an SSH path names `owner/repo` or `owner/project/repo`; anything else is refused before the server is asked
func TestSplitServRepoPath(t *testing.T) {
	for path, want := range map[string][2]string{
		"user2/repo1":            {"user2", "repo1"},
		"user2/repo1.git":        {"user2", "repo1"},
		"user2/repo1.wiki.git":   {"user2", "repo1.wiki"},
		"org3/proj/api":          {"org3", "proj/api"},
		"org3/proj/api.git":      {"org3", "proj/api"},
		"org3/proj/api.wiki.git": {"org3", "proj/api.wiki"},
	} {
		owner, repo, err := splitServRepoPath(path)
		require.NoError(t, err, path)
		assert.Equal(t, want, [2]string{owner, repo}, path)
	}
	for path, msg := range map[string]string{
		"user2":                 "Invalid repository path",
		"org3/a/b/c.git":        "Invalid repository path",
		"user2/":                "Invalid repo name",
		"user2/.git":            "Invalid repo name",
		"org3//api.git":         "Invalid repo name",
		"org3/@5.git":           "Invalid repo name",
		"org3/proj/@5.git":      "Invalid repo name",
		"org3/proj%2Fapi/x.git": "Invalid repo name",
		"org3/pr oj/api.git":    "Invalid repo name",
	} {
		_, _, err := splitServRepoPath(path)
		require.ErrorContains(t, err, msg, path)
	}
}

// git runs on the repository the server found, never on the path the client sent
func TestServRepoPath(t *testing.T) {
	defer test.MockVariableValue(&setting.RepoRootPath, "/data/repos")()
	defer test.MockVariableValue(&setting.AppURL, "https://example.com/")()

	p, err := servRepoPath(&private.ServCommandResults{OwnerName: "User2", RepoName: "Repo1"})
	require.NoError(t, err)
	assert.Equal(t, filepath.FromSlash("/data/repos/user2/repo1.git"), p)
	p, err = servRepoPath(&private.ServCommandResults{OwnerName: "org3", GroupName: "Proj", RepoName: "API"})
	require.NoError(t, err)
	assert.Equal(t, filepath.FromSlash("/data/repos/org3/proj/api.git"), p)
	p, err = servRepoPath(&private.ServCommandResults{OwnerName: "org3", GroupName: "proj", RepoName: "api", IsWiki: true})
	require.NoError(t, err)
	assert.Equal(t, filepath.FromSlash("/data/repos/org3/proj/api.wiki.git"), p)

	for _, r := range []private.ServCommandResults{
		{OwnerName: "org3", GroupName: "..", RepoName: "x/../../etc"},
		{OwnerName: "org3", GroupName: "a/b", RepoName: "api"},
		{OwnerName: "../org3", RepoName: "api"},
		{OwnerName: "org3", RepoName: ""},
		{OwnerName: "", RepoName: "api"},
	} {
		_, err := servRepoPath(&r)
		require.Error(t, err, "%+v", r)
	}

	assert.Equal(t, "https://example.com/user2/repo1.git/info/lfs", servLFSHref(&private.ServCommandResults{OwnerName: "user2", RepoName: "repo1"}))
	assert.Equal(t, "https://example.com/org3/proj/api.git/info/lfs", servLFSHref(&private.ServCommandResults{OwnerName: "org3", GroupName: "proj", RepoName: "api"}))
}

func TestServGitEnvAudit(t *testing.T) {
	t.Setenv(repo_module.EnvAuditIP, "203.0.113.9")
	t.Setenv(repo_module.EnvAuditRequestID, "forged")
	lastEnv := func(env []string, key string) string {
		v := "<unset>"
		for _, kv := range env {
			if s, ok := strings.CutPrefix(kv, key+"="); ok {
				v = s
			}
		}
		return v
	}

	env := servGitEnv(&private.ServCommandResults{KeyID: 3, UserID: 2, RequestID: "req"}, "192.0.2.1")
	assert.Equal(t, "ssh_key:3", lastEnv(env, repo_module.EnvAuditCredential))
	assert.Equal(t, "192.0.2.1", lastEnv(env, repo_module.EnvAuditIP))
	assert.Equal(t, "req", lastEnv(env, repo_module.EnvAuditRequestID))

	env = servGitEnv(&private.ServCommandResults{KeyID: 3, DeployKeyKeyID: 3, UserID: 2}, "")
	assert.Empty(t, lastEnv(env, repo_module.EnvAuditCredential)) // the hook resolves the deploy key itself
	assert.Empty(t, lastEnv(env, repo_module.EnvAuditIP))
	assert.Empty(t, lastEnv(env, repo_module.EnvAuditRequestID))
}
