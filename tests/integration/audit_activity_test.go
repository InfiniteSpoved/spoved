// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	actions_model "forgejo.org/models/actions"
	asymkey_model "forgejo.org/models/asymkey"
	audit_model "forgejo.org/models/audit"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/git"
	"forgejo.org/modules/gitrepo"
	"forgejo.org/modules/private"
	"forgejo.org/modules/setting"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/test"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// auditActivity returns the one entry of action written after since.
func auditActivity(t *testing.T, since int64, action string) *audit_model.Entry {
	t.Helper()
	entries := auditActions(t, since, action)
	require.Len(t, entries, 1, action)
	return entries[0]
}

func TestAuditAPIWrite(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	auditFresh(t)
	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteIssue, auth_model.AccessTokenScopeWriteRepository)
	tok, err := auth_model.GetAccessTokenBySHA(t.Context(), token)
	require.NoError(t, err)

	t.Run("repository write", func(t *testing.T) {
		since := auditMaxID(t)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/repo1/issues?secret=q", &api.CreateIssueOption{Title: "audit"}).AddTokenAuth(token), http.StatusCreated)
		e := auditActivity(t, since, "activity.api.write")
		assert.Equal(t, audit_model.CategoryActivity, e.Category)
		assert.Equal(t, audit_model.ResultSuccess, e.Result)
		assert.Equal(t, audit_model.CredentialAccessToken, e.CredentialType)
		assert.Equal(t, tok.ID, e.CredentialID)
		assert.EqualValues(t, 2, e.ActorID)
		assert.EqualValues(t, 1, e.RepoID)
		assert.EqualValues(t, 2, e.OwnerID)
		assert.Equal(t, "user2/repo1", e.TargetName)
		data := auditData(t, e)
		assert.Equal(t, "POST", data["method"])
		assert.Equal(t, "/api/v1/repos/{username}/{reponame}/issues", data["route"])
		assert.EqualValues(t, http.StatusCreated, data["status"])
		assert.Equal(t, "repo", data["target"])
		assertNoSecrets(t, since, token, "secret=q")
	})

	t.Run("missing repository targets the user", func(t *testing.T) {
		since := auditMaxID(t)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/no-such-repo/issues", &api.CreateIssueOption{Title: "audit"}).AddTokenAuth(token), http.StatusNotFound)
		e := auditActivity(t, since, "activity.api.write")
		assert.Equal(t, audit_model.ResultFailure, e.Result)
		assert.Zero(t, e.RepoID)
		assert.EqualValues(t, 2, e.OwnerID)
		assert.Equal(t, "user2", e.TargetName)
		assert.Equal(t, "user", auditData(t, e)["target"])
		assert.EqualValues(t, http.StatusNotFound, auditData(t, e)["status"])
	})

	t.Run("failures keep their status", func(t *testing.T) {
		since := auditMaxID(t)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/repo1/issues", &api.CreateIssueOption{Title: ""}).AddTokenAuth(token), http.StatusUnprocessableEntity)
		assert.EqualValues(t, http.StatusUnprocessableEntity, auditData(t, auditActivity(t, since, "activity.api.write"))["status"])

		since = auditMaxID(t)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/user/repos", &api.CreateRepoOption{Name: "no-scope"}).AddTokenAuth(token), http.StatusForbidden)
		assert.EqualValues(t, http.StatusForbidden, auditData(t, auditActivity(t, since, "activity.api.write"))["status"])
	})

	t.Run("anonymous and reads are not recorded", func(t *testing.T) {
		since := auditMaxID(t)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/repo1/issues", &api.CreateIssueOption{Title: "x"}), http.StatusUnauthorized)
		MakeRequest(t, NewRequest(t, "GET", "/api/v1/repos/user2/repo1/issues").AddTokenAuth(token), http.StatusOK)
		assert.Empty(t, auditActions(t, since, "activity.api.write"))
		assert.Empty(t, auditActions(t, since, "activity.api.sudo"))
	})

	t.Run("sudo read", func(t *testing.T) {
		adminToken := getUserToken(t, "user1", auth_model.AccessTokenScopeReadRepository)
		since := auditMaxID(t)
		MakeRequest(t, NewRequest(t, "GET", "/api/v1/repos/user2/repo1?sudo=user2").AddTokenAuth(adminToken), http.StatusOK)
		e := auditActivity(t, since, "activity.api.sudo")
		assert.EqualValues(t, 2, e.ActorID)
		assert.EqualValues(t, 1, e.SudoByID)
		assert.EqualValues(t, 1, e.RepoID)
		assert.Equal(t, "GET", auditData(t, e)["method"])
	})

	t.Run("a change and its request share the request ID", func(t *testing.T) {
		since := auditMaxID(t)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/repo1/hooks", &api.CreateHookOption{
			Type: "gitea", Config: api.CreateHookOptionConfig{"content_type": "json", "url": "http://example.com/"},
		}).AddTokenAuth(token), http.StatusCreated)
		change := auditActivity(t, since, "repo.webhook.created")
		write := auditActivity(t, since, "activity.api.write")
		assert.NotEmpty(t, write.RequestID)
		assert.Equal(t, change.RequestID, write.RequestID)
	})

	t.Run("job token", func(t *testing.T) {
		task := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionTask{ID: 47})
		task.RepoID, task.OwnerID = 1, 2
		task.GenerateToken()
		require.NoError(t, actions_model.UpdateTask(t.Context(), task, "repo_id", "owner_id", "token", "token_salt", "token_hash", "token_last_eight"))
		since := auditMaxID(t)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/repo1/issues", &api.CreateIssueOption{Title: "job"}).AddTokenAuth(task.Token), http.StatusCreated)
		e := auditActivity(t, since, "activity.api.write")
		assert.Equal(t, audit_model.ActorActions, e.ActorType)
		assert.Equal(t, audit_model.CredentialActionsTask, e.CredentialType)
		assert.Equal(t, task.ID, e.CredentialID)
	})

	t.Run("generic package", func(t *testing.T) {
		since := auditMaxID(t)
		MakeRequest(t, NewRequestWithBody(t, "PUT", "/api/packages/user2/generic/audit-pkg/1.0/file.bin", strings.NewReader("data")).AddBasicAuth("user2"), http.StatusCreated)
		e := auditActivity(t, since, "activity.api.write")
		assert.EqualValues(t, 2, e.OwnerID)
		assert.Zero(t, e.RepoID)
		assert.Equal(t, audit_model.CredentialPassword, e.CredentialType)
		assert.Equal(t, "PUT", auditData(t, e)["method"])
		assert.NotContains(t, auditData(t, e)["route"], "audit-pkg")
	})

	t.Run("container chunks", func(t *testing.T) {
		resp := MakeRequest(t, NewRequest(t, "GET", setting.AppURL+"v2/token").AddBasicAuth("user2"), http.StatusOK)
		tr := &struct {
			Token string `json:"token"`
		}{}
		DecodeJSON(t, resp, tr)
		bearer := "Bearer " + tr.Token
		blob := []byte("audit blob")
		sum := sha256.Sum256(blob)
		digest := "sha256:" + hex.EncodeToString(sum[:])

		since := auditMaxID(t)
		resp = MakeRequest(t, NewRequest(t, "POST", setting.AppURL+"v2/user2/audit-img/blobs/uploads").AddTokenAuth(bearer), http.StatusAccepted)
		uploadURL := resp.Header().Get("Location")
		resp = MakeRequest(t, NewRequestWithBody(t, "PATCH", setting.AppURL+uploadURL[1:], bytes.NewReader(blob)).AddTokenAuth(bearer).
			SetHeader("Content-Range", fmt.Sprintf("0-%d", len(blob)-1)), http.StatusAccepted)
		uploadURL = resp.Header().Get("Location")
		MakeRequest(t, NewRequest(t, "PUT", fmt.Sprintf("%s?digest=%s", setting.AppURL+uploadURL[1:], digest)).AddTokenAuth(bearer), http.StatusCreated)
		var methods []any
		for _, e := range auditActions(t, since, "activity.api.write") {
			methods = append(methods, auditData(t, e)["method"])
		}
		assert.Contains(t, methods, "PUT")
		assert.NotContains(t, methods, "PATCH")
	})

	t.Run("fail open", func(t *testing.T) {
		defer test.MockVariableValue(&audit_model.InsertEntry, func(context.Context, *audit_model.Entry) error { return errors.New("down") })()
		before := audit_model.WriteErrors.Load()
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/repo1/issues", &api.CreateIssueOption{Title: "fail open"}).AddTokenAuth(token), http.StatusCreated)
		assert.Greater(t, audit_model.WriteErrors.Load(), before)
	})
}

// gitURL is u with path and basic auth as user with password (a token or a password).
func gitURL(u *url.URL, path, user, password string) *url.URL {
	c := *u
	c.Path = path
	c.User = url.UserPassword(user, password)
	return &c
}

func TestAuditGitPush(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		auditFresh(t)
		token := getUserToken(t, "user2", auth_model.AccessTokenScopeWriteRepository)
		tok, err := auth_model.GetAccessTokenBySHA(t.Context(), token)
		require.NoError(t, err)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/user/repos", &api.CreateRepoOption{Name: "audit-push", AutoInit: true, DefaultBranch: "main", Readme: "Default"}).AddTokenAuth(getUserToken(t, "user2", auth_model.AccessTokenScopeAll)), http.StatusCreated)
		repo, err := repo_model.GetRepositoryByOwnerAndName(t.Context(), "user2", "audit-push")
		require.NoError(t, err)

		dir := t.TempDir()
		doGitClone(dir, gitURL(u, "/user2/audit-push.git", "user2", token))(t)
		doGitCheckoutBranch(dir, "-b", "feature")(t)
		commitFile(t, dir, "a.txt", 10)

		t.Run("push", func(t *testing.T) {
			since := auditMaxID(t)
			doGitPushTestRepository(dir, "origin", "feature")(t)
			e := auditActivity(t, since, "activity.git.push")
			assert.Equal(t, audit_model.CredentialAccessToken, e.CredentialType)
			assert.Equal(t, tok.ID, e.CredentialID)
			assert.EqualValues(t, 2, e.ActorID)
			assert.Equal(t, repo.ID, e.RepoID)
			assert.Equal(t, "127.0.0.1", e.IP)
			assert.NotEmpty(t, e.RequestID)
			data := auditData(t, e)
			refs := data["refs"].([]any)
			require.Len(t, refs, 1)
			ref := refs[0].(map[string]any)
			assert.Equal(t, "refs/heads/feature", ref["ref"])
			assert.Equal(t, git.Sha1ObjectFormat.EmptyObjectID().String(), ref["old"])
			assert.Equal(t, false, ref["forced"])
			assert.EqualValues(t, 0, data["forced_count"])
			assert.EqualValues(t, 0, data["deleted_count"])
			assert.Nil(t, data["trigger"])
			assertNoSecrets(t, since, token)
		})

		t.Run("force push", func(t *testing.T) {
			_, _, err := git.NewCommand(git.DefaultContext, "commit", "--amend", "-m", "amended").RunStdString(&git.RunOpts{Dir: dir})
			require.NoError(t, err)
			since := auditMaxID(t)
			doGitPushTestRepository(dir, "-f", "origin", "feature")(t)
			data := auditData(t, auditActivity(t, since, "activity.git.push"))
			assert.EqualValues(t, 1, data["forced_count"])
			assert.Equal(t, true, data["refs"].([]any)[0].(map[string]any)["forced"])
		})

		t.Run("delete", func(t *testing.T) {
			since := auditMaxID(t)
			doGitPushTestRepository(dir, "origin", ":feature")(t)
			assert.EqualValues(t, 1, auditData(t, auditActivity(t, since, "activity.git.push"))["deleted_count"])
		})

		t.Run("AGit is not a push entry", func(t *testing.T) {
			since := auditMaxID(t)
			doGitPushTestRepository(dir, "origin", "HEAD:refs/for/main/audit-topic", "-o", "title=audit")(t)
			assert.Empty(t, auditActions(t, since, "activity.git.push"))
		})

		t.Run("wiki", func(t *testing.T) {
			MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/audit-push/wiki/new", &api.CreateWikiPageOptions{Title: "Home", ContentBase64: "aG9tZQ=="}).AddTokenAuth(getUserToken(t, "user2", auth_model.AccessTokenScopeWriteRepository)), http.StatusCreated)
			wikiDir := t.TempDir()
			cloneWiki(t, wikiDir, gitURL(u, "/user2/audit-push.wiki.git", "user2", token))
			commitFile(t, wikiDir, "Page.md", 10)
			since := auditMaxID(t)
			doGitPushTestRepository(wikiDir, "origin", "HEAD")(t)
			e := auditActivity(t, since, "activity.git.push")
			assert.Equal(t, true, auditData(t, e)["wiki"])
			assert.Equal(t, repo.ID, e.RepoID)
		})

		t.Run("web editor", func(t *testing.T) {
			session := loginUser(t, "user2")
			since := auditMaxID(t)
			testEditFile(t, session, "user2", "audit-push", "main", "README.md", "edited")
			e := auditActivity(t, since, "activity.git.push")
			assert.Equal(t, audit_model.CredentialSession, e.CredentialType)
			assert.EqualValues(t, 2, e.ActorID)
			assert.NotEmpty(t, e.RequestID)
			assert.Equal(t, "web", auditData(t, e)["trigger"])
		})

		t.Run("deploy key at hook level", func(t *testing.T) {
			key, err := asymkey_model.AddDeployKey(audit_model.WithCLI(t.Context()), repo.ID, "audit-deploy", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFeRC8GfFyXtiy0f1E7hLv77BXW7e68tFvIcs8/29YqH nocomment", false, 0)
			require.NoError(t, err)
			gitRepo, err := gitrepo.OpenRepository(t.Context(), repo)
			require.NoError(t, err)
			defer gitRepo.Close()
			main, err := gitRepo.GetBranchCommitID("main")
			require.NoError(t, err)
			since := auditMaxID(t)
			extra := private.HookPushAudit(t.Context(), repo.ID, private.HookOptions{
				OldCommitIDs: []string{git.Sha1ObjectFormat.EmptyObjectID().String()}, NewCommitIDs: []string{main},
				RefFullNames: []git.RefName{git.RefNameFromBranch("deploy")}, UserID: repo.OwnerID, UserName: "user2",
				DeployKeyKeyID: key.KeyID, AuditIP: "192.0.2.7",
			})
			require.NoError(t, extra.Error)
			e := auditActivity(t, since, "activity.git.push")
			assert.Equal(t, audit_model.ActorDeployKey, e.ActorType)
			assert.Equal(t, audit_model.CredentialDeployKey, e.CredentialType)
			assert.Equal(t, key.ID, e.CredentialID)
			assert.Equal(t, "192.0.2.7", e.IP)
		})
	})
}

func TestAuditGitPushRefused(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		auditFresh(t)
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
		gitRepo, err := gitrepo.OpenRepository(t.Context(), repo)
		require.NoError(t, err)
		defer gitRepo.Close()
		master, err := gitRepo.GetBranchCommitID("master")
		require.NoError(t, err)
		branch2, err := gitRepo.GetBranchCommitID("branch2")
		require.NoError(t, err)
		zero := git.Sha1ObjectFormat.EmptyObjectID().String()

		refused := func(t *testing.T, reason string, opts private.HookOptions) {
			t.Helper()
			if opts.UserID == 0 {
				opts.UserID, opts.UserName = 2, "user2"
			}
			opts.AuditCredential = "session:0"
			since := auditMaxID(t)
			extra := private.HookPreReceive(t.Context(), repo.ID, opts)
			require.Error(t, extra.Error)
			e := auditActivity(t, since, "activity.git.push.refused")
			assert.Equal(t, audit_model.ResultRefused, e.Result)
			assert.Equal(t, reason, auditData(t, e)["reason"])
			assert.Equal(t, repo.ID, e.RepoID)
		}
		push := func(old, new string, ref git.RefName) private.HookOptions {
			return private.HookOptions{OldCommitIDs: []string{old}, NewCommitIDs: []string{new}, RefFullNames: []git.RefName{ref}}
		}

		t.Run("protected_branch", func(t *testing.T) {
			refused(t, "protected_branch", push(master, zero, git.RefNameFromBranch("master")))
		})
		t.Run("force_push", func(t *testing.T) {
			require.NoError(t, git_model.UpdateProtectBranch(audit_model.WithCLI(t.Context()), repo, &git_model.ProtectedBranch{RepoID: repo.ID, RuleName: "branch2", CanPush: true}, git_model.WhitelistOptions{}))
			defer func() {
				require.NoError(t, git_model.DeleteProtectedBranch(audit_model.WithCLI(t.Context()), repo, protectedBranchID(t, repo.ID, "branch2")))
			}()
			refused(t, "force_push", push(branch2, master, git.RefNameFromBranch("branch2")))
		})
		t.Run("push_rule", func(t *testing.T) {
			setPRTitleRule(t, repo.ID, git_model.PushRule{BranchNameRegex: "^ok-"})
			defer setPRTitleRule(t, repo.ID, git_model.PushRule{})
			refused(t, "push_rule", push(zero, master, git.RefNameFromBranch("bad")))
		})
		t.Run("push_rule AGit title", func(t *testing.T) {
			setPRTitleRule(t, repo.ID, git_model.PushRule{PRTitleRegex: `^ABC-\d+ `})
			defer setPRTitleRule(t, repo.ID, git_model.PushRule{})
			since := auditMaxID(t)
			_, extra := private.HookProcReceive(t.Context(), repo.ID, private.HookOptions{
				OldCommitIDs: []string{zero}, NewCommitIDs: []string{branch2}, RefFullNames: []git.RefName{"refs/for/master/audit-title"},
				UserID: 2, UserName: "user2", GitPushOptions: map[string]string{"title": "no key"},
			})
			require.Error(t, extra.Error)
			assert.Equal(t, "push_rule", auditData(t, auditActivity(t, since, "activity.git.push.refused"))["reason"])
		})
		t.Run("org_policy", func(t *testing.T) {
			defer setPersonalRepoMode(t, setting.PersonalReposPrivateOnly)()
			private2 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 2})
			since := auditMaxID(t)
			extra := private.HookPreReceive(t.Context(), private2.ID, private.HookOptions{
				OldCommitIDs: []string{zero}, NewCommitIDs: []string{zero}, RefFullNames: []git.RefName{git.RefNameFromBranch("x")},
				UserID: 2, UserName: "user2", GitPushOptions: map[string]string{"repo.private": "false"},
			})
			require.Error(t, extra.Error)
			assert.Equal(t, "org_policy", auditData(t, auditActivity(t, since, "activity.git.push.refused"))["reason"])
		})
		t.Run("deploy_key", func(t *testing.T) {
			key, err := asymkey_model.AddDeployKey(audit_model.WithCLI(t.Context()), 2, "audit-other-repo", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINDRDoephkaFELacrNNe2fqAwedhRB1MKOpLEHlPuczO nocomment", false, 0)
			require.NoError(t, err)
			opts := push(master, branch2, git.RefNameFromBranch("branch2"))
			opts.DeployKeyKeyID = key.KeyID
			refused(t, "deploy_key", opts)
		})
		t.Run("permission", func(t *testing.T) {
			opts := push(master, branch2, git.RefNameFromBranch("branch2"))
			opts.UserID, opts.UserName = 4, "user4"
			refused(t, "permission", opts)
		})

		t.Run("HTTP", func(t *testing.T) {
			readToken := getUserToken(t, "user2", auth_model.AccessTokenScopeReadRepository)
			dir := t.TempDir()
			doGitClone(dir, gitURL(u, "/user2/repo1.git", "user2", readToken))(t)
			doGitCheckoutBranch(dir, "-b", "audit-read")(t)
			commitFile(t, dir, "r.txt", 10)

			since := auditMaxID(t)
			_, err := gitPushResult(dir, "origin", "audit-read")
			require.Error(t, err)
			e := auditActivity(t, since, "activity.git.push.refused") // none for git's first, anonymous round trip
			assert.Equal(t, "permission", auditData(t, e)["reason"])
			assert.Equal(t, audit_model.CredentialAccessToken, e.CredentialType)

			t.Run("size_lock", func(t *testing.T) {
				defer resetPersonalRepoMode(t)
				lockPersonalRepos(t, 2, true)
				writeToken := getUserToken(t, "user2", auth_model.AccessTokenScopeWriteRepository)
				doGitSetRemoteURL(dir, "origin", gitURL(u, "/user2/repo1.git", "user2", writeToken))(t)
				since := auditMaxID(t)
				_, err := gitPushResult(dir, "origin", "audit-read")
				require.Error(t, err)
				assert.Equal(t, "size_lock", auditData(t, auditActivity(t, since, "activity.git.push.refused"))["reason"])
			})

			t.Run("expired token", func(t *testing.T) {
				expiredToken := getUserToken(t, "user2", auth_model.AccessTokenScopeWriteRepository)
				expired, err := auth_model.GetAccessTokenBySHA(t.Context(), expiredToken)
				require.NoError(t, err)
				_, err = db.GetEngine(t.Context()).Exec("UPDATE access_token SET expires_unix = 1 WHERE id = ?", expired.ID)
				require.NoError(t, err)
				doGitSetRemoteURL(dir, "origin", gitURL(u, "/user2/repo1.git", "user2", expiredToken))(t)
				since := auditMaxID(t)
				_, err = gitPushResult(dir, "origin", "audit-read")
				require.Error(t, err)
				assert.Empty(t, auditActions(t, since, "activity.git.push.refused"))
			})
		})
	})
}

func protectedBranchID(t *testing.T, repoID int64, name string) int64 {
	t.Helper()
	pb, err := git_model.GetProtectedBranchRuleByName(t.Context(), repoID, name)
	require.NoError(t, err)
	return pb.ID
}
