// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"net/http"
	"strconv"
	"testing"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	"forgejo.org/models/organization"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An org owner links team1 (write; user2, user4) to the project on its settings page: its members reach the private
// repositories of the project, the team and collaborators pages say where the access comes from, a remove through
// the web or the API keeps it, and unlinking takes it back.
func TestRepoGroupTeams(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	f := prepareRepoGroupWeb(t)
	owner := loginUser(t, "user2")
	member := loginUser(t, "user4")
	proj, err := repo_model.GetRepoGroupByName(db.DefaultContext, f.org.ID, "proj")
	require.NoError(t, err)
	viaRow := func() *organization.TeamRepo {
		row := new(organization.TeamRepo)
		has, err := db.GetEngine(db.DefaultContext).Where("team_id = 2 AND repo_id = ?", f.priv.ID).Get(row)
		require.NoError(t, err)
		if !has {
			return nil
		}
		return row
	}
	link := func(session *TestSession, teamID int64, status int) {
		t.Helper()
		session.MakeRequest(t, NewRequestWithValues(t, "POST", "/org3/proj/-/settings/teams/link", map[string]string{"team_id": strconv.FormatInt(teamID, 10)}), status)
	}
	member.MakeRequest(t, NewRequest(t, "GET", "/org3/proj/priv"), http.StatusNotFound)

	t.Run("only owners link", func(t *testing.T) {
		link(member, 2, http.StatusNotFound)
		link(owner, 3, http.StatusNotFound) // org6's team
		resp := owner.MakeRequest(t, NewRequest(t, "GET", "/org3/proj/-/settings"), http.StatusOK)
		assert.Contains(t, resp.Body.String(), `name="team_id"`)
		_, err := db.GetEngine(db.DefaultContext).ID(f.org.ID).Cols("is_archived").Update(&user_model.User{IsArchived: true})
		require.NoError(t, err)
		link(owner, 2, http.StatusForbidden)
		_, err = db.GetEngine(db.DefaultContext).ID(f.org.ID).Cols("is_archived").Update(&user_model.User{IsArchived: false})
		require.NoError(t, err)
		unittest.AssertNotExistsBean(t, &organization.TeamGroup{TeamID: 2})
	})

	t.Run("link grants", func(t *testing.T) {
		link(owner, 2, http.StatusSeeOther)
		require.True(t, viaRow().ViaGroup)
		member.MakeRequest(t, NewRequest(t, "GET", "/org3/proj/priv"), http.StatusOK)
		req := NewRequest(t, "GET", "/org3/proj/priv.git/info/refs?service=git-upload-pack")
		req.SetBasicAuth("user4", getTokenForLoggedInUser(t, member, auth_model.AccessTokenScopeReadRepository))
		MakeRequest(t, req, http.StatusOK)
		resp := owner.MakeRequest(t, NewRequest(t, "GET", "/org3/proj/-/settings"), http.StatusOK)
		assert.Contains(t, resp.Body.String(), `data-url="/org3/proj/-/settings/teams/unlink" data-id="2"`)
	})

	t.Run("labels instead of remove", func(t *testing.T) {
		resp := owner.MakeRequest(t, NewRequest(t, "GET", "/org/org3/teams/team1/repositories"), http.StatusOK)
		assert.Contains(t, resp.Body.String(), `aria-label="via project proj"`)
		assert.NotContains(t, resp.Body.String(), `value="`+strconv.FormatInt(f.priv.ID, 10)+`">Remove`)
		resp = owner.MakeRequest(t, NewRequest(t, "GET", "/org3/proj/priv/settings/collaboration"), http.StatusOK)
		assert.Contains(t, resp.Body.String(), `aria-label="via project proj"`)
	})

	t.Run("removing keeps the link's grant", func(t *testing.T) {
		owner.MakeRequest(t, NewRequestWithValues(t, "POST", "/org/org3/teams/team1/action/repo/remove", map[string]string{"repoid": strconv.FormatInt(f.priv.ID, 10)}), http.StatusSeeOther)
		owner.MakeRequest(t, NewRequestWithValues(t, "POST", "/org3/proj/priv/settings/collaboration/team/delete", map[string]string{"id": "2"}), http.StatusOK)
		token := getTokenForLoggedInUser(t, owner, auth_model.AccessTokenScopeWriteRepository)
		MakeRequest(t, NewRequest(t, "DELETE", "/api/v1/repos/org3/proj/priv/-/teams/team1").AddTokenAuth(token), http.StatusNoContent)
		MakeRequest(t, NewRequest(t, "PUT", "/api/v1/repos/org3/proj/priv/-/teams/team1").AddTokenAuth(token), http.StatusUnprocessableEntity)
		require.True(t, viaRow().ViaGroup)
		member.MakeRequest(t, NewRequest(t, "GET", "/org3/proj/priv"), http.StatusOK)
		unittest.CheckTeamGroupRows(t)
	})

	t.Run("unlink revokes", func(t *testing.T) {
		member.MakeRequest(t, NewRequestWithValues(t, "POST", "/org3/proj/-/settings/teams/unlink", map[string]string{"id": "2"}), http.StatusNotFound)
		owner.MakeRequest(t, NewRequestWithValues(t, "POST", "/org3/proj/-/settings/teams/unlink", map[string]string{"id": "2"}), http.StatusOK)
		assert.Nil(t, viaRow())
		member.MakeRequest(t, NewRequest(t, "GET", "/org3/proj/priv"), http.StatusNotFound)
		unittest.AssertNotExistsBean(t, &organization.TeamGroup{TeamID: 2, GroupID: proj.ID})
		owner.MakeRequest(t, NewRequestWithValues(t, "POST", "/org3/proj/-/settings/teams/unlink", map[string]string{"id": "2"}), http.StatusNotFound)
		unittest.CheckTeamGroupRows(t)
	})
}
