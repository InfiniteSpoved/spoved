// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	secretscan_model "forgejo.org/models/secretscan"
	"forgejo.org/modules/setting"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/test"
	secretscan_service "forgejo.org/services/secretscan"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// textPlace reads the place of a kind of the alert, inside Eventually: no require, no t.Context().
func textPlace(alertID int64, kind string) *secretscan_model.Location {
	var l secretscan_model.Location
	if has, err := db.GetEngine(db.DefaultContext).Where("alert_id = ? AND kind = ?", alertID, kind).Get(&l); err != nil || !has {
		return nil
	}
	return &l
}

// A secret written in an issue and in a comment is found when the text is saved, follows an edit and a delete, and
// shows up in the alert list but not in its places (the viewer's access to the issues is not checked yet).
func TestSecretTextScanFlow(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		defer test.MockVariableValue(&setting.SecretScanning.Enabled, true)()
		defer test.MockVariableValue(&setting.SecretScanning.ScanText, true)()
		defer test.MockVariableValue(&setting.SecretScanning.MaxTextBytes, 1<<20)()
		write := getUserToken(t, "user2", auth_model.AccessTokenScopeWriteIssue, auth_model.AccessTokenScopeWriteRepository)
		const base = "/api/v1/repos/user2/repo1"
		issueTok, commentTok := secretToken("text-issue"), secretToken("text-comment")

		var issue api.Issue
		resp := MakeRequest(t, NewRequestWithJSON(t, "POST", base+"/issues", api.CreateIssueOption{Title: "a leak", Body: "line one\nkey: " + issueTok}).AddTokenAuth(write), http.StatusCreated)
		DecodeJSON(t, resp, &issue)
		a := secretAlertOf(t, 1, issueTok)
		assert.True(t, a.ViaPull, "text shares the cap of the pull refs")
		var l *secretscan_model.Location
		require.Eventually(t, func() bool { l = textPlace(a.ID, secretscan_model.KindIssue); return l != nil }, 10*time.Second, 50*time.Millisecond)
		assert.Equal(t, 2, l.StartLine)
		assert.Equal(t, "body", l.Field)

		var comment api.Comment
		resp = MakeRequest(t, NewRequestWithJSON(t, "POST", fmt.Sprintf("%s/issues/%d/comments", base, issue.Index), api.CreateIssueCommentOption{Body: "and " + commentTok}).AddTokenAuth(write), http.StatusCreated)
		DecodeJSON(t, resp, &comment)
		b := secretAlertOf(t, 1, commentTok)
		require.Eventually(t, func() bool { return textPlace(b.ID, secretscan_model.KindComment) != nil }, 10*time.Second, 50*time.Millisecond)

		t.Run("the alert is listed, its places in text are not", func(t *testing.T) {
			var list []*api.SecretScanningAlert
			DecodeJSON(t, MakeRequest(t, NewRequest(t, "GET", base+"/secret-scanning/alerts").AddTokenAuth(write), http.StatusOK), &list)
			numbers := map[int64]bool{}
			for _, al := range list {
				numbers[al.Number] = true
			}
			assert.True(t, numbers[a.Index] && numbers[b.Index])
			var locs []*api.SecretScanningLocation
			DecodeJSON(t, MakeRequest(t, NewRequest(t, "GET", fmt.Sprintf("%s/secret-scanning/alerts/%d/locations", base, a.Index)).AddTokenAuth(write), http.StatusOK), &locs)
			assert.Empty(t, locs)
		})

		t.Run("an edit that removes the secret marks the place, a delete too", func(t *testing.T) {
			MakeRequest(t, NewRequestWithJSON(t, "PATCH", fmt.Sprintf("%s/issues/%d", base, issue.Index), api.EditIssueOption{Body: new("fixed")}).AddTokenAuth(write), http.StatusCreated)
			require.Eventually(t, func() bool { p := textPlace(a.ID, secretscan_model.KindIssue); return p != nil && p.RemovedUnix != 0 }, 10*time.Second, 50*time.Millisecond)
			MakeRequest(t, NewRequest(t, "DELETE", fmt.Sprintf("%s/issues/comments/%d", base, comment.ID)).AddTokenAuth(write), http.StatusNoContent)
			require.Eventually(t, func() bool { p := textPlace(b.ID, secretscan_model.KindComment); return p != nil && p.RemovedUnix != 0 }, 10*time.Second, 50*time.Millisecond)
			assert.Equal(t, secretscan_model.StateOpen, secretAlertRow(t, 1, a.Index).State, "the secret was exposed: the alert stays")
		})

		t.Run("with the text scan off nothing is stored", func(t *testing.T) {
			defer test.MockVariableValue(&setting.SecretScanning.ScanText, false)()
			off := secretToken("text-off")
			MakeRequest(t, NewRequestWithJSON(t, "POST", base+"/issues", api.CreateIssueOption{Title: "quiet", Body: off}).AddTokenAuth(write), http.StatusCreated)
			assert.Never(t, func() bool { return secretAlertWith(1, off, 1) != nil }, time.Second, 100*time.Millisecond)
		})
	})
}

// The text scan has a switch on the Site administration page: a post without its field keeps it, and with the field
// (shown marker) an unticked box turns it off.
func TestSecretScanningTextSwitch(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	defer test.MockVariableValue(&setting.SecretScanning.Enabled, true)()
	defer test.MockVariableValue(&setting.SecretScanning.ScanText, true)()
	keepSecretScanningSettings(t)
	const switches = "/admin/config/secret-scanning/switches"
	admin := loginUser(t, "user1")
	effective := func() setting.SecretScanningOptions { return setting.SecretScanningSettings(t.Context()) }
	require.True(t, secretscan_service.SiteEnabled(t.Context()))

	doc := NewHTMLParser(t, admin.MakeRequest(t, NewRequest(t, "GET", "/admin/config/secret-scanning"), http.StatusOK).Body)
	doc.AssertElement(t, "#secret-scanning-text[checked]", true)

	admin.MakeRequest(t, NewRequestWithValues(t, "POST", switches, map[string]string{"enabled": "on", "generic": "on"}), http.StatusSeeOther)
	bumpSettingsRevision(t)
	assert.True(t, effective().ScanText, "a post without the field keeps the setting")

	since := auditMaxID(t)
	admin.MakeRequest(t, NewRequestWithValues(t, "POST", switches, map[string]string{"enabled": "on", "scan_text_shown": "true"}), http.StatusSeeOther)
	bumpSettingsRevision(t)
	assert.False(t, effective().ScanText)
	assert.Contains(t, auditOne(t, since, "admin.setting.changed").Data, `"scan_text":false`)
	doc = NewHTMLParser(t, admin.MakeRequest(t, NewRequest(t, "GET", "/admin/config/secret-scanning"), http.StatusOK).Body)
	doc.AssertElement(t, "#secret-scanning-text[checked]", false)
	doc.AssertElement(t, "#secret-scanning-text-file-default", true)

	admin.MakeRequest(t, NewRequestWithValues(t, "POST", switches, map[string]string{"enabled": "on", "scan_text_shown": "true", "scan_text": "on"}), http.StatusSeeOther)
	bumpSettingsRevision(t)
	assert.True(t, effective().ScanText)

	t.Run("the limit is on the limits form", func(t *testing.T) {
		admin.MakeRequest(t, NewRequestWithValues(t, "POST", "/admin/config/secret-scanning/limits", map[string]string{"max_text_bytes": "64"}), http.StatusSeeOther)
		bumpSettingsRevision(t)
		assert.EqualValues(t, 64<<10, effective().MaxTextBytes)
		admin.MakeRequest(t, NewRequestWithValues(t, "POST", "/admin/config/secret-scanning/limits", map[string]string{"max_text_bytes": "0.5"}), http.StatusUnprocessableEntity)
	})
}
