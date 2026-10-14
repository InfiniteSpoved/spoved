// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/json"
	"forgejo.org/modules/log"
	"forgejo.org/modules/private"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/structs"
	"forgejo.org/modules/test"
	"forgejo.org/services/migrations"
	"forgejo.org/services/migrations/allowlist"
	repo_service "forgejo.org/services/repository"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const bitbucketTestToken = "bbtoken-0123456789abcdef"

// newBitbucketMock serves a Bitbucket Data Center REST API with the projects and their repository slugs, and every
// repository's git data (dumb HTTP) from a copy of user2/repo1. Every clone link it hands out is hostile.
func newBitbucketMock(t *testing.T, projects map[string]string, repos map[string][]string) *httptest.Server {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "repo.git")
	for _, args := range [][]string{
		{"clone", "--bare", filepath.Join(setting.RepoRootPath, "user2", "repo1.git"), bare},
		{"-C", bare, "update-server-info"},
	} {
		out, err := exec.Command("git", args...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	files := http.FileServer(http.Dir(bare))

	page := func(w http.ResponseWriter, values any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"isLastPage": true, "values": values})
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if parts[0] == "scm" && len(parts) >= 3 { // git data: the same repository for every slug
			http.StripPrefix("/"+strings.Join(parts[:3], "/"), files).ServeHTTP(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+bitbucketTestToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		rest := parts[3:] // after rest/api/1.0
		switch {
		case len(rest) == 1 && rest[0] == "projects":
			values := []map[string]string{}
			for key, name := range projects {
				values = append(values, map[string]string{"key": key, "name": name})
			}
			page(w, values)
		case len(rest) == 3 && rest[2] == "repos":
			values := []map[string]any{}
			for _, slug := range repos[strings.ToUpper(rest[1])] {
				values = append(values, map[string]any{"slug": slug, "links": map[string]any{"clone": []map[string]string{
					{"name": "http", "href": "http://evil.example.com/scm/x/y.git"},
					{"name": "ssh", "href": "ssh://git@bb.example.com:7999/" + rest[1] + "/" + slug + ".git\x1b[2J"},
				}}})
			}
			page(w, values)
		case len(rest) == 4:
			_ = json.NewEncoder(w).Encode(map[string]any{"slug": rest[3], "links": map[string]any{
				"clone": []map[string]string{{"name": "http", "href": "file:///etc/passwd"}},
			}})
		case len(rest) == 6 && rest[4] == "branches":
			_, _ = w.Write([]byte(`{"displayId":"master"}`))
		default:
			page(w, []any{})
		}
	}))
}

func countBitbucketImportRows(t *testing.T) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, bean := range []any{new(repo_model.Repository), new(repo_model.RepoGroup), new(repo_model.RepoGroupNameClaim), new(repo_model.BitbucketOrigin), new(repo_model.Redirect)} {
		n, err := db.GetEngine(db.DefaultContext).Count(bean)
		require.NoError(t, err)
		counts[fmt.Sprintf("%T", bean)] = int(n)
	}
	return counts
}

func TestBitbucketImport(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, _ *url.URL) {
		defer test.MockVariableValueWithReset(&setting.Migrations.AllowLocalNetworks, true, func() { require.NoError(t, allowlist.Init()) })()
		defer test.MockVariableValueWithReset(&setting.Migrations.AllowUnencrypted, true, func() { require.NoError(t, allowlist.Init()) })()
		ctx := t.Context()
		server := newBitbucketMock(t, map[string]string{"PAY": "Payments\x1b[31m Team", "ZZZ": "repo3"}, map[string][]string{"PAY": {"api", "web"}, "ZZZ": {"tools"}})
		defer server.Close()
		opts := migrations.BitbucketImportOptions{Server: server.URL, Token: bitbucketTestToken, Projects: []string{"PAY"}, PullRequests: true}
		dryRun := opts
		dryRun.DryRun = true

		t.Run("a conflict in the last item writes nothing", func(t *testing.T) {
			before := countBitbucketImportRows(t)
			all := opts
			all.Projects = nil // PAY, then ZZZ, whose name is taken by org3's repo3
			report, err := migrations.ImportBitbucket(ctx, "user2", "org3", all)
			require.NoError(t, err)
			assert.True(t, report.Blocked)
			last := report.Items[len(report.Items)-2]
			assert.Equal(t, "ZZZ", last.Bitbucket)
			assert.Equal(t, migrations.BitbucketConflict, last.Status)
			assert.Equal(t, before, countBitbucketImportRows(t))
		})

		t.Run("a server outside the allowlist gets no request", func(t *testing.T) {
			hits := 0
			blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
			defer blocked.Close()
			defer test.MockVariableValueWithReset(&setting.Migrations.AllowLocalNetworks, false, func() { require.NoError(t, allowlist.Init()) })()
			_, err := migrations.ImportBitbucket(ctx, "user2", "org3", migrations.BitbucketImportOptions{Server: blocked.URL, Token: bitbucketTestToken, DryRun: true})
			require.Error(t, err)
			assert.Zero(t, hits)
		})

		t.Run("dry run", func(t *testing.T) {
			before := countBitbucketImportRows(t)
			report, err := migrations.ImportBitbucket(ctx, "user2", "org3", dryRun)
			require.NoError(t, err)
			assert.False(t, report.Blocked)
			assert.Equal(t, before, countBitbucketImportRows(t))
			require.Len(t, report.Items, 3)
			assert.Equal(t, "org3/payments-31m-team", report.Items[0].Name)
			for _, item := range report.Items {
				assert.Equal(t, migrations.BitbucketPlanned, item.Status, item.Bitbucket)
			}
			api := report.Items[1]
			assert.Equal(t, "PAY/api", api.Bitbucket)
			assert.Equal(t, server.URL+"/scm/pay/api.git", api.OldHTTP)
			assert.Equal(t, server.URL+"/projects/PAY/repos/api/browse", api.OldWeb)
			assert.NotContains(t, api.OldSSH, "\x1b", "display text is stripped of control characters")
		})

		t.Run("through the internal route", func(t *testing.T) {
			text, extra := private.BitbucketImport(ctx, private.BitbucketImportParams{Doer: "user2", Org: "org3", Server: server.URL, Token: bitbucketTestToken, Projects: []string{"PAY"}, DryRun: true})
			require.NoError(t, extra.Error)
			assert.Contains(t, text, `"status":"planned"`)
			assert.NotContains(t, text, bitbucketTestToken)

			_, extra = private.BitbucketImport(ctx, private.BitbucketImportParams{Doer: "user5", Org: "org3", Server: server.URL, Token: bitbucketTestToken, DryRun: true})
			assert.Equal(t, http.StatusForbidden, extra.StatusCode)
		})

		var report *migrations.BitbucketImportReport
		t.Run("import", func(t *testing.T) {
			logChecker, cleanup := test.NewLogChecker(log.DEFAULT, log.TRACE)
			logChecker.Filter(bitbucketTestToken)
			defer cleanup()

			var err error
			report, err = migrations.ImportBitbucket(ctx, "user2", "org3", opts)
			require.NoError(t, err)
			for _, item := range report.Items {
				require.Equal(t, migrations.BitbucketCreated, item.Status, "%s: %s", item.Bitbucket, item.Reason)
			}
			found, _ := logChecker.Check(time.Second)
			assert.False(t, found[0], "the token is never logged")

			group := unittest.AssertExistsAndLoadBean(t, &repo_model.RepoGroup{OwnerID: 3, LowerName: "payments-31m-team"})
			unittest.AssertExistsAndLoadBean(t, &repo_model.BitbucketOrigin{OwnerID: 3, GroupID: group.ID, ProjectKey: "PAY", Server: server.URL})
			for _, slug := range []string{"api", "web"} {
				repo, err := repo_model.GetRepositoryByPath(ctx, 3, group.Name, slug)
				require.NoError(t, err)
				assert.True(t, repo.IsPrivate)
				assert.Equal(t, group.ID, repo.GroupID)
				assert.Equal(t, server.URL+"/projects/PAY/repos/"+slug+"/browse", repo.OriginalURL)
				origin := unittest.AssertExistsAndLoadBean(t, &repo_model.BitbucketOrigin{RepoID: repo.ID})
				assert.Zero(t, origin.OwnerID)
				assert.Equal(t, "PAY/"+slug, origin.Path())

				// the git data is at the project path and nothing at the owner's root
				assert.DirExists(t, repo.RepoPath())
				assert.NoDirExists(t, repo_model.RepoPath("org3", slug))
				config, err := os.ReadFile(filepath.Join(repo.RepoPath(), "config"))
				require.NoError(t, err)
				assert.NotContains(t, string(config), bitbucketTestToken)
				assert.NotContains(t, string(config), `[remote "origin"]`)
				assert.NotContains(t, repo.OriginalURL, bitbucketTestToken)
				out, err := exec.Command("git", "-C", repo.RepoPath(), "rev-parse", "--verify", "refs/heads/master").CombinedOutput()
				require.NoError(t, err, string(out))
			}
		})

		t.Run("a second run reuses everything", func(t *testing.T) {
			before := countBitbucketImportRows(t)
			again, err := migrations.ImportBitbucket(ctx, "user2", "org3", opts)
			require.NoError(t, err)
			for _, item := range again.Items {
				assert.Equal(t, migrations.BitbucketReused, item.Status, item.Bitbucket)
			}
			assert.Equal(t, report.Items[1].NewHTTP, again.Items[1].NewHTTP)
			assert.Equal(t, before, countBitbucketImportRows(t))
		})

		t.Run("a resume never reuses a repository that left its place", func(t *testing.T) {
			group := unittest.AssertExistsAndLoadBean(t, &repo_model.RepoGroup{OwnerID: 3, LowerName: "payments-31m-team"})
			api, err := repo_model.GetRepositoryByPath(ctx, 3, group.Name, "api")
			require.NoError(t, err)
			blocked := func(t *testing.T) {
				t.Helper()
				before := countBitbucketImportRows(t)
				r, err := migrations.ImportBitbucket(ctx, "user2", "org3", opts)
				require.NoError(t, err)
				assert.True(t, r.Blocked)
				assert.Equal(t, migrations.BitbucketConflict, r.Items[1].Status)
				assert.Equal(t, before, countBitbucketImportRows(t))
			}

			_, err = db.Exec(ctx, "UPDATE repository SET trashed_unix = 1 WHERE id = ?", api.ID)
			require.NoError(t, err)
			blocked(t)
			_, err = db.Exec(ctx, "UPDATE repository SET trashed_unix = 0 WHERE id = ?", api.ID)
			require.NoError(t, err)

			require.NoError(t, repo_service.MoveRepository(ctx, unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1}), api, 0))
			blocked(t)
		})

		t.Run("the existing web migration still works", func(t *testing.T) {
			session := loginUser(t, "user2")
			req := NewRequestWithValues(t, "POST", "/repo/migrate", map[string]string{
				"clone_addr": server.URL + "/scm/pay/api.git",
				"auth_token": bitbucketTestToken,
				"uid":        "2",
				"repo_name":  "from-bitbucket",
				"service":    fmt.Sprint(int(structs.BitbucketDataCenterService)),
			})
			session.MakeRequest(t, req, http.StatusSeeOther)
			assert.Eventually(t, func() bool {
				repo, err := repo_model.GetRepositoryByOwnerAndName(db.DefaultContext, "user2", "from-bitbucket")
				return err == nil && repo.Status == repo_model.RepositoryReady
			}, 30*time.Second, 200*time.Millisecond)
			repo, err := repo_model.GetRepositoryByOwnerAndName(ctx, "user2", "from-bitbucket")
			require.NoError(t, err)
			assert.NotContains(t, repo.OriginalURL, bitbucketTestToken)
			assert.Equal(t, structs.BitbucketDataCenterService, repo.OriginalServiceType)
		})
	})
}
