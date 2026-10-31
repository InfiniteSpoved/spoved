// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	actions_model "forgejo.org/models/actions"
	audit_model "forgejo.org/models/audit"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	perm_model "forgejo.org/models/perm"
	repo_model "forgejo.org/models/repo"
	secretscan_model "forgejo.org/models/secretscan"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/log"
	repo_module "forgejo.org/modules/repository"
	"forgejo.org/modules/setting"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/test"
	maintenance_service "forgejo.org/services/maintenance"
	repo_service "forgejo.org/services/repository"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var secretAlertSeq atomic.Int64

// seedSecretAlert stores an alert with one place, without any scan: the tests of the API need rows, not pushes.
func seedSecretAlert(t *testing.T, repo *repo_model.Repository, rule, secretType, kind, path string) *secretscan_model.Alert {
	t.Helper()
	a := &secretscan_model.Alert{
		RepoID: repo.ID, RuleID: rule, SecretType: secretType, LocationCount: 1,
		Fingerprint: fmt.Sprintf("%064x", secretAlertSeq.Add(1)),
	}
	require.NoError(t, db.WithTx(t.Context(), func(ctx context.Context) error {
		if err := secretscan_model.InsertAlert(ctx, a); err != nil {
			return err
		}
		l := secretscan_model.NewLocation(repo.ID, kind, "blob"+a.Fingerprint[:8], path, 3, 3, "commitsha1", "An Author", "author@example.com", 1700000000)
		l.AlertID = a.ID
		_, err := db.GetEngine(ctx).Insert(l)
		return err
	}))
	return a
}

func secretAlertRow(t *testing.T, repoID, index int64) *secretscan_model.Alert {
	t.Helper()
	a, err := secretscan_model.GetAlert(t.Context(), repoID, index)
	require.NoError(t, err)
	return a
}

// seedSecretRepo gives a repository the three alerts of the tests: github (repo), aws (repo) and github (wiki).
func seedSecretRepo(t *testing.T, repo *repo_model.Repository) {
	t.Helper()
	seedSecretAlert(t, repo, "github-token", "github", secretscan_model.KindRepo, "deploy/env")
	seedSecretAlert(t, repo, "aws-access-key-id", "aws", secretscan_model.KindRepo, "conf/aws.ini")
	seedSecretAlert(t, repo, "github-token", "github", secretscan_model.KindWiki, "Home.md")
}

func TestSecretScanningAPI(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		defer test.MockVariableValue(&setting.SecretScanning.Enabled, true)()
		sys := audit_model.WithSystem(db.DefaultContext, "test")
		org3 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 3})
		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
		group, err := repo_model.CreateRepoGroup(sys, org3, "sproj", "")
		require.NoError(t, err)
		root := depNewRepo(t, org3, user2, "secret-org", true, 0)
		leaf := depNewRepo(t, org3, user2, "leaf", true, group.ID)
		pub := depNewRepo(t, user2, user2, "secret-pub", false, 0)
		other := depNewRepo(t, org3, user2, "secret-other", true, 0)
		for _, r := range []*repo_model.Repository{root, leaf, pub} {
			seedSecretRepo(t, r)
		}
		for range 5 {
			seedSecretAlert(t, other, "github-token", "github", secretscan_model.KindRepo, "x")
		}
		bases := map[string]struct {
			base string
			repo *repo_model.Repository
		}{
			"plain":      {"/api/v1/repos/org3/secret-org/secret-scanning/alerts", root},
			"id":         {fmt.Sprintf("/api/v1/repositories/%d/secret-scanning/alerts", root.ID), root},
			"project":    {"/api/v1/repos/org3/sproj/leaf/-/secret-scanning/alerts", leaf},
			"project id": {fmt.Sprintf("/api/v1/repositories/%d/secret-scanning/alerts", leaf.ID), leaf},
		}
		do := func(method, path, token string, body any, status int) *httptest.ResponseRecorder {
			t.Helper()
			var req *RequestWrapper
			if body != nil {
				req = NewRequestWithJSON(t, method, path, body)
			} else {
				req = NewRequest(t, method, path)
			}
			if token != "" {
				req.AddTokenAuth(token)
			}
			return MakeRequest(t, req, status)
		}
		writeToken := getUserToken(t, "user2", auth_model.AccessTokenScopeWriteRepository)
		readToken := getUserToken(t, "user2", auth_model.AccessTokenScopeReadRepository)
		closeBody := map[string]string{"state": "revoked", "comment": "rotated"}
		unchanged := func(r *repo_model.Repository, index int64) {
			t.Helper()
			assert.Equal(t, secretscan_model.StateOpen, secretAlertRow(t, r.ID, index).State)
		}

		t.Run("the response", func(t *testing.T) {
			for name, b := range bases {
				var list []*api.SecretScanningAlert
				resp := do("GET", b.base, readToken, nil, http.StatusOK)
				DecodeJSON(t, resp, &list)
				require.Len(t, list, 3, name)
				assert.Equal(t, "3", resp.Header().Get("X-Total-Count"))
				assert.Equal(t, []int64{3, 2, 1}, []int64{list[0].Number, list[1].Number, list[2].Number}, "newest first")
				assert.Empty(t, list[0].Locations, "the list has no places")
				var one api.SecretScanningAlert
				DecodeJSON(t, do("GET", b.base+"/1", readToken, nil, http.StatusOK), &one)
				assert.EqualValues(t, 1, one.Number)
				assert.Equal(t, "open", one.State)
				assert.Equal(t, "github-token", one.RuleID)
				assert.Equal(t, "github", one.SecretType)
				assert.Equal(t, "GitHub token", one.SecretTypeDisplayName)
				assert.Len(t, one.Fingerprint, 64)
				assert.Equal(t, b.repo.HTMLURL()+"/security/secrets/1", one.HTMLURL)
				assert.Equal(t, 1, one.LocationsCount)
				assert.Nil(t, one.ResolvedBy)
				require.Len(t, one.Locations, 1)
				l := one.Locations[0]
				assert.Equal(t, "repo", l.Kind)
				assert.Equal(t, "deploy/env", l.Path)
				assert.Equal(t, 3, l.StartLine)
				assert.Equal(t, "commitsha1", l.CommitSHA)
				assert.Equal(t, "An Author", l.CommitAuthorName)
				assert.Equal(t, "author@example.com", l.CommitAuthorEmail)
				require.NotNil(t, l.CommitDate)
				assert.Equal(t, b.repo.HTMLURL()+"/src/commit/commitsha1/deploy/env#L3", l.HTMLURL)
				DecodeJSON(t, do("GET", b.base+"/3", readToken, nil, http.StatusOK), &one)
				require.Len(t, one.Locations, 1)
				assert.Equal(t, "wiki", one.Locations[0].Kind)
				assert.Equal(t, b.repo.HTMLURL()+"/wiki/Home", one.Locations[0].HTMLURL)
			}
		})

		t.Run("filters and pages", func(t *testing.T) {
			base := bases["plain"].base
			count := func(query string) (n int, total string) {
				var list []*api.SecretScanningAlert
				resp := do("GET", base+query, readToken, nil, http.StatusOK)
				DecodeJSON(t, resp, &list)
				return len(list), resp.Header().Get("X-Total-Count")
			}
			for query, want := range map[string]int{
				"?state=open": 3, "?state=closed": 0, "?state=revoked": 0, "?secret_type=aws": 1, "?secret_type=github": 2,
				"?rule=github-token": 2, "?rule=aws-access-key-id&state=open": 1, "?rule=nothing": 0,
			} {
				n, _ := count(query)
				assert.Equal(t, want, n, query)
			}
			n, total := count("?limit=2&page=2")
			assert.Equal(t, 1, n)
			assert.Equal(t, "3", total)
			for _, bad := range []string{"state=fixed", "state=bogus", "secret_type=" + strings.Repeat("a", 65), "rule=" + strings.Repeat("a", 65)} {
				do("GET", base+"?"+bad, readToken, nil, http.StatusUnprocessableEntity)
			}
		})

		t.Run("who is refused, before any validation", func(t *testing.T) {
			missing := do("GET", "/api/v1/repos/org3/no-such-repo/secret-scanning/alerts", readToken, nil, http.StatusNotFound).Body.String()
			session := loginUser(t, "user2")
			publicOnly := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopePublicOnly, auth_model.AccessTokenScopeReadRepository)
			readIssue := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadIssue)
			user4Token := getUserToken(t, "user4", auth_model.AccessTokenScopeReadRepository, auth_model.AccessTokenScopeWriteRepository)
			req := NewRequestWithJSON(t, "POST", "/api/v1/users/user2/tokens", &api.CreateAccessTokenOption{
				Name: "secret-limited", Scopes: []string{string(auth_model.AccessTokenScopeWriteRepository)},
				Repositories: []*api.RepoTargetOption{{Owner: "user2", Name: "repo2"}},
			})
			req.AddBasicAuth("user2")
			var limited api.AccessToken
			DecodeJSON(t, MakeRequest(t, req, http.StatusCreated), &limited)

			for name, b := range bases {
				one := b.base + "/1"
				// anonymous, an org member without access, and the bad filter or ID or body behind none of them
				for _, token := range []string{"", user4Token} {
					assert.Equal(t, missing, do("GET", b.base, token, nil, http.StatusNotFound).Body.String(), name)
					do("GET", b.base+"?state=bogus", token, nil, http.StatusNotFound)
					do("GET", b.base+"/abc", token, nil, http.StatusNotFound)
					do("GET", one, token, nil, http.StatusNotFound)
					do("PATCH", one, token, map[string]string{"state": "bogus"}, http.StatusNotFound)
					do("PATCH", b.base+"/999", token, map[string]string{"state": "bogus"}, http.StatusNotFound)
				}
				// a reader of the code is not enough; a writer is
				require.NoError(t, repo_module.AddCollaborator(sys, b.repo, user4, perm_model.AccessModeRead))
				do("GET", b.base, user4Token, nil, http.StatusNotFound)
				do("PATCH", one, user4Token, closeBody, http.StatusNotFound)
				require.NoError(t, repo_module.ChangeCollaborationAccessMode(sys, b.repo, user4.ID, perm_model.AccessModeWrite))
				do("GET", b.base, user4Token, nil, http.StatusOK)
				require.NoError(t, repo_module.ChangeCollaborationAccessMode(sys, b.repo, user4.ID, perm_model.AccessModeAdmin))
				do("GET", one, user4Token, nil, http.StatusOK)
				require.NoError(t, repo_module.DeleteCollaboration(sys, b.repo, user4.ID))
				do("GET", b.base, user4Token, nil, http.StatusNotFound)

				// tokens
				do("GET", b.base, publicOnly, nil, http.StatusNotFound)
				do("GET", b.base, readIssue, nil, http.StatusForbidden)
				do("GET", b.base, limited.Token, nil, http.StatusNotFound) // limited to another repository
				do("PATCH", one, readToken, closeBody, http.StatusForbidden)
				readOwned := newOwnedAccessToken(t, auth_model.AccessTokenOwnerRepo, b.repo.ID, 0, perm_model.AccessModeRead, "write:repository")
				do("GET", b.base, readOwned.Token, nil, http.StatusNotFound)
				do("PATCH", one, readOwned.Token, closeBody, http.StatusNotFound)
				writeOwned := newOwnedAccessToken(t, auth_model.AccessTokenOwnerRepo, b.repo.ID, 0, perm_model.AccessModeWrite, "write:repository")
				do("GET", b.base, writeOwned.Token, nil, http.StatusOK)
				unchanged(b.repo, 1)
			}
			// a public repository is no exception
			pubBase := "/api/v1/repos/user2/secret-pub/secret-scanning/alerts"
			do("GET", pubBase, "", nil, http.StatusNotFound)
			do("GET", pubBase, user4Token, nil, http.StatusNotFound)
			do("GET", pubBase, publicOnly, nil, http.StatusOK) // the writer's public-only token reads a public repository
			do("PATCH", pubBase+"/1", "", closeBody, http.StatusNotFound)
		})

		t.Run("who sees them", func(t *testing.T) {
			admin := getUserToken(t, "user1", auth_model.AccessTokenScopeReadRepository, auth_model.AccessTokenScopeWriteRepository)
			orgOwner := getUserToken(t, "user2", auth_model.AccessTokenScopeReadRepository)
			orgOwnedToken := newOwnedAccessToken(t, auth_model.AccessTokenOwnerOrg, 3, 0, perm_model.AccessModeWrite, "write:repository")
			for name, b := range bases {
				for _, token := range []string{admin, orgOwner, orgOwnedToken.Token} {
					var one api.SecretScanningAlert
					DecodeJSON(t, do("GET", b.base+"/1", token, nil, http.StatusOK), &one)
					assert.Len(t, one.Locations, 1, name)
				}
				// a repository admin who is no owner
				require.NoError(t, repo_module.AddCollaborator(sys, b.repo, user4, perm_model.AccessModeAdmin))
				do("GET", b.base+"/1", getUserToken(t, "user4", auth_model.AccessTokenScopeReadRepository), nil, http.StatusOK)
				require.NoError(t, repo_module.DeleteCollaboration(sys, b.repo, user4.ID))
			}
		})

		t.Run("auditor reads, never writes", func(t *testing.T) {
			_, err := db.GetEngine(t.Context()).Exec("UPDATE `user` SET is_auditor = ? WHERE id = 8", true)
			require.NoError(t, err)
			defer func() {
				_, err := db.GetEngine(t.Context()).Exec("UPDATE `user` SET is_auditor = ? WHERE id = 8", false)
				require.NoError(t, err)
			}()
			auditor := getUserToken(t, "user8", auth_model.AccessTokenScopeWriteRepository)
			for name, b := range bases {
				var list []*api.SecretScanningAlert
				DecodeJSON(t, do("GET", b.base, auditor, nil, http.StatusOK), &list)
				assert.Len(t, list, 3, name)
				do("GET", b.base+"/1", auditor, nil, http.StatusOK)
				do("PATCH", b.base+"/1", auditor, closeBody, http.StatusNotFound)
				unchanged(b.repo, 1)
			}
		})

		t.Run("job tokens", func(t *testing.T) {
			original := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionTask{ID: 47})
			t.Cleanup(func() {
				require.NoError(t, actions_model.UpdateTask(db.DefaultContext, original, "repo_id", "owner_id", "is_fork_pull_request", "token_permissions", "token_salt", "token_hash", "token_last_eight"))
			})
			for _, b := range bases {
				task := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionTask{ID: 47})
				task.RepoID, task.OwnerID, task.IsForkPullRequest, task.TokenPermissions = b.repo.ID, b.repo.OwnerID, false, ""
				task.GenerateToken()
				require.NoError(t, actions_model.UpdateTask(t.Context(), task, "repo_id", "owner_id", "is_fork_pull_request", "token_permissions", "token", "token_salt", "token_hash", "token_last_eight"))
				do("GET", b.base, task.Token, nil, http.StatusNotFound)
				do("GET", b.base+"/1", task.Token, nil, http.StatusNotFound)
				do("PATCH", b.base+"/1", task.Token, closeBody, http.StatusForbidden)
				unchanged(b.repo, 1)
			}
		})

		t.Run("close and reopen", func(t *testing.T) {
			for name, b := range bases {
				for i, state := range secretscan_model.ClosedStates {
					index := int64(i + 1)
					since := auditMaxID(t)
					var got api.SecretScanningAlert
					DecodeJSON(t, do("PATCH", fmt.Sprintf("%s/%d", b.base, index), writeToken, map[string]string{"state": state, "comment": "because <b>" + state}, http.StatusOK), &got)
					assert.Equal(t, state, got.State, name)
					assert.Equal(t, state, got.Resolution)
					assert.Equal(t, "because <b>"+state, got.ResolutionComment)
					require.NotNil(t, got.ResolvedBy)
					assert.Equal(t, user2.ID, got.ResolvedBy.ID)
					assert.NotNil(t, got.ResolvedAt)
					row := secretAlertRow(t, b.repo.ID, index)
					assert.Equal(t, state, row.State)
					assert.Equal(t, state, row.Reason)
					assert.Equal(t, user2.ID, row.ClosedByID)
					assert.NotZero(t, row.ClosedUnix)
					e := auditOne(t, since, "repo.secret_alert.closed")
					assert.Equal(t, "secret_scanning_alert", e.TargetType)
					assert.Equal(t, b.repo.ID, e.RepoID)
					assert.Equal(t, row.ID, e.TargetID)
					data := auditData(t, e)
					assert.EqualValues(t, index, data["index"])
					assert.Equal(t, row.RuleID, data["rule"])
					assert.Equal(t, row.Fingerprint[:12], data["fingerprint"])
					assert.Equal(t, "open", data["previous_state"])
					assert.Equal(t, state, data["state"])
					assert.Equal(t, "because <b>"+state, data["comment"])
					assert.NotEmpty(t, data["path"])
				}
				// closed alerts are in the closed filter and out of the open one
				var list []*api.SecretScanningAlert
				DecodeJSON(t, do("GET", b.base+"?state=closed", readToken, nil, http.StatusOK), &list)
				assert.Len(t, list, 3)
				DecodeJSON(t, do("GET", b.base+"?state=used_in_tests", readToken, nil, http.StatusOK), &list)
				assert.Len(t, list, 1)

				// a closed alert can't be closed again
				do("PATCH", b.base+"/1", writeToken, map[string]string{"state": "false_positive"}, http.StatusConflict)
				assert.Equal(t, "revoked", secretAlertRow(t, b.repo.ID, 1).State)

				for index := int64(1); index <= 3; index++ {
					since := auditMaxID(t)
					var got api.SecretScanningAlert
					DecodeJSON(t, do("PATCH", fmt.Sprintf("%s/%d", b.base, index), writeToken, map[string]string{"state": "open"}, http.StatusOK), &got)
					assert.Equal(t, "open", got.State)
					assert.Empty(t, got.Resolution)
					assert.Empty(t, got.ResolutionComment)
					assert.Nil(t, got.ResolvedBy)
					assert.Nil(t, got.ResolvedAt)
					row := secretAlertRow(t, b.repo.ID, index)
					assert.Equal(t, secretscan_model.StateOpen, row.State)
					assert.Empty(t, row.Reason)
					assert.Empty(t, row.Comment)
					assert.Zero(t, row.ClosedByID)
					assert.Zero(t, row.ClosedUnix)
					data := auditData(t, auditOne(t, since, "repo.secret_alert.reopened"))
					assert.EqualValues(t, index, data["index"])
					assert.Equal(t, "open", data["state"])
					assert.NotEqual(t, "open", data["previous_state"])
				}
				// reopening an open alert changes nothing and records nothing
				since := auditMaxID(t)
				do("PATCH", b.base+"/1", writeToken, map[string]string{"state": "open"}, http.StatusOK)
				n, err := db.GetEngine(t.Context()).Where("id > ? AND action LIKE ?", since, "repo.secret_alert.%").Count(new(audit_model.Entry))
				require.NoError(t, err)
				assert.Zero(t, n)
			}
		})

		t.Run("validation", func(t *testing.T) {
			base := bases["plain"].base
			one := base + "/2"
			tok := secretToken("comment")
			body := strings.TrimPrefix(tok, "ghp"+"_")
			lc, cancel := test.NewLogChecker(log.DEFAULT, log.TRACE)
			defer cancel()
			lc.Filter(tok, body).StopMark("secret-comment-stop")
			for _, bad := range []map[string]string{
				{"state": "bogus"},
				{"state": "fixed"},
				{"state": ""},
				{"state": "closed"},
				{"state": "REVOKED"},
				{"state": "revoked", "comment": strings.Repeat("a", 1001)},
				{"state": "revoked", "comment": "see " + tok},
				{"state": "false_positive", "comment": "key=" + tok},
				{"state": "open", "comment": "x"},
			} {
				do("PATCH", one, writeToken, bad, http.StatusUnprocessableEntity)
			}
			do("PATCH", one, writeToken, map[string]string{"state": "revoked", "comment": strings.Repeat("é", 500)}, http.StatusOK) // 1000 bytes
			do("PATCH", one, writeToken, map[string]string{"state": "open"}, http.StatusOK)
			for _, bad := range []map[string]string{{"state": "bogus"}, {"state": "revoked", "comment": "see " + tok}} {
				do("PATCH", bases["project"].base+"/2", writeToken, bad, http.StatusUnprocessableEntity)
			}
			// the control characters of a comment are stripped
			var got api.SecretScanningAlert
			DecodeJSON(t, do("PATCH", one, writeToken, map[string]string{"state": "revoked", "comment": "a\x00b\x1b[31mc"}, http.StatusOK), &got)
			assert.NotContains(t, got.ResolutionComment, "\x00")
			assert.NotContains(t, got.ResolutionComment, "\x1b")
			do("PATCH", one, writeToken, map[string]string{"state": "open"}, http.StatusOK)

			for _, table := range []string{"secret_scanning_alert", "audit_event"} {
				rows, err := db.GetEngine(t.Context()).Query("SELECT * FROM " + table)
				require.NoError(t, err)
				for _, row := range rows {
					for col, v := range row {
						assert.NotContains(t, string(v), tok, "%s.%s", table, col)
						assert.NotContains(t, string(v), body, "%s.%s", table, col)
					}
				}
			}
			log.Info("secret-comment-stop")
			seen, stopped := lc.Check(5 * time.Second)
			require.True(t, stopped)
			assert.Equal(t, []bool{false, false}, seen)
		})

		t.Run("unknown and foreign alerts", func(t *testing.T) {
			for _, b := range bases {
				for _, index := range []string{"999999", "0", "-1", "abc", "4", "5"} {
					do("GET", b.base+"/"+index, readToken, nil, http.StatusNotFound)
					do("PATCH", b.base+"/"+index, writeToken, closeBody, http.StatusNotFound)
				}
			}
			// number 5 is the other repository's alert; it was not touched
			unchanged(other, 5)
		})

		t.Run("archived", func(t *testing.T) {
			require.NoError(t, repo_service.SetArchiveRepoState(sys, root, true))
			defer func() { require.NoError(t, repo_service.SetArchiveRepoState(sys, root, false)) }()
			for _, name := range []string{"plain", "id"} {
				do("PATCH", bases[name].base+"/1", writeToken, closeBody, http.StatusLocked)
				do("PATCH", bases[name].base+"/1", writeToken, map[string]string{"state": "open"}, http.StatusLocked)
				do("GET", bases[name].base+"/1", readToken, nil, http.StatusOK)
			}
			unchanged(root, 1)
			// the gate comes first: someone who can't see alerts gets 404, not 423
			do("PATCH", bases["plain"].base+"/1", "", closeBody, http.StatusNotFound)
		})

		t.Run("maintenance read-only", func(t *testing.T) {
			require.NoError(t, maintenance_service.Set(sys, nil, setting.MaintenanceReadOnly, "Upgrade", time.Time{}))
			defer func() {
				require.NoError(t, maintenance_service.Set(sys, nil, setting.MaintenanceOff, "", time.Time{}))
			}()
			do("PATCH", bases["plain"].base+"/1", writeToken, closeBody, http.StatusServiceUnavailable)
			do("GET", bases["plain"].base+"/1", writeToken, nil, http.StatusOK)
			unchanged(root, 1)
		})

		t.Run("scan status", func(t *testing.T) {
			statusOf := func(name string) string { return strings.TrimSuffix(bases[name].base, "/alerts") + "/status" }
			var st api.SecretScanningStatus
			DecodeJSON(t, do("GET", statusOf("project"), readToken, nil, http.StatusOK), &st)
			assert.Equal(t, "pending", st.Status, "no scan yet")

			tips := `["` + strings.Repeat("a", 40) + `"]`
			save := func(kind, status, code string, holes, skipped int64) {
				require.NoError(t, secretscan_model.Save(t.Context(), nil, &secretscan_model.Scan{
					RepoID: root.ID, Kind: kind, ScannedTips: tips, Status: status, Error: code, Holes: holes, SkippedLarge: skipped,
				}))
			}
			save(secretscan_model.KindRepo, secretscan_model.StatusOK, "", 0, 2)
			save(secretscan_model.KindWiki, secretscan_model.StatusIncomplete, "over_limit", 3, 0)
			save(secretscan_model.KindPull, secretscan_model.StatusOK, "", 1, 0)
			for _, name := range []string{"plain", "id"} {
				resp := do("GET", statusOf(name), readToken, nil, http.StatusOK)
				DecodeJSON(t, resp, &st)
				assert.Equal(t, secretscan_model.StatusIncomplete, st.Status)
				assert.Equal(t, "over_limit", st.Reason)
				assert.EqualValues(t, 4, st.Holes)
				assert.EqualValues(t, 2, st.SkippedLarge)
				require.NotNil(t, st.LastScanned)
				assert.NotContains(t, resp.Body.String(), "scanned_tips")
				assert.NotContains(t, resp.Body.String(), strings.Repeat("a", 40), "no tips")
			}
			// a failed kind decides
			_, err := db.GetEngine(t.Context()).Exec("UPDATE secret_scan SET status = ?, error = ? WHERE repo_id = ? AND kind = ?", secretscan_model.StatusFailed, "git_error", root.ID, secretscan_model.KindPull)
			require.NoError(t, err)
			DecodeJSON(t, do("GET", statusOf("plain"), readToken, nil, http.StatusOK), &st)
			assert.Equal(t, secretscan_model.StatusFailed, st.Status)
			assert.Equal(t, "git_error", st.Reason)
			// the same gate as the alerts
			do("GET", statusOf("plain"), "", nil, http.StatusNotFound)
			do("GET", statusOf("plain"), getUserToken(t, "user4", auth_model.AccessTokenScopeReadRepository), nil, http.StatusNotFound)
		})

		t.Run("an alert only a pull ref reaches says so", func(t *testing.T) {
			a := seedSecretAlert(t, root, "github-token", "github", secretscan_model.KindRepo, "pull.txt")
			_, err := db.GetEngine(t.Context()).Exec("UPDATE secret_scanning_alert SET via_pull = ? WHERE id = ?", true, a.ID)
			require.NoError(t, err)
			var one api.SecretScanningAlert
			DecodeJSON(t, do("GET", fmt.Sprintf("%s/%d", bases["plain"].base, a.Index), readToken, nil, http.StatusOK), &one)
			assert.True(t, one.ViaPull)
			DecodeJSON(t, do("GET", bases["plain"].base+"/1", readToken, nil, http.StatusOK), &one)
			assert.False(t, one.ViaPull)
			_, err = db.GetEngine(t.Context()).Exec("DELETE FROM secret_scanning_location WHERE alert_id = ?", a.ID)
			require.NoError(t, err)
			_, err = db.GetEngine(t.Context()).Exec("DELETE FROM secret_scanning_alert WHERE id = ?", a.ID)
			require.NoError(t, err)
		})

		t.Run("switched off", func(t *testing.T) {
			root.SecretScanningDisabled = true
			require.NoError(t, repo_model.UpdateRepositoryCols(t.Context(), root, "secret_scanning_disabled"))
			for _, name := range []string{"plain", "id"} {
				do("GET", bases[name].base, writeToken, nil, http.StatusNotFound)
				do("GET", bases[name].base+"/1", writeToken, nil, http.StatusNotFound)
				do("GET", strings.TrimSuffix(bases[name].base, "/alerts")+"/status", writeToken, nil, http.StatusNotFound)
				do("PATCH", bases[name].base+"/1", writeToken, closeBody, http.StatusNotFound)
			}
			unchanged(root, 1)
			do("GET", bases["project"].base, writeToken, nil, http.StatusOK) // another repository
			root.SecretScanningDisabled = false
			require.NoError(t, repo_model.UpdateRepositoryCols(t.Context(), root, "secret_scanning_disabled"))
			do("GET", bases["plain"].base, writeToken, nil, http.StatusOK)
		})
	})
}
