// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"

	actions_model "forgejo.org/models/actions"
	asymkey_model "forgejo.org/models/asymkey"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	"forgejo.org/models/packages"
	"forgejo.org/models/perm"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	webhook_model "forgejo.org/models/webhook"
	"forgejo.org/modules/private"
	"forgejo.org/modules/structs"
	webhook_module "forgejo.org/modules/webhook"
	"forgejo.org/services/mailer/incoming"
	incoming_payload "forgejo.org/services/mailer/incoming/payload"
	webhook_service "forgejo.org/services/webhook"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AC3, one test per entry point beyond web/API/git-HTTP (see trash_test.go).

// SSH: the internal serv endpoint refuses a trashed repository (ssh itself isn't available in containers).
func TestTrashSSHServ(t *testing.T) {
	defer prepareTrash(t)()
	moveToTrash(t)

	key := unittest.AssertExistsAndLoadBean(t, &asymkey_model.PublicKey{ID: 1})
	_, extra := private.ServCommand(t.Context(), key.ID, "user2", "repo1", perm.AccessModeRead, "127.0.0.1", "git-upload-pack")
	assert.Equal(t, http.StatusNotFound, extra.StatusCode)
}

// LFS: a batch download for a trashed repository is refused.
func TestTrashLFS(t *testing.T) {
	defer prepareTrash(t)()
	moveToTrash(t)

	req := NewRequestWithJSON(t, "POST", "/user2/repo1.git/info/lfs/objects/batch", map[string]any{
		"operation": "download",
		"objects":   []map[string]any{{"oid": "0000000000000000000000000000000000000000000000000000000000000000", "size": 1}},
	}).SetHeader("Accept", "application/vnd.git-lfs+json").AddBasicAuth("user2")
	MakeRequest(t, req, http.StatusNotFound)
}

// Packages: a package linked to a trashed repository is neither listed nor downloadable, while the
// owner's other packages keep working.
func TestTrashPackages(t *testing.T) {
	defer prepareTrash(t)()
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

	upload := func(name string) {
		url := fmt.Sprintf("/api/packages/%s/generic/%s/1.0.0/file.bin", user.Name, name)
		MakeRequest(t, NewRequestWithBody(t, "PUT", url, bytes.NewReader([]byte{1, 2, 3})).AddBasicAuth(user.Name), http.StatusCreated)
	}
	upload("linked")
	upload("unlinked")
	pkg, err := packages.GetPackageByName(t.Context(), user.ID, packages.TypeGeneric, "linked")
	require.NoError(t, err)
	require.NoError(t, packages.SetRepositoryLink(t.Context(), pkg.ID, trashRepoID))

	moveToTrash(t)

	MakeRequest(t, NewRequest(t, "GET", fmt.Sprintf("/api/packages/%s/generic/linked/1.0.0/file.bin", user.Name)).AddBasicAuth(user.Name), http.StatusNotFound)
	MakeRequest(t, NewRequest(t, "GET", fmt.Sprintf("/api/packages/%s/generic/unlinked/1.0.0/file.bin", user.Name)).AddBasicAuth(user.Name), http.StatusOK)

	token := getUserToken(t, "user2", auth_model.AccessTokenScopeAll)
	var list []*structs.Package
	DecodeJSON(t, MakeRequest(t, NewRequest(t, "GET", "/api/v1/packages/user2").AddTokenAuth(token), http.StatusOK), &list)
	for _, p := range list {
		assert.NotEqual(t, "linked", p.Name)
	}
}

// Webhooks: no delivery is queued for a trashed repository.
func TestTrashWebhooks(t *testing.T) {
	defer prepareTrash(t)()
	hook := &webhook_model.Webhook{RepoID: trashRepoID, URL: "http://127.0.0.1:1/hook", HTTPMethod: "POST", ContentType: webhook_model.ContentTypeJSON, IsActive: true, Type: webhook_module.GITEA}
	hook.HookEvent = &webhook_module.HookEvent{PushOnly: true}
	require.NoError(t, webhook_model.CreateWebhook(t.Context(), hook, ""))

	moveToTrash(t)

	repo := trashRepo(t)
	before := unittest.GetCount(t, &webhook_model.HookTask{})
	require.NoError(t, webhook_service.PrepareWebhooks(t.Context(), webhook_service.EventSource{Repository: repo}, webhook_module.HookEventPush, &structs.PushPayload{}))
	assert.Equal(t, before, unittest.GetCount(t, &webhook_model.HookTask{}))
}

// Mirrors: a trashed mirror is not synced.
func TestTrashMirrors(t *testing.T) {
	defer prepareTrash(t)()
	require.NoError(t, db.Insert(t.Context(), &repo_model.Mirror{RepoID: trashRepoID, Interval: 3600}))

	moveToTrash(t)

	require.NoError(t, repo_model.MirrorsIterate(t.Context(), 0, func(_ int, bean any) error {
		assert.NotEqual(t, int64(trashRepoID), bean.(*repo_model.Mirror).RepoID)
		return nil
	}))
}

// Actions: a waiting job of a trashed repository is cancelled, and a runner never picks it up.
func TestTrashActions(t *testing.T) {
	defer prepareTrash(t)()
	run := &actions_model.ActionRun{RepoID: trashRepoID, OwnerID: 2, Index: 9001, Status: actions_model.StatusWaiting, WorkflowID: "t.yml", Ref: "refs/heads/master", CommitSHA: "0000000000000000000000000000000000000000", TriggerUserID: 2}
	require.NoError(t, db.Insert(t.Context(), run))
	job := &actions_model.ActionRunJob{RunID: run.ID, RepoID: trashRepoID, OwnerID: 2, Handle: "trash-job", Status: actions_model.StatusWaiting, RunsOn: []string{"docker"}, JobID: "j", Name: "j"}
	require.NoError(t, db.Insert(t.Context(), job))

	moveToTrash(t)

	job = unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: job.ID})
	assert.Equal(t, actions_model.StatusCancelled, job.Status)
}

// Attachments: issue and release attachments of a trashed repository are 404.
func TestTrashAttachments(t *testing.T) {
	defer prepareTrash(t)()
	att := unittest.AssertExistsAndLoadBean(t, &repo_model.Attachment{ID: 1})
	require.Equal(t, int64(trashRepoID), att.RepoID)

	moveToTrash(t)

	MakeRequest(t, NewRequest(t, "GET", "/attachments/"+att.UUID), http.StatusNotFound)
	loginUser(t, "user2").MakeRequest(t, NewRequest(t, "GET", "/attachments/"+att.UUID), http.StatusNotFound)
}

// F2: an issue that a trashed repository referenced still renders.
// F3: owner-level project boards and subscriptions don't show a trashed repository's issues.
// F5: an LFS object of a trashed repository can't be linked into another repository.
func TestTrashReferencesAndLists(t *testing.T) {
	defer prepareTrash(t)()
	moveToTrash(t)

	// F2: an issue of repo2 that repo1 references still renders, on the web and in the API
	require.NoError(t, db.Insert(t.Context(), &issues_model.Comment{Type: issues_model.CommentTypeIssueRef, IssueID: 4, PosterID: 2, RefRepoID: trashRepoID, RefIssueID: 1}))
	loginUser(t, "user2").MakeRequest(t, NewRequest(t, "GET", "/user2/repo2/issues/1"), http.StatusOK)
	MakeRequest(t, NewRequest(t, "GET", "/api/v1/repos/user2/repo2/issues/1/comments").AddTokenAuth(getUserToken(t, "user2", auth_model.AccessTokenScopeAll)), http.StatusOK)

	body := loginUser(t, "user2").MakeRequest(t, NewRequest(t, "GET", "/notifications/subscriptions"), http.StatusOK).Body.String()
	assert.NotContains(t, body, `/user2/repo1"`)
	body = loginUser(t, "user2").MakeRequest(t, NewRequest(t, "GET", "/issues"), http.StatusOK).Body.String()
	assert.NotContains(t, body, `/user2/repo1"`)

	repos, err := repo_model.GetRepositoriesMapByIDs(t.Context(), []int64{trashRepoID})
	require.NoError(t, err)
	assert.Empty(t, repos)
}

// Forks: a fork of a trashed repository keeps working and doesn't 500 on its missing base.
func TestTrashFork(t *testing.T) {
	defer prepareTrash(t)()
	fork := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 11})
	require.Equal(t, int64(10), fork.ForkID)
	session := loginUser(t, "user13")
	pages := []string{"/user13/repo11", "/user13/repo11/pulls", "/api/v1/repos/user13/repo11", "/api/v1/repos/user13/repo11/pulls"}

	before := map[string]int{}
	for _, page := range pages {
		before[page] = session.MakeRequest(t, NewRequest(t, "GET", page), NoExpectedStatus).Code
	}
	assert.Equal(t, http.StatusOK, before["/user13/repo11"])

	// trash the base (repo 10 of user12)
	MakeRequest(t, NewRequest(t, "DELETE", "/api/v1/repos/user12/repo10").AddTokenAuth(getUserToken(t, "user12", auth_model.AccessTokenScopeAll)), http.StatusNoContent)
	require.True(t, unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 10}).IsTrashed())

	for _, page := range pages {
		assert.Equal(t, before[page], session.MakeRequest(t, NewRequest(t, "GET", page), NoExpectedStatus).Code, page)
	}
}

// Issue search (indexer) does not return the issues of a trashed repository.
func TestTrashIssueSearch(t *testing.T) {
	defer prepareTrash(t)()
	token := getUserToken(t, "user2", auth_model.AccessTokenScopeAll)
	search := func() []int64 {
		var issues []*structs.Issue
		DecodeJSON(t, MakeRequest(t, NewRequest(t, "GET", "/api/v1/repos/issues/search?q=issue1&type=issues").AddTokenAuth(token), http.StatusOK), &issues)
		ids := make([]int64, 0, len(issues))
		for _, i := range issues {
			ids = append(ids, i.Repo.ID)
		}
		return ids
	}
	require.Contains(t, search(), int64(trashRepoID))

	moveToTrash(t)
	assert.NotContains(t, search(), int64(trashRepoID))
}

// An email reply to an issue of a trashed repository is refused and creates no comment.
func TestTrashEmailReply(t *testing.T) {
	defer prepareTrash(t)()
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 1})
	payload, err := incoming_payload.CreateReferencePayload(issue)
	require.NoError(t, err)

	moveToTrash(t)
	before := unittest.GetCount(t, &issues_model.Comment{IssueID: issue.ID})
	require.Error(t, (&incoming.ReplyHandler{}).Handle(t.Context(), &incoming.MailContent{Content: "reply"}, user, payload))
	assert.Equal(t, before, unittest.GetCount(t, &issues_model.Comment{IssueID: issue.ID}))
}
