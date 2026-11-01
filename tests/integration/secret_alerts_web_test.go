// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	audit_model "forgejo.org/models/audit"
	"forgejo.org/models/db"
	"forgejo.org/models/organization"
	perm_model "forgejo.org/models/perm"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	secretscan_model "forgejo.org/models/secretscan"
	"forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	repo_module "forgejo.org/modules/repository"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"
	maintenance_service "forgejo.org/services/maintenance"
	repo_service "forgejo.org/services/repository"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func secretPage(t *testing.T, s *TestSession, path string, status int) *HTMLDoc {
	t.Helper()
	return NewHTMLParser(t, s.MakeRequest(t, NewRequest(t, "GET", path), status).Body)
}

func secretPost(t *testing.T, s *TestSession, path string, values url.Values, status int) {
	t.Helper()
	s.MakeRequest(t, NewRequestWithURLValues(t, "POST", path, values), status)
}

func TestSecretAlertsWeb(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		defer test.MockVariableValue(&setting.SecretScanning.Enabled, true)()
		sys := audit_model.WithSystem(db.DefaultContext, "test")
		org3 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 3})
		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
		user5 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 5})
		group, err := repo_model.CreateRepoGroup(sys, org3, "webproj", "")
		require.NoError(t, err)
		root := depNewRepo(t, org3, user2, "secret-web", true, 0)
		leaf := depNewRepo(t, org3, user2, "web-leaf", true, group.ID)
		seedSecretRepo(t, root) // 1: github (repo), 2: aws (repo), 3: github (wiki only)
		seedSecretRepo(t, leaf)
		for _, r := range []*repo_model.Repository{root, leaf} { // scanned: the pages offer a rescan
			require.NoError(t, secretscan_model.Save(t.Context(), nil, &secretscan_model.Scan{RepoID: r.ID, Kind: secretscan_model.KindRepo, ScannedTips: "[]", Status: secretscan_model.StatusOK}))
		}
		list := "/org3/secret-web/security/secrets"
		owner := loginUser(t, "user2")
		bare := "ghp" + "_" + "Ab1Cd2Ef3Gh4" + "Ij5Kl6Mn7Op8" + "Qr9St0Uv1Wx2"

		t.Run("list", func(t *testing.T) {
			doc := secretPage(t, owner, list, http.StatusOK)
			assert.Equal(t, 3, doc.Find(".secret-alerts-table .repo-security-row:not(.repo-security-head)").Length())
			assert.Equal(t, "3 Open", strings.TrimSpace(doc.Find(".repo-security-tabs a").First().Text()))
			assert.Equal(t, "0 Closed", strings.TrimSpace(doc.Find(".repo-security-tabs a").Eq(1).Text()))
			// the Security tab, with the count, links here while there is no vulnerability page; the menu names the pages
			assert.Equal(t, "3", strings.TrimSpace(doc.Find("#security-open-alerts").Text()))
			doc.AssertElement(t, `.repo-security-menu a.item[href$="/security/secrets"][aria-current="page"]`, true)
			doc.AssertElement(t, `.repo-security-menu a[href$="/security/alerts"]`, false)
			// first locations, newest first; a rule name, never the secret, the fingerprint only on the alert page
			text := doc.Find(".secret-alerts-table").Text()
			assert.Contains(t, text, "AWS access key ID")
			assert.Contains(t, text, "conf/aws.ini:3")
			assert.NotContains(t, text, secretAlertRow(t, root.ID, 1).Fingerprint[:12])
			doc.AssertElement(t, "#secret-rescan", true) // a repository admin (the owner) can rescan
			doc.AssertElement(t, "#secret-bulk-modal", true)

			for query, want := range map[string]int{
				"?q=aws.ini": 1, "?q=AWS.INI": 1, "?q=nothing": 0, "?type=aws-access-key-id": 1, "?type=github-token": 2,
				"?sort=oldest": 3, "?type=bogus&sort=bogus&page=-4&state=bogus": 3, "?q=Home.md": 1, // the wiki's place, for a wiki reader
			} {
				doc = secretPage(t, owner, list+query, http.StatusOK)
				assert.Equal(t, want, doc.Find(".secret-alerts-table .repo-security-row:not(.repo-security-head)").Length(), query)
			}
			doc = secretPage(t, owner, list+"?q=nothing", http.StatusOK)
			doc.AssertElement(t, "#secret-no-match", true)
			doc = secretPage(t, owner, list+"?sort=oldest", http.StatusOK)
			assert.Contains(t, doc.Find(".secret-alerts-table .repo-security-row:not(.repo-security-head)").First().Text(), "GitHub token")
			doc = secretPage(t, owner, list+"?state=closed", http.StatusOK)
			doc.AssertElement(t, "#secret-empty-state", true)
			doc.AssertElement(t, `select[name="reason"]`, true)
			// the same page through the project path and the repository ID path
			doc = secretPage(t, owner, "/org3/webproj/web-leaf/security/secrets", http.StatusOK)
			assert.Equal(t, 3, doc.Find(".secret-alerts-table .repo-security-row:not(.repo-security-head)").Length())
		})

		t.Run("detail", func(t *testing.T) {
			doc := secretPage(t, owner, list+"/2", http.StatusOK)
			assert.Contains(t, doc.Find("h2").First().Text(), "AWS access key ID")
			assert.Contains(t, doc.Find("#secret-guidance").Text(), "Revoke this credential with the provider first")
			doc.AssertElement(t, `#secret-guidance a[href^="https://docs.aws.amazon.com/"][rel~="noopener"]`, true)
			assert.Equal(t, secretAlertRow(t, root.ID, 2).Fingerprint[:12], strings.TrimSpace(doc.Find("#secret-fingerprint").Text()))
			assert.NotContains(t, doc.Find("body").Text(), secretAlertRow(t, root.ID, 2).Fingerprint)
			loc := doc.Find(".secret-locations .repo-security-row:not(.repo-security-head)")
			require.Equal(t, 1, loc.Length())
			assert.Contains(t, loc.Text(), "Code")
			assert.Contains(t, loc.Text(), "conf/aws.ini:3")
			assert.Contains(t, loc.Text(), "An Author")
			doc.AssertElement(t, `.secret-locations a[href$="/src/commit/commitsha1/conf/aws.ini#L3"]`, true)
			doc.AssertElement(t, "#secret-close", true)
			// the wiki alert: a Wiki label and the page link; the generic and private key texts have no provider link
			doc = secretPage(t, owner, list+"/3", http.StatusOK)
			assert.Contains(t, doc.Find(".secret-locations").Text(), "Wiki")
			doc.AssertElement(t, `.secret-locations a[href$="/wiki/Home"]`, true)
			guide := depNewRepo(t, org3, user2, "secret-guide", true, 0)
			gen := seedSecretAlert(t, guide, "generic-secret-assignment", "generic", secretscan_model.KindRepo, "g.env")
			pem := seedSecretAlert(t, guide, "private-key", "private-key", secretscan_model.KindRepo, "k.pem")
			for _, c := range []struct {
				a    *secretscan_model.Alert
				text string
			}{{gen, "close it as a false positive"}, {pem, "Generate a new key pair"}} {
				doc = secretPage(t, owner, fmt.Sprintf("/org3/secret-guide/security/secrets/%d", c.a.Index), http.StatusOK)
				assert.Contains(t, doc.Find("#secret-guidance").Text(), c.text)
				doc.AssertElement(t, "#secret-guidance a", false)
			}
			secretPage(t, owner, list+"/99", http.StatusNotFound)
			secretPage(t, owner, list+"/abc", http.StatusNotFound)
		})

		t.Run("close and reopen", func(t *testing.T) {
			one := list + "/1"
			since := auditMaxID(t)
			// no reason, won't fix without a comment, a secret in the comment: nothing changes
			for _, v := range []url.Values{
				{},
				{"reason": {"bogus"}},
				{"reason": {"wont_fix"}},
				{"reason": {"wont_fix"}, "comment": {"  "}},
				{"reason": {"revoked"}, "comment": {"it was " + bare}},
				{"reason": {"revoked"}, "comment": {strings.Repeat("é", 600)}},
			} {
				secretPost(t, owner, one+"/close", v, http.StatusSeeOther)
				assert.NotEmpty(t, flashMessage(t, owner), v)
				assert.Equal(t, secretscan_model.StateOpen, secretAlertRow(t, root.ID, 1).State, v)
			}
			assert.Equal(t, since, auditMaxID(t))

			secretPost(t, owner, one+"/close", url.Values{"reason": {"wont_fix"}, "comment": {"Accepted: internal test host, SEC-7."}}, http.StatusSeeOther)
			assert.Contains(t, flashMessage(t, owner), "was closed")
			row := secretAlertRow(t, root.ID, 1)
			assert.Equal(t, secretscan_model.StateWontFix, row.State)
			assert.Equal(t, "Accepted: internal test host, SEC-7.", row.Comment)
			assert.Equal(t, user2.ID, row.ClosedByID)
			e := auditOne(t, since, "repo.secret_alert.closed")
			assert.Equal(t, user2.ID, e.ActorID)

			doc := secretPage(t, owner, one, http.StatusOK)
			assert.Contains(t, doc.Find("#alert-state").Text(), "Won't fix")
			doc.AssertElement(t, "#secret-reopen", true)
			doc.AssertElement(t, "#secret-close", false)
			assert.Contains(t, doc.Find("#secret-activity").Text(), "closed it as Won't fix")
			assert.Contains(t, doc.Find("#secret-activity").Text(), "Accepted: internal test host, SEC-7.")
			doc = secretPage(t, owner, list+"?state=closed&reason=wont_fix", http.StatusOK)
			assert.Equal(t, 1, doc.Find(".secret-alerts-table .repo-security-row:not(.repo-security-head)").Length())
			assert.Contains(t, doc.Find(".secret-alerts-table").Text(), "user2")
			doc = secretPage(t, owner, list+"?state=closed&reason=revoked", http.StatusOK)
			doc.AssertElement(t, "#secret-no-match", true)
			doc = secretPage(t, owner, list, http.StatusOK)
			assert.Equal(t, "2 Open", strings.TrimSpace(doc.Find(".repo-security-tabs a").First().Text()))
			assert.Equal(t, "1 Closed", strings.TrimSpace(doc.Find(".repo-security-tabs a").Eq(1).Text()))

			// closing a closed alert is a conflict, shown as a message
			secretPost(t, owner, one+"/close", url.Values{"reason": {"revoked"}}, http.StatusSeeOther)
			assert.Contains(t, flashMessage(t, owner), "changed meanwhile")

			since = auditMaxID(t)
			secretPost(t, owner, one+"/reopen", url.Values{}, http.StatusSeeOther)
			assert.Equal(t, secretscan_model.StateOpen, secretAlertRow(t, root.ID, 1).State)
			auditOne(t, since, "repo.secret_alert.reopened")
			doc = secretPage(t, owner, one, http.StatusOK)
			assert.Contains(t, doc.Find("#secret-activity").Text(), "reopened it")
		})

		t.Run("bulk close", func(t *testing.T) {
			since := auditMaxID(t)
			secretPost(t, owner, list+"/close", url.Values{"reason": {"false_positive"}}, http.StatusSeeOther) // nothing selected
			assert.NotEmpty(t, flashMessage(t, owner))
			secretPost(t, owner, list+"/close", url.Values{"index": {"1", "2", "2", "99", "x"}}, http.StatusSeeOther) // no reason
			assert.NotEmpty(t, flashMessage(t, owner))
			assert.Equal(t, since, auditMaxID(t))
			secretPost(t, owner, list+"/close", url.Values{"index": {"1", "2", "2", "99", "x"}, "reason": {"used_in_tests"}, "comment": {"fixtures"}}, http.StatusSeeOther)
			flash := flashMessage(t, owner)
			assert.Contains(t, flash, "2 alerts were closed")
			assert.Contains(t, flash, "2 alerts were not closed")
			assert.Equal(t, secretscan_model.StateUsedInTests, secretAlertRow(t, root.ID, 1).State)
			assert.Equal(t, secretscan_model.StateUsedInTests, secretAlertRow(t, root.ID, 2).State)
			var closed []*audit_model.Entry
			require.NoError(t, db.GetEngine(t.Context()).Where("id > ? AND action = ?", since, "repo.secret_alert.closed").Find(&closed))
			assert.Len(t, closed, 2, "one audit entry per alert")
			for _, i := range []int64{1, 2} {
				secretPost(t, owner, fmt.Sprintf("%s/%d/reopen", list, i), url.Values{}, http.StatusSeeOther)
			}
		})

		t.Run("who sees the pages", func(t *testing.T) {
			// anonymous, a user without access and a reader get the 404 of a missing page, before anything else
			missing := NewRequest(t, "GET", "/org3/no-such-repo/security/secrets")
			missingBody := MakeRequest(t, missing, http.StatusNotFound).Body.String()
			_ = missingBody
			for _, path := range []string{list, list + "/1", list + "/99", list + "/abc", "/org3/webproj/web-leaf/security/secrets"} {
				MakeRequest(t, NewRequest(t, "GET", path), http.StatusNotFound)
				secretPage(t, loginUser(t, "user4"), path, http.StatusNotFound)
			}
			reader := loginUser(t, "user4")
			for _, r := range []*repo_model.Repository{root, leaf} {
				require.NoError(t, repo_module.AddCollaborator(sys, r, user4, perm_model.AccessModeRead))
			}
			for _, path := range []string{list, list + "/1", "/org3/webproj/web-leaf/security/secrets"} {
				secretPage(t, reader, path, http.StatusNotFound)
			}
			secretPage(t, reader, "/org3/secret-web", http.StatusOK).AssertElement(t, "#security-open-alerts", false)
			secretPost(t, reader, list+"/1/close", url.Values{"reason": {"revoked"}}, http.StatusNotFound)
			secretPost(t, reader, list+"/rescan", url.Values{}, http.StatusNotFound)
			// a writer reads and closes, but can't rescan; an admin can
			for _, r := range []*repo_model.Repository{root, leaf} {
				require.NoError(t, repo_module.ChangeCollaborationAccessMode(sys, r, user4.ID, perm_model.AccessModeWrite))
			}
			secretPage(t, reader, list, http.StatusOK).AssertElement(t, "#secret-rescan", false)
			secretPage(t, reader, "/org3/webproj/web-leaf/security/secrets", http.StatusOK)
			secretPost(t, reader, list+"/rescan", url.Values{}, http.StatusNotFound)
			secretPost(t, reader, list+"/1/close", url.Values{"reason": {"revoked"}}, http.StatusSeeOther)
			secretPost(t, reader, list+"/1/reopen", url.Values{}, http.StatusSeeOther)
			require.NoError(t, repo_module.ChangeCollaborationAccessMode(sys, root, user4.ID, perm_model.AccessModeAdmin))
			secretPage(t, reader, list, http.StatusOK).AssertElement(t, "#secret-rescan", true)
			for _, r := range []*repo_model.Repository{root, leaf} {
				require.NoError(t, repo_module.DeleteCollaboration(sys, r, user4.ID))
			}
			secretPage(t, reader, list, http.StatusNotFound)
			// a site administrator
			secretPage(t, loginUser(t, "user1"), list, http.StatusOK)
		})

		t.Run("auditor reads, never writes", func(t *testing.T) {
			_, err := db.GetEngine(t.Context()).Exec("UPDATE `user` SET is_auditor = ? WHERE id = 8", true)
			require.NoError(t, err)
			defer func() {
				_, err := db.GetEngine(t.Context()).Exec("UPDATE `user` SET is_auditor = ? WHERE id = 8", false)
				require.NoError(t, err)
			}()
			auditor := loginUser(t, "user8")
			doc := secretPage(t, auditor, list, http.StatusOK)
			doc.AssertElement(t, "#secret-rescan", false)
			doc.AssertElement(t, "#secret-bulk-modal", false)
			doc.AssertElement(t, `input[name="index"]`, false)
			doc = secretPage(t, auditor, list+"/1", http.StatusOK)
			doc.AssertElement(t, "#secret-close", false)
			doc.AssertElement(t, "#secret-reopen", false)
			secretPost(t, auditor, list+"/1/close", url.Values{"reason": {"revoked"}}, http.StatusNotFound)
			secretPost(t, auditor, list+"/rescan", url.Values{}, http.StatusNotFound)
			// not a repository whose code the auditor can't read
			removeCodeUnit(t, root.ID)
			secretPage(t, auditor, list, http.StatusNotFound)
		})

		t.Run("wiki only alerts need Wiki read", func(t *testing.T) {
			team := &organization.Team{OrgID: org3.ID, Name: "codeonly-web", LowerName: "codeonly-web", AccessMode: perm_model.AccessModeWrite}
			require.NoError(t, db.Insert(t.Context(), team))
			require.NoError(t, db.Insert(t.Context(), &organization.TeamUnit{OrgID: org3.ID, TeamID: team.ID, Type: unit.TypeCode, AccessMode: perm_model.AccessModeWrite}))
			require.NoError(t, db.Insert(t.Context(), &organization.TeamRepo{OrgID: org3.ID, TeamID: team.ID, RepoID: root.ID}))
			require.NoError(t, db.Insert(t.Context(), &organization.TeamUser{OrgID: org3.ID, TeamID: team.ID, UID: user5.ID}))
			require.NoError(t, db.Insert(t.Context(), &organization.OrgUser{OrgID: org3.ID, UID: user5.ID}))
			require.NoError(t, access_model.RecalculateAccesses(t.Context(), root))
			perm, err := access_model.GetUserRepoPermission(t.Context(), root, user5)
			require.NoError(t, err)
			require.True(t, perm.CanWrite(unit.TypeCode))
			require.False(t, perm.CanRead(unit.TypeWiki))
			codeOnly := loginUser(t, "user5")
			doc := secretPage(t, codeOnly, list, http.StatusOK)
			assert.Equal(t, "2 Open", strings.TrimSpace(doc.Find(".repo-security-tabs a").First().Text()), "without the wiki-only alert")
			assert.NotContains(t, doc.Find(".secret-alerts-table").Text(), "Home.md")
			assert.Equal(t, 0, secretPage(t, codeOnly, list+"?q=Home.md", http.StatusOK).Find(".secret-alerts-table").Length())
			secretPage(t, codeOnly, list+"/3", http.StatusNotFound)
			secretPost(t, codeOnly, list+"/3/close", url.Values{"reason": {"revoked"}}, http.StatusNotFound)
			// a bulk close of a hidden alert counts as not closed
			secretPost(t, codeOnly, list+"/close", url.Values{"index": {"3"}, "reason": {"revoked"}}, http.StatusSeeOther)
			assert.Contains(t, flashMessage(t, codeOnly), "not closed")
			assert.Equal(t, secretscan_model.StateOpen, secretAlertRow(t, root.ID, 3).State)
			// an alert with a place in the code too stays, without its wiki place
			both := seedSecretAlert(t, root, "github-token", "github", secretscan_model.KindRepo, "both.txt")
			l := secretscan_model.NewLocation(root.ID, secretscan_model.KindWiki, "blobw", "Both.md", 1, 1, "wikicommit", "Wiki Author", "wiki@example.com", 1700000000)
			l.AlertID = both.ID
			_, err = db.GetEngine(t.Context()).Insert(l)
			require.NoError(t, err)
			doc = secretPage(t, codeOnly, fmt.Sprintf("%s/%d", list, both.Index), http.StatusOK)
			assert.Equal(t, 1, doc.Find(".secret-locations .repo-security-row:not(.repo-security-head)").Length())
			assert.NotContains(t, doc.Find("body").Text(), "Wiki Author")
			assert.Equal(t, 2, secretPage(t, owner, fmt.Sprintf("%s/%d", list, both.Index), http.StatusOK).Find(".secret-locations .repo-security-row:not(.repo-security-head)").Length())
		})

		t.Run("archived, maintenance and scanning off", func(t *testing.T) {
			require.NoError(t, repo_service.SetArchiveRepoState(sys, root, true))
			secretPost(t, owner, list+"/2/close", url.Values{"reason": {"revoked"}}, http.StatusNotFound)
			doc := secretPage(t, owner, list+"/2", http.StatusOK)
			doc.AssertElement(t, "#secret-close", false)
			doc.AssertElement(t, "#secret-rescan", false)
			require.NoError(t, repo_service.SetArchiveRepoState(sys, root, false))

			require.NoError(t, maintenance_service.Set(sys, nil, setting.MaintenanceReadOnly, maintenanceMessage, time.Time{}))
			secretPost(t, owner, list+"/2/close", url.Values{"reason": {"revoked"}}, http.StatusSeeOther)
			assertMaintenanceFlash(t, owner)
			assert.Equal(t, secretscan_model.StateOpen, secretAlertRow(t, root.ID, 2).State)
			secretPage(t, owner, list, http.StatusOK) // reading works
			require.NoError(t, maintenance_service.Set(sys, nil, setting.MaintenanceOff, "", time.Time{}))

			// off: the alerts stay, read-only, under a banner; writes are refused
			root.SecretScanningDisabled = true
			require.NoError(t, repo_model.UpdateRepositoryCols(t.Context(), root, "secret_scanning_disabled"))
			doc = secretPage(t, owner, list, http.StatusOK)
			doc.AssertElement(t, "#secret-banner-off", true)
			doc.AssertElement(t, `input[name="index"]`, false)
			doc.AssertElement(t, "#secret-rescan", false)
			assert.Contains(t, doc.Find("#secret-scan-status").Text(), "Scanning off")
			assert.Equal(t, 4, doc.Find(".secret-alerts-table .repo-security-row:not(.repo-security-head)").Length())
			doc = secretPage(t, owner, list+"/2", http.StatusOK)
			doc.AssertElement(t, "#secret-close", false)
			secretPost(t, owner, list+"/2/close", url.Values{"reason": {"revoked"}}, http.StatusNotFound)
			secretPost(t, owner, list+"/rescan", url.Values{}, http.StatusNotFound)
			// the site switch off: the same, and no alerts left means no page
			defer test.MockVariableValue(&setting.SecretScanning.Enabled, false)()
			bumpSettingsRevision(t)
			doc = secretPage(t, owner, list, http.StatusOK)
			assert.Contains(t, doc.Find("#secret-banner-off").Text(), "off on this instance")
			empty := depNewRepo(t, org3, user2, "secret-none", true, 0)
			secretPage(t, owner, "/org3/secret-none/security/secrets", http.StatusNotFound)
			_ = empty
		})
	})
}

func TestSecretAlertsRescanAndSettings(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		defer test.MockVariableValue(&setting.SecretScanning.Enabled, true)()
		bumpSettingsRevision(t)
		org3 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 3})
		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		repo := depNewRepo(t, org3, user2, "secret-rescan", true, 0)
		seedSecretRepo(t, repo)
		list := "/org3/secret-rescan/security/secrets"
		owner := loginUser(t, "user2")

		t.Run("rescan clears the scan state and keeps the alerts", func(t *testing.T) {
			for _, kind := range []string{secretscan_model.KindRepo, secretscan_model.KindWiki} {
				require.NoError(t, secretscan_model.Save(t.Context(), nil, &secretscan_model.Scan{
					RepoID: repo.ID, Kind: kind, ScannedTips: `["abc"]`, Status: secretscan_model.StatusIncomplete, Error: "over_limit", Holes: 3, SkippedLarge: 2,
				}))
			}
			doc := secretPage(t, owner, list, http.StatusOK)
			doc.AssertElement(t, "#secret-banner-incomplete", true)
			assert.Contains(t, doc.Find("#secret-banner-incomplete").Text(), "6 files or secrets were left out")
			since := auditMaxID(t)
			secretPost(t, owner, list+"/rescan", url.Values{}, http.StatusSeeOther)
			assert.Contains(t, flashMessage(t, owner), "new scan was queued")
			// the queues are immediate in tests, so the new scan has already run: from the beginning, without the holes
			scans, err := secretscan_model.FindScans(t.Context(), repo.ID)
			require.NoError(t, err)
			for _, sc := range scans {
				assert.Zero(t, sc.Holes)
				assert.NotEqual(t, `["abc"]`, sc.ScannedTips)
				assert.NotEqual(t, "over_limit", sc.Error)
			}
			open, closed, err := secretscan_model.CountByState(t.Context(), repo.ID, false)
			require.NoError(t, err)
			assert.EqualValues(t, 3, open+closed)
			e := auditOne(t, since, "repo.secret_scanning.rescanned")
			assert.Equal(t, repo.ID, e.RepoID)
			doc = secretPage(t, owner, list, http.StatusOK)
			doc.AssertElement(t, "#secret-banner-incomplete", false)
		})

		t.Run("failed and ok status", func(t *testing.T) {
			require.NoError(t, secretscan_model.DeleteScans(t.Context(), repo.ID))
			require.NoError(t, secretscan_model.Save(t.Context(), nil, &secretscan_model.Scan{RepoID: repo.ID, Kind: secretscan_model.KindRepo, ScannedTips: "[]", Status: secretscan_model.StatusFailed, Error: "git_error"}))
			doc := secretPage(t, owner, list, http.StatusOK)
			doc.AssertElement(t, "#secret-banner-failed", true)
			doc.AssertElement(t, "#secret-rescan", true)
			require.NoError(t, secretscan_model.DeleteScans(t.Context(), repo.ID))
			require.NoError(t, secretscan_model.Save(t.Context(), nil, &secretscan_model.Scan{RepoID: repo.ID, Kind: secretscan_model.KindRepo, ScannedTips: "[]", Status: secretscan_model.StatusOK}))
			doc = secretPage(t, owner, list, http.StatusOK)
			assert.Contains(t, doc.Find("#secret-scan-status").Text(), "Last scanned")
			doc.AssertElement(t, "#secret-banner-failed", false)
		})

		t.Run("settings: the switch, the confirmation, the locks", func(t *testing.T) {
			settings := "/org3/secret-rescan/settings/code_security"
			doc := secretPage(t, owner, settings, http.StatusOK)
			doc.AssertElement(t, "#secret-scanning-enabled[checked]", true)
			doc.AssertElement(t, "#secret-scanning-off-modal", true)
			assert.Contains(t, doc.Find("#secret-scanning-off-modal").Text(), "3 open alerts are kept")
			since := auditMaxID(t)
			secretPost(t, owner, settings+"/secret_scanning", url.Values{}, http.StatusSeeOther) // no "enabled": off
			assert.Contains(t, flashMessage(t, owner), "settings saved")
			repo = unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: repo.ID})
			assert.True(t, repo.SecretScanningDisabled)
			e := auditOne(t, since, "repo.repository.updated")
			assert.Contains(t, e.Data, `"secret_scanning":false`)
			doc = secretPage(t, owner, settings, http.StatusOK)
			doc.AssertElement(t, "#secret-scanning-enabled[checked]", false)
			assert.Contains(t, doc.Find("#secret-scanning-set-by").Text(), "Turned off by")
			doc = secretPage(t, owner, list, http.StatusOK)
			assert.Contains(t, doc.Find("#secret-banner-off").Text(), "Turned off by")
			doc.AssertElement(t, `#secret-banner-off a[href$="/settings/code_security"]`, true)

			// nothing recorded for a save that changes nothing; on again
			since = auditMaxID(t)
			secretPost(t, owner, settings+"/secret_scanning", url.Values{}, http.StatusSeeOther)
			assert.Equal(t, since, auditMaxID(t))
			secretPost(t, owner, settings+"/secret_scanning", url.Values{"enabled": {"on"}}, http.StatusSeeOther)
			assert.False(t, unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: repo.ID}).SecretScanningDisabled)

			// only repository admins; a writer gets the 404 of the settings
			user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
			require.NoError(t, repo_module.AddCollaborator(audit_model.WithSystem(db.DefaultContext, "test"), repo, user4, perm_model.AccessModeWrite))
			secretPost(t, loginUser(t, "user4"), settings+"/secret_scanning", url.Values{}, http.StatusNotFound)
			assert.False(t, unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: repo.ID}).SecretScanningDisabled)

			// the organization locks it on: shown disabled, nothing saved
			require.NoError(t, user_model.SetUserSetting(t.Context(), org3.ID, secretscan_model.OrgLockedKey, "true"))
			doc = secretPage(t, owner, settings, http.StatusOK)
			doc.AssertElement(t, "#secret-scanning-box fieldset[disabled]", true)
			assert.Contains(t, doc.Find("#secret-scanning-set-by").Text(), "turns it on for all its repositories")
			secretPost(t, owner, settings+"/secret_scanning", url.Values{}, http.StatusSeeOther)
			assert.Contains(t, flashMessage(t, owner), "nothing was saved")
			assert.False(t, unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: repo.ID}).SecretScanningDisabled)
			// off for the organization: shown disabled, the list shows the banner of the organization
			require.NoError(t, user_model.SetUserSetting(t.Context(), org3.ID, secretscan_model.OrgKey, "false"))
			doc = secretPage(t, owner, settings, http.StatusOK)
			assert.Contains(t, doc.Find("#secret-scanning-set-by").Text(), "turns it off for all its repositories")
			doc = secretPage(t, owner, list, http.StatusOK)
			assert.Contains(t, doc.Find("#secret-banner-off").Text(), "off for all repositories of org3")
			require.NoError(t, user_model.DeleteUserSetting(t.Context(), org3.ID, secretscan_model.OrgKey))
			require.NoError(t, user_model.DeleteUserSetting(t.Context(), org3.ID, secretscan_model.OrgLockedKey))
		})
	})
}
