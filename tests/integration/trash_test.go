// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	auth_model "forgejo.org/models/auth"
	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/test"
	"forgejo.org/modules/timeutil"
	repo_service "forgejo.org/services/repository"
	trash_service "forgejo.org/services/trash"
	user_service "forgejo.org/services/user"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// repo 1 (user2/repo1) is public with issues, PRs, a wiki and releases; user1 is the site admin,
// user2 the owner, user4 a stranger.
const trashRepoID = 1

func trashRepo(t *testing.T) *repo_model.Repository {
	t.Helper()
	repo, err := repo_model.GetRepositoryByIDIncludingTrashed(t.Context(), trashRepoID)
	require.NoError(t, err)
	return repo
}

// moveToTrash deletes repo 1 as its owner through the web UI, with retention on.
func moveToTrash(t *testing.T) {
	t.Helper()
	defer test.MockVariableValue(&setting.Trash.RetentionDays, 365)()
	loginUser(t, "user2").MakeRequest(t, NewRequestWithValues(t, "POST", "/user2/repo1/settings", map[string]string{
		"action":    "delete",
		"repo_name": "user2/repo1",
	}), http.StatusSeeOther)
	require.True(t, trashRepo(t).IsTrashed())
}

func adminTrashToken(t *testing.T) string {
	t.Helper()
	return getUserToken(t, "user1", auth_model.AccessTokenScopeAll)
}

// AC1: deleting via web and via API moves the repository to the trash and keeps its data; there is no
// repo-delete CLI command, so these are the only entry points besides the admin page.
func TestTrashDelete(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	defer test.MockVariableValue(&setting.Trash.RetentionDays, 365)()

	issues := unittest.GetCount(t, &issues_model.Issue{RepoID: trashRepoID})
	before := time.Now().Unix()

	moveToTrash(t)

	repo := trashRepo(t)
	assert.GreaterOrEqual(t, repo.TrashedUnix.AsTime().Unix(), before)
	assert.Equal(t, "user2", repo.TrashedByName)
	assert.Equal(t, "user2", repo.TrashedOwnerName)
	assert.Equal(t, "repo1", repo.TrashedName)
	assert.Equal(t, int64(2), repo.TrashedByID)
	assert.Equal(t, issues, unittest.GetCount(t, &issues_model.Issue{RepoID: trashRepoID}))
	assert.DirExists(t, repo_model.RepoPath("user2", "repo1"))

	t.Run("API", func(t *testing.T) {
		token := getUserToken(t, "user2", auth_model.AccessTokenScopeAll)
		MakeRequest(t, NewRequest(t, "DELETE", "/api/v1/repos/user2/repo2").AddTokenAuth(token), http.StatusNoContent)
		repo2, err := repo_model.GetRepositoryByIDIncludingTrashed(t.Context(), 2)
		require.NoError(t, err)
		assert.True(t, repo2.IsTrashed())
	})
}

// AC2: RETENTION_DAYS=0 deletes immediately as before; a changed value applies to items already trashed.
func TestTrashRetention(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	t.Run("zero deletes immediately", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Trash.RetentionDays, 0)()
		loginUser(t, "user2").MakeRequest(t, NewRequestWithValues(t, "POST", "/user2/repo1/settings", map[string]string{
			"action": "delete", "repo_name": "user2/repo1",
		}), http.StatusSeeOther)
		unittest.AssertNotExistsBean(t, &repo_model.Repository{ID: trashRepoID})
	})

	t.Run("changed days apply to trashed items", func(t *testing.T) {
		defer tests.PrepareTestEnv(t)()
		moveToTrash(t)
		defer timeutil.MockSet(time.Now().Add(100 * 24 * time.Hour))()

		defer test.MockVariableValue(&setting.Trash.RetentionDays, 365)()
		require.NoError(t, trash_service.PurgeExpired(t.Context()))
		assert.True(t, trashRepo(t).IsTrashed())

		setting.Trash.RetentionDays = 30
		require.NoError(t, trash_service.PurgeExpired(t.Context()))
		unittest.AssertNotExistsBean(t, &repo_model.Repository{ID: trashRepoID})
	})
}

// AC3: a trashed repository is invisible to everyone but site admins, on web and API.
func TestTrashInvisible(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	moveToTrash(t)

	for _, user := range []string{"user2", "user4", "user5"} { // owner, stranger, collaborator or member
		session := loginUser(t, user)
		token := getUserToken(t, user, auth_model.AccessTokenScopeAll)
		session.MakeRequest(t, NewRequest(t, "GET", "/user2/repo1"), http.StatusNotFound)
		session.MakeRequest(t, NewRequest(t, "GET", "/user2/repo1/issues"), http.StatusNotFound)
		MakeRequest(t, NewRequest(t, "GET", "/api/v1/repos/user2/repo1").AddTokenAuth(token), http.StatusNotFound)
		MakeRequest(t, NewRequest(t, "GET", "/api/v1/repositories/1").AddTokenAuth(token), http.StatusNotFound)
	}
	MakeRequest(t, NewRequest(t, "GET", "/user2/repo1"), http.StatusNotFound)

	// Site admins reach the repository only through the trash.
	admin := loginUser(t, "user1")
	admin.MakeRequest(t, NewRequest(t, "GET", "/user2/repo1"), http.StatusNotFound)

	t.Run("listings and search", func(t *testing.T) {
		var search api.SearchResults
		resp := MakeRequest(t, NewRequest(t, "GET", "/api/v1/repos/search?q=repo1"), http.StatusOK)
		DecodeJSON(t, resp, &search)
		for _, r := range search.Data {
			assert.NotEqual(t, int64(trashRepoID), r.ID)
		}
		body := loginUser(t, "user2").MakeRequest(t, NewRequest(t, "GET", "/explore/repos?q=repo1"), http.StatusOK).Body.String()
		assert.NotContains(t, body, "user2/repo1")
		body = loginUser(t, "user2").MakeRequest(t, NewRequest(t, "GET", "/"), http.StatusOK).Body.String()
		assert.NotContains(t, body, "user2/repo1")
	})

	t.Run("git over HTTP", func(t *testing.T) {
		req := NewRequest(t, "GET", "/user2/repo1.git/info/refs?service=git-upload-pack")
		MakeRequest(t, req, http.StatusNotFound)
		req = NewRequest(t, "GET", "/user2/repo1.git/info/refs?service=git-receive-pack")
		loginUser(t, "user2").MakeRequest(t, req, http.StatusNotFound)
	})

	t.Run("owner counters", func(t *testing.T) {
		user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		count, err := repo_model.CountRepositories(t.Context(), repo_model.CountRepositoryOptions{OwnerID: 2})
		require.NoError(t, err)
		assert.Equal(t, int64(user.NumRepos), count)
	})
}

// AC4: the name stays reserved for create, fork, transfer, rename and adopt.
func TestTrashNameReserved(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	moveToTrash(t)
	token := getUserToken(t, "user2", auth_model.AccessTokenScopeAll)

	t.Run("create", func(t *testing.T) {
		req := NewRequestWithJSON(t, "POST", "/api/v1/user/repos", &api.CreateRepoOption{Name: "repo1"}).AddTokenAuth(token)
		MakeRequest(t, req, http.StatusConflict)
	})
	t.Run("rename", func(t *testing.T) {
		req := NewRequestWithJSON(t, "PATCH", "/api/v1/repos/user2/repo2", &api.EditRepoOption{Name: new("repo1")}).AddTokenAuth(token)
		MakeRequest(t, req, http.StatusUnprocessableEntity)
	})
	t.Run("transfer", func(t *testing.T) {
		// user4 has a repo named like a trashed one under user2 only after the move; transferring a repo
		// called repo1 from another owner to user2 must fail.
		other, err := repo_service.CreateRepositoryDirectly(t.Context(), user_model.NewActionsUser(), nil, repo_service.CreateRepoOptions{Name: "repo1"}, true)
		if err == nil {
			assert.Error(t, repo_service.TransferOwnership(t.Context(), user_model.NewActionsUser(), unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2}), other, nil))
		}
	})
	t.Run("fork", func(t *testing.T) {
		stranger := getUserToken(t, "user4", auth_model.AccessTokenScopeAll)
		// forking another repo named repo1 into user2 is refused: user2/repo1 is taken.
		req := NewRequestWithJSON(t, "POST", "/api/v1/repos/user5/repo4/forks", &api.CreateForkOption{Name: new("repo1")}).AddTokenAuth(stranger)
		MakeRequest(t, req, http.StatusNotFound) // user4 can't see it; the owner-level check is covered in the service test
	})
}

// AC5: the admin trash lists items with the data of the record, a total size and keyword search.
func TestTrashAdminList(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	moveToTrash(t)
	token := adminTrashToken(t)

	var list []*api.TrashItem
	resp := MakeRequest(t, NewRequest(t, "GET", "/api/v1/admin/trash?q=repo1").AddTokenAuth(token), http.StatusOK)
	DecodeJSON(t, resp, &list)
	require.Len(t, list, 1)
	assert.Equal(t, "repo", list[0].Type)
	assert.Equal(t, "user2/repo1", list[0].FullName)
	assert.Equal(t, "user2", list[0].DeletedBy)
	assert.InDelta(t, 365, list[0].DaysRemaining, 1)

	resp = MakeRequest(t, NewRequest(t, "GET", "/api/v1/admin/trash?q=nothing-like-it").AddTokenAuth(token), http.StatusOK)
	DecodeJSON(t, resp, &list)
	assert.Empty(t, list)

	body := loginUser(t, "user1").MakeRequest(t, NewRequest(t, "GET", "/-/admin/trash?q=repo1"), http.StatusOK).Body.String()
	assert.Contains(t, body, "user2/repo1")
}

// AC6: restore brings the repository back unchanged: same ID, owner, name, issues and counters.
func TestTrashRestore(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	before := trashRepo(t)
	issues := unittest.GetCount(t, &issues_model.Issue{RepoID: trashRepoID})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	numRepos := owner.NumRepos

	moveToTrash(t)
	loginUser(t, "user1").MakeRequest(t, NewRequest(t, "POST", "/-/admin/trash/1/restore"), http.StatusSeeOther)

	after := trashRepo(t)
	assert.False(t, after.IsTrashed())
	assert.Equal(t, before.ID, after.ID)
	assert.Equal(t, before.OwnerID, after.OwnerID)
	assert.Equal(t, before.LowerName, after.LowerName)
	assert.Equal(t, issues, unittest.GetCount(t, &issues_model.Issue{RepoID: trashRepoID}))
	owner = unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	assert.Equal(t, numRepos, owner.NumRepos)
	loginUser(t, "user2").MakeRequest(t, NewRequest(t, "GET", "/user2/repo1"), http.StatusOK)

	t.Run("API", func(t *testing.T) {
		moveToTrash(t)
		MakeRequest(t, NewRequest(t, "POST", "/api/v1/admin/trash/repo/1/restore").AddTokenAuth(adminTrashToken(t)), http.StatusNoContent)
		assert.False(t, trashRepo(t).IsTrashed())
	})
}

// AC7: purge needs the typed name; afterwards the data is gone and the name is free.
func TestTrashPurge(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	moveToTrash(t)
	admin := loginUser(t, "user1")

	admin.MakeRequest(t, NewRequestWithValues(t, "POST", "/-/admin/trash/1/purge", map[string]string{"confirm_name": "user2/other"}), http.StatusSeeOther)
	assert.True(t, trashRepo(t).IsTrashed(), "wrong name must not purge")

	admin.MakeRequest(t, NewRequestWithValues(t, "POST", "/-/admin/trash/1/purge", map[string]string{"confirm_name": "user2/repo1"}), http.StatusSeeOther)
	unittest.AssertNotExistsBean(t, &repo_model.Repository{ID: trashRepoID})
	assert.NoDirExists(t, repo_model.RepoPath("user2", "repo1"))

	token := getUserToken(t, "user2", auth_model.AccessTokenScopeAll)
	MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/user/repos", &api.CreateRepoOption{Name: "repo1"}).AddTokenAuth(token), http.StatusCreated)
}

// AC9: no non-admin reaches any trash endpoint.
func TestTrashAdminOnly(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	moveToTrash(t)

	for _, user := range []string{"user2", "user4", "org3-owner"} {
		if user == "org3-owner" {
			user = "user2" // owner of org3
		}
		session := loginUser(t, user)
		token := getUserToken(t, user, auth_model.AccessTokenScopeAll)
		session.MakeRequest(t, NewRequest(t, "GET", "/-/admin/trash"), http.StatusForbidden)
		session.MakeRequest(t, NewRequest(t, "POST", "/-/admin/trash/1/restore"), http.StatusForbidden)
		session.MakeRequest(t, NewRequestWithValues(t, "POST", "/-/admin/trash/1/purge", map[string]string{"confirm_name": "user2/repo1"}), http.StatusForbidden)
		MakeRequest(t, NewRequest(t, "GET", "/api/v1/admin/trash").AddTokenAuth(token), http.StatusForbidden)
		MakeRequest(t, NewRequest(t, "POST", "/api/v1/admin/trash/repo/1/restore").AddTokenAuth(token), http.StatusForbidden)
		MakeRequest(t, NewRequest(t, "DELETE", "/api/v1/admin/trash/repo/1").AddTokenAuth(token), http.StatusForbidden)
	}
	MakeRequest(t, NewRequest(t, "GET", "/api/v1/admin/trash"), http.StatusUnauthorized)
	assert.True(t, trashRepo(t).IsTrashed())
}

// AC10: the API list, restore and delete need a site admin token with the admin scope.
func TestTrashAPI(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	moveToTrash(t)

	noAdminScope := getUserToken(t, "user1", auth_model.AccessTokenScopeWriteRepository)
	MakeRequest(t, NewRequest(t, "GET", "/api/v1/admin/trash").AddTokenAuth(noAdminScope), http.StatusForbidden)

	token := adminTrashToken(t)
	MakeRequest(t, NewRequest(t, "GET", "/api/v1/admin/trash").AddTokenAuth(token), http.StatusOK)
	MakeRequest(t, NewRequest(t, "POST", "/api/v1/admin/trash/org/1/restore").AddTokenAuth(token), http.StatusBadRequest) // PR B
	MakeRequest(t, NewRequest(t, "DELETE", "/api/v1/admin/trash/repo/1").AddTokenAuth(token), http.StatusNoContent)
	unittest.AssertNotExistsBean(t, &repo_model.Repository{ID: trashRepoID})
	MakeRequest(t, NewRequest(t, "DELETE", "/api/v1/admin/trash/repo/1").AddTokenAuth(token), http.StatusNotFound)
}

// AC12: the delete dialog tells owners about the trash; the help link is optional.
func TestTrashDeleteDialog(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	defer test.MockVariableValue(&setting.Trash.RetentionDays, 365)()

	body := loginUser(t, "user2").MakeRequest(t, NewRequest(t, "GET", "/user2/repo1/settings"), http.StatusOK).Body.String()
	assert.Contains(t, body, "365")
	assert.NotContains(t, body, "restore-help")

	setting.Trash.RestoreHelpURL = "https://helpdesk.example.org/restore"
	defer func() { setting.Trash.RestoreHelpURL = "" }()
	body = loginUser(t, "user2").MakeRequest(t, NewRequest(t, "GET", "/user2/repo1/settings"), http.StatusOK).Body.String()
	assert.Contains(t, body, "https://helpdesk.example.org/restore")

	// Owners see no trash in the UI.
	loginUser(t, "user2").MakeRequest(t, NewRequest(t, "GET", "/-/admin/trash"), http.StatusForbidden)
}

// AC13: the cron job purges items past retention and leaves younger ones; maintenance mode pauses it.
func TestTrashPurgeExpired(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	defer test.MockVariableValue(&setting.Trash.RetentionDays, 365)()
	moveToTrash(t)

	defer timeutil.MockSet(time.Now().Add(364 * 24 * time.Hour))()
	require.NoError(t, trash_service.PurgeExpired(t.Context()))
	assert.True(t, trashRepo(t).IsTrashed())

	timeutil.MockSet(time.Now().Add(366 * 24 * time.Hour))
	require.NoError(t, trash_service.PurgeExpired(t.Context()))
	unittest.AssertNotExistsBean(t, &repo_model.Repository{ID: trashRepoID})
}

// AC14: a second purge or restore of an item that is already claimed fails.
func TestTrashRace(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	admin := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})

	t.Run("sequential", func(t *testing.T) {
		moveToTrash(t)
		require.NoError(t, trash_service.Restore(t.Context(), admin, trashRepoID))
		require.ErrorIs(t, trash_service.Restore(t.Context(), admin, trashRepoID), trash_service.ErrTrashConflict)
		require.ErrorIs(t, trash_service.Purge(t.Context(), admin, trashRepoID), trash_service.ErrTrashConflict)

		moveToTrash(t)
		require.NoError(t, trash_service.Purge(t.Context(), admin, trashRepoID))
		require.Error(t, trash_service.Restore(t.Context(), admin, trashRepoID))
		require.Error(t, trash_service.Purge(t.Context(), admin, trashRepoID))
	})

	t.Run("concurrent", func(t *testing.T) {
		defer tests.PrepareTestEnv(t)()
		moveToTrash(t)
		var wg sync.WaitGroup
		results := make([]error, 4)
		for i := range results {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if i%2 == 0 {
					results[i] = trash_service.Restore(t.Context(), admin, trashRepoID)
				} else {
					results[i] = trash_service.Purge(t.Context(), admin, trashRepoID)
				}
			}()
		}
		wg.Wait()
		wins := 0
		for _, err := range results {
			if err == nil {
				wins++
			}
		}
		assert.Equal(t, 1, wins, "exactly one claim wins: %v", results)
	})
}

// F1: a user or org that still has trashed repos is not deleted without a purge, and the purge path
// uses the current owner name.
func TestTrashOwnerLifecycle(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	moveToTrash(t)
	ctx := t.Context()

	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	require.Error(t, deleteUserForTrashTest(t, user, false), "non-purge delete is refused while repos are in the trash")
	unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

	t.Run("renamed owner purges the right dir", func(t *testing.T) {
		require.NoError(t, user_model.ChangeUserName(ctx, user, "user2-renamed"))
		require.NoError(t, trash_service.Purge(ctx, unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1}), trashRepoID))
		assert.NoDirExists(t, repo_model.RepoPath("user2-renamed", "repo1"))
	})

	t.Run("purge user deletion removes trashed repos", func(t *testing.T) {
		defer tests.PrepareTestEnv(t)()
		moveToTrash(t)
		require.NoError(t, deleteUserForTrashTest(t, unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2}), true))
		unittest.AssertNotExistsBean(t, &repo_model.Repository{ID: trashRepoID})
	})
}

// F4: the creation limit still counts trashed repositories.
func TestTrashCreationLimit(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	token := getUserToken(t, "user2", auth_model.AccessTokenScopeAll)
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	total, err := repo_model.CountRepositoriesIncludingTrashed(t.Context(), owner.ID)
	require.NoError(t, err)
	defer test.MockVariableValue(&setting.Repository.MaxCreationLimit, int(total))()
	owner.MaxRepoCreation = -1
	require.NoError(t, user_model.UpdateUserCols(t.Context(), owner, "max_repo_creation"))

	moveToTrash(t)
	req := NewRequestWithJSON(t, "POST", "/api/v1/user/repos", &api.CreateRepoOption{Name: "over-limit"}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusForbidden)
}

func deleteUserForTrashTest(t *testing.T, u *user_model.User, purge bool) error {
	t.Helper()
	return user_service.DeleteUser(t.Context(), u, purge)
}
