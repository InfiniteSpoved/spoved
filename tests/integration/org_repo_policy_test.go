// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"net/http"
	"strconv"
	"testing"

	auth_model "forgejo.org/models/auth"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	api "forgejo.org/modules/structs"
	trash_service "forgejo.org/services/trash"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// org3's only owner is user2; user4 is a member that the tests make an admin collaborator of each repository.

func setOrgRepoPolicies(t *testing.T, restrict bool, forkPolicy int) {
	t.Helper()
	org := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "org3"})
	org.RestrictRepoVisibilityChange, org.RestrictRepoDeleteTransfer, org.PrivateForkPolicy = restrict, restrict, forkPolicy
	require.NoError(t, user_model.UpdateUserCols(t.Context(), org, "restrict_repo_visibility_change", "restrict_repo_delete_transfer", "private_fork_policy"))
}

// orgPolicyRepo creates org3/<name> as user2 and makes user4 an admin collaborator of it.
func orgPolicyRepo(t *testing.T, name string, private bool) *repo_model.Repository {
	t.Helper()
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
	admin := "admin"
	MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/orgs/org3/repos", &api.CreateRepoOption{Name: name, Private: private, AutoInit: true}).AddTokenAuth(token), http.StatusCreated)
	MakeRequest(t, NewRequestWithJSON(t, "PUT", "/api/v1/repos/org3/"+name+"/collaborators/user4", &api.AddCollaboratorOption{Permission: &admin}).AddTokenAuth(token), http.StatusNoContent)
	return unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{OwnerName: "org3", LowerName: name})
}

func orgPolicyToken(t *testing.T, user string) string {
	t.Helper()
	return getTokenForLoggedInUser(t, loginUser(t, user), auth_model.AccessTokenScopeAll)
}

// assertPolicyRefusedAPI checks a 403 that names the policy.
func assertPolicyRefusedAPI(t *testing.T, req *RequestWrapper, policy string) {
	t.Helper()
	resp := MakeRequest(t, req, http.StatusForbidden)
	var apiErr api.APIError
	DecodeJSON(t, resp, &apiErr)
	assert.Contains(t, apiErr.Message, policy)
}

// assertPolicyRefusedWeb checks that the page shows the translated error that names the policy.
func assertPolicyRefusedWeb(t *testing.T, user, url string, values map[string]string, label string) {
	t.Helper()
	resp := loginUser(t, user).MakeRequest(t, NewRequestWithValues(t, "POST", url, values), http.StatusOK)
	assert.Contains(t, NewHTMLParser(t, resp.Body).Find(".flash-error").Text(), "Blocked by the organization policy \""+label+"\"")
}

const (
	orgPolicyDeleteLabel     = "Only organization owners can delete or transfer repositories"
	orgPolicyVisibilityLabel = "Only organization owners can change the visibility of repositories"
)

// AC1, AC8: the owner edits the policies on the web and through the API; non-owners can't.
func TestOrgRepoPolicySettings(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	loginUser(t, "user2").MakeRequest(t, NewRequestWithValues(t, "POST", "/org/org3/settings", map[string]string{
		"name":                            "org3",
		"visibility":                      "0",
		"restrict_repo_visibility_change": "on",
		"restrict_repo_delete_transfer":   "on",
		"private_fork_policy":             "2",
	}), http.StatusSeeOther)
	org := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "org3"})
	assert.True(t, org.RestrictRepoVisibilityChange)
	assert.True(t, org.RestrictRepoDeleteTransfer)
	assert.Equal(t, user_model.PrivateForkNone, org.PrivateForkPolicy)

	// an out-of-range fork policy is refused
	loginUser(t, "user2").MakeRequest(t, NewRequestWithValues(t, "POST", "/org/org3/settings", map[string]string{
		"name": "org3", "visibility": "0", "private_fork_policy": "3",
	}), http.StatusOK)
	assert.Equal(t, user_model.PrivateForkNone, unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "org3"}).PrivateForkPolicy)

	f, org2 := false, "org"
	req := NewRequestWithJSON(t, "PATCH", "/api/v1/orgs/org3", &api.EditOrgOption{RestrictRepoVisibilityChange: &f, PrivateForkPolicy: &org2}).AddTokenAuth(orgPolicyToken(t, "user2"))
	var apiOrg api.Organization
	DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &apiOrg)
	assert.False(t, apiOrg.RestrictRepoVisibilityChange)
	assert.True(t, apiOrg.RestrictRepoDeleteTransfer)
	assert.Equal(t, "org", apiOrg.PrivateForkPolicy)
	org = unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "org3"})
	assert.False(t, org.RestrictRepoVisibilityChange)
	assert.Equal(t, user_model.PrivateForkOrg, org.PrivateForkPolicy)

	bad := "nobody"
	MakeRequest(t, NewRequestWithJSON(t, "PATCH", "/api/v1/orgs/org3", &api.EditOrgOption{PrivateForkPolicy: &bad}).AddTokenAuth(orgPolicyToken(t, "user2")), http.StatusUnprocessableEntity)

	// user4 is a member, not an owner
	MakeRequest(t, NewRequestWithJSON(t, "PATCH", "/api/v1/orgs/org3", &api.EditOrgOption{RestrictRepoDeleteTransfer: &f}).AddTokenAuth(orgPolicyToken(t, "user4")), http.StatusForbidden)
	loginUser(t, "user4").MakeRequest(t, NewRequestWithValues(t, "POST", "/org/org3/settings", map[string]string{
		"name": "org3", "visibility": "0",
	}), http.StatusNotFound)
	assert.True(t, unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "org3"}).RestrictRepoDeleteTransfer)
}

// AC3: delete.
func TestOrgRepoPolicyDelete(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	setOrgRepoPolicies(t, true, user_model.PrivateForkAll)
	repo := orgPolicyRepo(t, "policy-delete", true)

	assertPolicyRefusedWeb(t, "user4", "/org3/policy-delete/settings", map[string]string{"action": "delete", "repo_name": "org3/policy-delete"}, orgPolicyDeleteLabel)
	assertPolicyRefusedAPI(t, NewRequest(t, "DELETE", "/api/v1/repos/org3/policy-delete").AddTokenAuth(orgPolicyToken(t, "user4")), "restrict_repo_delete_transfer")
	unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: repo.ID})

	// the owner and a site admin can
	MakeRequest(t, NewRequest(t, "DELETE", "/api/v1/repos/org3/policy-delete").AddTokenAuth(orgPolicyToken(t, "user2")), http.StatusNoContent)
	orgPolicyRepo(t, "policy-delete2", false)
	loginUser(t, "user1").MakeRequest(t, NewRequestWithValues(t, "POST", "/org3/policy-delete2/settings", map[string]string{"action": "delete", "repo_name": "org3/policy-delete2"}), http.StatusSeeOther)

	// the purge_trash cron (a doer with ID -1) still purges a trashed repository of a restricting organization
	require.NoError(t, trash_service.Purge(t.Context(), &user_model.User{ID: -1, Name: "(Cron)"}, repo.ID))
	unittest.AssertNotExistsBean(t, &repo_model.Repository{ID: repo.ID})

	// policy off: the repo admin can
	setOrgRepoPolicies(t, false, user_model.PrivateForkAll)
	orgPolicyRepo(t, "policy-delete3", false)
	MakeRequest(t, NewRequest(t, "DELETE", "/api/v1/repos/org3/policy-delete3").AddTokenAuth(orgPolicyToken(t, "user4")), http.StatusNoContent)
}

// AC4: transfer.
func TestOrgRepoPolicyTransfer(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	setOrgRepoPolicies(t, true, user_model.PrivateForkAll)
	repo := orgPolicyRepo(t, "policy-transfer", false)

	assertPolicyRefusedWeb(t, "user4", "/org3/policy-transfer/settings", map[string]string{"action": "transfer", "repo_name": "org3/policy-transfer", "new_owner_name": "user4"}, orgPolicyDeleteLabel)
	// a transfer to the doer itself is the immediate branch
	assertPolicyRefusedAPI(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/org3/policy-transfer/transfer", &api.TransferRepoOption{NewOwner: "user4"}).AddTokenAuth(orgPolicyToken(t, "user4")), "restrict_repo_delete_transfer")
	assert.Equal(t, "org3", unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: repo.ID}).OwnerName)

	// the owner starts a transfer to user4, which user4 accepts: accepting isn't checked again
	MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/org3/policy-transfer/transfer", &api.TransferRepoOption{NewOwner: "user4"}).AddTokenAuth(orgPolicyToken(t, "user2")), http.StatusCreated)
	MakeRequest(t, NewRequest(t, "POST", "/api/v1/repos/org3/policy-transfer/transfer/accept").AddTokenAuth(orgPolicyToken(t, "user4")), http.StatusAccepted)
	assert.Equal(t, "user4", unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: repo.ID}).OwnerName)

	// policy off: the repo admin moves it to its own account at once
	setOrgRepoPolicies(t, false, user_model.PrivateForkAll)
	repo = orgPolicyRepo(t, "policy-transfer2", false)
	MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/org3/policy-transfer2/transfer", &api.TransferRepoOption{NewOwner: "user4"}).AddTokenAuth(orgPolicyToken(t, "user4")), http.StatusAccepted)
	assert.Equal(t, "user4", unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: repo.ID}).OwnerName)
}

// AC5, AC9: visibility, both directions, and the disabled controls.
func TestOrgRepoPolicyVisibility(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	setOrgRepoPolicies(t, true, user_model.PrivateForkAll)
	private := orgPolicyRepo(t, "policy-private", true)
	public := orgPolicyRepo(t, "policy-public", false)
	token := orgPolicyToken(t, "user4")
	yes, no := true, false

	assertPolicyRefusedWeb(t, "user4", "/org3/policy-private/settings", map[string]string{"action": "update", "repo_name": "policy-private"}, orgPolicyVisibilityLabel)
	assertPolicyRefusedWeb(t, "user4", "/org3/policy-public/settings", map[string]string{"action": "update", "repo_name": "policy-public", "private": "on"}, orgPolicyVisibilityLabel)
	assertPolicyRefusedAPI(t, NewRequestWithJSON(t, "PATCH", "/api/v1/repos/org3/policy-private", &api.EditRepoOption{Private: &no}).AddTokenAuth(token), "restrict_repo_visibility_change")
	assertPolicyRefusedAPI(t, NewRequestWithJSON(t, "PATCH", "/api/v1/repos/org3/policy-public", &api.EditRepoOption{Private: &yes}).AddTokenAuth(token), "restrict_repo_visibility_change")
	assert.True(t, unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: private.ID}).IsPrivate)
	assert.False(t, unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: public.ID}).IsPrivate)

	// other settings still change when the visibility doesn't
	desc := "changed"
	MakeRequest(t, NewRequestWithJSON(t, "PATCH", "/api/v1/repos/org3/policy-private", &api.EditRepoOption{Description: &desc, Private: &yes}).AddTokenAuth(token), http.StatusOK)
	loginUser(t, "user4").MakeRequest(t, NewRequestWithValues(t, "POST", "/org3/policy-private/settings", map[string]string{"action": "update", "repo_name": "policy-private", "private": "true", "description": "web"}), http.StatusSeeOther)
	assert.Equal(t, "web", unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: private.ID}).Description)

	// the settings page disables the controls, with a note
	doc := NewHTMLParser(t, loginUser(t, "user4").MakeRequest(t, NewRequest(t, "GET", "/org3/policy-private/settings"), http.StatusOK).Body)
	_, disabled := doc.Find(`input[name="private"][type="checkbox"]`).Attr("disabled")
	assert.True(t, disabled)
	_, disabled = doc.Find(`button[data-modal="#delete-repo-modal"]`).Attr("disabled")
	assert.True(t, disabled)
	_, disabled = doc.Find(`button[data-modal="#transfer-repo-modal"]`).Attr("disabled")
	assert.True(t, disabled)
	assert.Equal(t, 3, doc.Find(".org-policy-note").Length())

	// the owner and a site admin see no restriction and can change it
	doc = NewHTMLParser(t, loginUser(t, "user2").MakeRequest(t, NewRequest(t, "GET", "/org3/policy-private/settings"), http.StatusOK).Body)
	assert.Equal(t, 0, doc.Find(".org-policy-note").Length())
	MakeRequest(t, NewRequestWithJSON(t, "PATCH", "/api/v1/repos/org3/policy-private", &api.EditRepoOption{Private: &no}).AddTokenAuth(orgPolicyToken(t, "user2")), http.StatusOK)
	MakeRequest(t, NewRequestWithJSON(t, "PATCH", "/api/v1/repos/org3/policy-private", &api.EditRepoOption{Private: &yes}).AddTokenAuth(orgPolicyToken(t, "user1")), http.StatusOK)

	// policy off: the repo admin can
	setOrgRepoPolicies(t, false, user_model.PrivateForkAll)
	MakeRequest(t, NewRequestWithJSON(t, "PATCH", "/api/v1/repos/org3/policy-private", &api.EditRepoOption{Private: &no}).AddTokenAuth(token), http.StatusOK)

	// AC7: a user-owned repository is never restricted, even with the flags set on the user's row
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	user2.RestrictRepoVisibilityChange = true
	require.NoError(t, user_model.UpdateUserCols(t.Context(), user2, "restrict_repo_visibility_change"))
	MakeRequest(t, NewRequestWithJSON(t, "PATCH", "/api/v1/repos/user2/repo2", &api.EditRepoOption{Private: &no}).AddTokenAuth(orgPolicyToken(t, "user2")), http.StatusOK)
}

// AC6: forks of private repositories.
func TestOrgRepoPolicyFork(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	private := orgPolicyRepo(t, "policy-fork", true)
	orgPolicyRepo(t, "policy-fork-public", false)
	token := orgPolicyToken(t, "user4")
	user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user4"})
	forkName := func(s string) *api.CreateForkOption { return &api.CreateForkOption{Name: &s} }
	forkInto := func(org, s string) *api.CreateForkOption { return &api.CreateForkOption{Organization: &org, Name: &s} }
	forkButton := func(user, repo string) bool {
		doc := NewHTMLParser(t, loginUser(t, user).MakeRequest(t, NewRequest(t, "GET", "/org3/"+repo), http.StatusOK).Body)
		return doc.Find(`a[href$="/org3/`+repo+`/fork"]`).Length() > 0
	}
	require.True(t, forkButton("user4", "policy-fork"))

	// only into this organization: not into a personal account, on the web or the API
	setOrgRepoPolicies(t, false, user_model.PrivateForkOrg)
	assertPolicyRefusedAPI(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/org3/policy-fork/forks", forkName("f1")).AddTokenAuth(token), "private_fork_policy")
	// the web can't fork into the owner, so no target is left: the page and the post say so, and the button is gone
	assert.False(t, forkButton("user4", "policy-fork"))
	resp := loginUser(t, "user4").MakeRequest(t, NewRequest(t, "GET", "/org3/policy-fork/fork"), http.StatusOK)
	assert.Contains(t, resp.Body.String(), "there are no valid owners")
	resp = loginUser(t, "user4").MakeRequest(t, NewRequestWithValues(t, "POST", "/org3/policy-fork/fork", map[string]string{"uid": strconv.FormatInt(user4.ID, 10), "repo_name": "f1"}), http.StatusOK)
	assert.Contains(t, resp.Body.String(), "there are no valid owners")
	unittest.AssertNotExistsBean(t, &repo_model.Repository{OwnerID: user4.ID, LowerName: "f1"})
	// the API forks into the organization under a new name
	MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/org3/policy-fork/forks", forkInto("org3", "policy-fork-in-org")).AddTokenAuth(orgPolicyToken(t, "user2")), http.StatusAccepted)
	unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{OwnerName: "org3", LowerName: "policy-fork-in-org", ForkID: private.ID})

	// none: owners included, site admins excepted; public repositories are never restricted
	setOrgRepoPolicies(t, false, user_model.PrivateForkNone)
	assertPolicyRefusedAPI(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/org3/policy-fork/forks", forkInto("org3", "f2")).AddTokenAuth(orgPolicyToken(t, "user2")), "private_fork_policy")
	assertPolicyRefusedAPI(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/org3/policy-fork/forks", forkName("f2")).AddTokenAuth(orgPolicyToken(t, "user2")), "private_fork_policy")
	MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/org3/policy-fork/forks", forkName("admin-fork")).AddTokenAuth(orgPolicyToken(t, "user1")), http.StatusAccepted)
	assert.True(t, forkButton("user4", "policy-fork-public"))
	MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/org3/policy-fork-public/forks", forkName("public-fork")).AddTokenAuth(token), http.StatusAccepted)

	// all: as today
	setOrgRepoPolicies(t, false, user_model.PrivateForkAll)
	testRepoFork(t, loginUser(t, "user4"), "org3", "policy-fork", "user4", "f3")
}

// The policies add no query to the repository page or its settings page while they allow the action.
func TestOrgRepoPolicyQueryCount(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	orgPolicyRepo(t, "policy-count", false)
	session := loginUser(t, "user4")
	count := func() (page, settings int64) {
		page = countQueries(t, func() { session.MakeRequest(t, NewRequest(t, "GET", "/org3/policy-count"), http.StatusOK) })
		settings = countQueries(t, func() { session.MakeRequest(t, NewRequest(t, "GET", "/org3/policy-count/settings"), http.StatusOK) })
		return page, settings
	}
	count() // warm up
	page, settings := count()
	// a public repository and policies that only bind private forks
	setOrgRepoPolicies(t, false, user_model.PrivateForkNone)
	page2, settings2 := count()
	assert.Equal(t, page, page2, "repository page")
	assert.Equal(t, settings, settings2, "settings page")
}
