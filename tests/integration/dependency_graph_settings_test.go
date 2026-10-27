// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"forgejo.org/models/db"
	system_model "forgejo.org/models/system"
	"forgejo.org/modules/dependency"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"
	vulnerability_service "forgejo.org/services/vulnerability"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDependencyGraphSettingsAdmin(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		enableDependencyGraph(t)
		const token = "settings-token-0123456789"
		page := "/admin/config/dependency-graph"
		admin := loginUser(t, "user1")
		var hits, authorized atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			if r.Header.Get("Authorization") != "Bearer "+token {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			authorized.Add(1)
			http.NotFound(w, r)
		}))
		defer srv.Close()
		// the webhook list stays at its default (external): the page allows loopback for the mirror only
		t.Cleanup(test.MockVariableValue(&setting.Webhook.AllowedHostList, ""))
		t.Cleanup(test.MockVariableValue(&setting.AppDataPath, t.TempDir()))
		t.Cleanup(test.MockVariableValue(&vulnerability_service.TestConnectionInterval, 0))
		t.Cleanup(func() {
			require.NoError(t, system_model.SetSettings(db.DefaultContext, map[string]string{setting.Config().DependencyGraphConfig.DynKey(): ""}))
			bumpSettingsRevision(t)
		})
		key := setting.Config().DependencyGraphConfig.DynKey()
		stored := func() string {
			v, _, err := system_model.GetSettingForUpdate(db.DefaultContext, key)
			require.NoError(t, err)
			return v
		}
		post := func(s *TestSession, path string, values map[string]string, status int) {
			t.Helper()
			s.MakeRequest(t, NewRequestWithValues(t, "POST", path, values), status)
			bumpSettingsRevision(t)
		}
		form := map[string]string{
			"source": "mirror", "mirror_url": srv.URL, "sign_in": "token", "token": token, "allowed_hosts": "loopback",
			"update_interval": "12", "max_concurrent_scans": "2",
		}

		t.Run("save", func(t *testing.T) {
			since := auditMaxID(t)
			post(admin, page+"/settings", form, http.StatusSeeOther)
			assert.Contains(t, bwFlash(admin), "The dependency graph settings were saved.")
			assert.NotContains(t, stored(), token, "stored encrypted")
			assert.Contains(t, stored(), "token_enc")
			e := auditOne(t, since, "admin.dependency_graph.settings_changed")
			assert.Equal(t, "admin", e.Category)
			assert.NotContains(t, e.Data, token)
			data := auditData(t, e)
			assert.Equal(t, true, data["token_changed"])
			assert.EqualValues(t, 12*3600, data["after"].(map[string]any)["update_interval"])
			assert.Equal(t, "loopback", data["after"].(map[string]any)["allowed_hosts"])

			cfg := setting.DependencyGraphSettings(t.Context())
			assert.Equal(t, 12*time.Hour, cfg.UpdateInterval)
			assert.Equal(t, 2, cfg.MaxConcurrentScans)
			body := admin.MakeRequest(t, NewRequest(t, "GET", page), http.StatusOK).Body.String()
			assert.NotContains(t, body, token, "the page never holds the token")
		})

		t.Run("an unchanged save writes nothing", func(t *testing.T) {
			since, before := auditMaxID(t), stored()
			unchanged := maps.Clone(form)
			unchanged["token"] = ""
			post(admin, page+"/settings", unchanged, http.StatusSeeOther)
			assert.Contains(t, bwFlash(admin), "Nothing changed.")
			assert.Equal(t, before, stored())
			var n int64
			_, err := db.GetEngine(t.Context()).Table("audit_event").Where("id > ? AND action = ?", since, "admin.dependency_graph.settings_changed").Select("COUNT(*)").Get(&n)
			require.NoError(t, err)
			assert.Zero(t, n)
		})

		t.Run("refusals", func(t *testing.T) {
			before := stored()
			for name, c := range map[string]struct {
				change map[string]string
				flash  string
			}{
				"host change without a token": {map[string]string{"mirror_url": "http://localhost.invalid:1/osv", "token": ""}, "enter the token again"},
				"own host":                    {map[string]string{"mirror_url": setting.AppURL + "osv"}, "points to this instance"},
				"out of range":                {map[string]string{"update_interval": "721"}, "Update every must be 1 to 720"},
				"scans at once":               {map[string]string{"max_concurrent_scans": "65"}, "Scans at once must be 1 to 64"},
				"bad hosts":                   {map[string]string{"allowed_hosts": "bad host"}, "Hosts the mirror may be on: the value can"},
				"basic without a user":        {map[string]string{"sign_in": "basic"}, "Basic sign-in needs a username"},
			} {
				t.Run(name, func(t *testing.T) {
					values := maps.Clone(form)
					maps.Copy(values, c.change)
					post(admin, page+"/settings", values, http.StatusSeeOther)
					assert.Contains(t, bwFlash(admin), c.flash)
					assert.Equal(t, before, stored())
				})
			}
		})

		t.Run("test connection", func(t *testing.T) {
			hits.Store(0)
			post(admin, page+"/test-connection", nil, http.StatusSeeOther)
			assert.Contains(t, bwFlash(admin), "The mirror refused the request.", "the token is sent: the mirror answers 404")
			assert.EqualValues(t, 1, authorized.Load())

			// a wrong token: 401
			values := maps.Clone(form)
			values["token"] = "wrong-token-0123456789"
			post(admin, page+"/settings", values, http.StatusSeeOther)
			post(admin, page+"/test-connection", nil, http.StatusSeeOther)
			assert.Contains(t, bwFlash(admin), "The mirror refused the credentials.")

			// the host list without loopback
			values["allowed_hosts"] = "private"
			post(admin, page+"/settings", values, http.StatusSeeOther)
			before := hits.Load()
			post(admin, page+"/test-connection", nil, http.StatusSeeOther)
			assert.Contains(t, bwFlash(admin), "The mirror's host is not allowed.")
			assert.Equal(t, before, hits.Load())

			// one test per interval
			defer test.MockVariableValue(&vulnerability_service.TestConnectionInterval, time.Hour)()
			post(admin, page+"/test-connection", nil, http.StatusSeeOther)
			assert.Contains(t, bwFlash(admin), "tested a moment ago")
			vulnerability_service.TestConnectionInterval = 0

			// offline: nothing to test
			post(admin, page+"/settings", map[string]string{"source": "none"}, http.StatusSeeOther)
			post(admin, page+"/test-connection", nil, http.StatusSeeOther)
			assert.Contains(t, bwFlash(admin), "the advisory source is None")
		})

		t.Run("remove the token", func(t *testing.T) {
			values := maps.Clone(form)
			post(admin, page+"/settings", values, http.StatusSeeOther)
			require.Equal(t, token, setting.DependencyGraphSettings(t.Context()).OSVMirrorToken)
			since := auditMaxID(t)
			values["token"], values["remove_token"] = "", "on"
			post(admin, page+"/settings", values, http.StatusSeeOther)
			assert.Empty(t, setting.DependencyGraphSettings(t.Context()).OSVMirrorToken)
			e := auditOne(t, since, "admin.dependency_graph.settings_changed")
			assert.Equal(t, true, auditData(t, e)["token_changed"])
		})

		t.Run("live effect", func(t *testing.T) {
			var zipHits atomic.Int32
			zipSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				zipHits.Add(1)
				http.NotFound(w, r)
			}))
			defer zipSrv.Close()
			defer test.MockVariableValue(&vulnerability_service.ManualUpdateInterval, time.Duration(0))()
			// no restart: the next "Update now" reads the saved URL
			post(admin, page+"/settings", map[string]string{"source": "mirror", "mirror_url": zipSrv.URL + "/osv", "sign_in": "none", "allowed_hosts": "loopback", "update_interval": "12"}, http.StatusSeeOther)
			post(admin, page+"/update-advisories", nil, http.StatusSeeOther)
			assert.Contains(t, bwFlash(admin), "The advisory update was started.")
			require.Eventually(t, func() bool { return !vulnerability_service.Running() }, 30*time.Second, 50*time.Millisecond)
			assert.EqualValues(t, len(dependency.Ecosystems), zipHits.Load())
			doc := NewHTMLParser(t, admin.MakeRequest(t, NewRequest(t, "GET", page), http.StatusOK).Body)
			assert.Contains(t, doc.Find("#advisory-mirror").Text(), zipSrv.URL+"/osv")
			assert.Contains(t, doc.Find("#advisory-mirror").Text(), "12h", "the saved interval")
		})

		t.Run("access", func(t *testing.T) {
			_, err := db.GetEngine(t.Context()).Exec("UPDATE `user` SET is_auditor = ? WHERE id = 8", true)
			require.NoError(t, err)
			defer func() {
				_, err := db.GetEngine(t.Context()).Exec("UPDATE `user` SET is_auditor = ? WHERE id = 8", false)
				require.NoError(t, err)
			}()
			auditor := loginUser(t, "user8")
			auditor.MakeRequest(t, NewRequest(t, "GET", page), http.StatusOK)
			user := loginUser(t, "user2")
			before, since := stored(), auditMaxID(t)
			for _, path := range []string{page + "/settings", page + "/test-connection"} {
				auditor.MakeRequest(t, NewRequestWithValues(t, "POST", path, form), http.StatusForbidden)
				user.MakeRequest(t, NewRequestWithValues(t, "POST", path, form), http.StatusForbidden)
				resp := MakeRequest(t, NewRequestWithValues(t, "POST", path, form), http.StatusSeeOther)
				assert.Contains(t, resp.Header().Get("Location"), "/user/login")
			}
			assert.Equal(t, before, stored())
			assert.Equal(t, since, auditMaxID(t))
		})
	})
}
