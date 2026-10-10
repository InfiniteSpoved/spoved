// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	actions_model "forgejo.org/models/actions"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	"forgejo.org/models/perm"
	unit_model "forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/json"
	repo_module "forgejo.org/modules/repository"
	"forgejo.org/modules/setting"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/test"
	"forgejo.org/modules/timeutil"
	actions_service "forgejo.org/services/actions"
	repo_service "forgejo.org/services/repository"
	"forgejo.org/tests/forgery"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const scheduledDispatchWorkflow = "name: d\non:\n  workflow_dispatch:\n    inputs:\n      who:\n        required: true\n        default: ''\n      extra:\n        default: x\njobs:\n  t:\n    runs-on: docker\n    steps:\n      - run: echo t\n"

func TestActionsScheduledDispatch(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		defer timeutil.MockUnset()
		defer test.MockVariableValue(&setting.Actions.MaxDelayedRunDays, 30)()

		owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		writer := forgery.CreateUser(t, nil)
		reader := forgery.CreateUser(t, nil)
		repoAdmin := forgery.CreateUser(t, nil)
		files := forgery.MapFS{
			".forgejo/workflows/dispatch.yml": forgery.MapFile(scheduledDispatchWorkflow),
			".forgejo/workflows/push.yml":     forgery.MapFile("name: p\non: [push]\njobs:\n  t:\n    runs-on: docker\n    steps:\n      - run: echo t\n"),
		}
		repo := forgery.CreateRepository(t, owner, &forgery.CreateRepositoryOptions{
			Files:     files,
			IsPrivate: true,
			Collaborators: map[*user_model.User]perm.AccessMode{
				writer: perm.AccessModeWrite, reader: perm.AccessModeRead, repoAdmin: perm.AccessModeAdmin,
			},
		})
		forgery.EnableRepoUnit(t, repo, unit_model.TypeActions, nil)
		other := forgery.CreateRepository(t, owner, &forgery.CreateRepositoryOptions{Files: files})
		forgery.EnableRepoUnit(t, other, unit_model.TypeActions, nil)

		ownerToken := getUserToken(t, owner.LowerName, auth_model.AccessTokenScopeWriteRepository)
		writerToken := getUserToken(t, writer.LowerName, auth_model.AccessTokenScopeWriteRepository)
		readerToken := getUserToken(t, reader.LowerName, auth_model.AccessTokenScopeWriteRepository)
		adminToken := getUserToken(t, repoAdmin.LowerName, auth_model.AccessTokenScopeWriteRepository)
		siteAdminToken := getUserToken(t, "user1", auth_model.AccessTokenScopeWriteRepository)

		base := fmt.Sprintf("/api/v1/repos/%s/%s/actions", repo.OwnerName, repo.Name)
		countRuns := func() int {
			return unittest.GetCount(t, &actions_model.ActionRun{RepoID: repo.ID, TriggerEvent: "workflow_dispatch"})
		} // the push of the files starts a run too
		schedule := func(token, workflow string, runAt time.Time, inputs map[string]string, status int) *api.ScheduledWorkflowRun {
			resp := MakeRequest(t, NewRequestWithJSON(t, http.MethodPost, base+"/workflows/"+workflow+"/dispatches",
				&api.DispatchWorkflowOption{Ref: repo.DefaultBranch, Inputs: inputs, RunAt: &runAt, ReturnRunInfo: true},
			).AddTokenAuth(token), status)
			if status != http.StatusCreated {
				return nil
			}
			res := new(api.ScheduledWorkflowRun)
			DecodeJSON(t, resp, res)
			return res
		}
		list := func(token string, status int) []*api.ScheduledWorkflowRun {
			resp := MakeRequest(t, NewRequest(t, http.MethodGet, base+"/scheduled-runs").AddTokenAuth(token), status)
			if status != http.StatusOK {
				return nil
			}
			var res []*api.ScheduledWorkflowRun
			DecodeJSON(t, resp, &res)
			return res
		}
		del := func(token string, id int64, status int) {
			MakeRequest(t, NewRequest(t, http.MethodDelete, fmt.Sprintf("%s/scheduled-runs/%d", base, id)).AddTokenAuth(token), status)
		}
		in := map[string]string{"who": "me"}

		t.Run("refused", func(t *testing.T) {
			now := time.Now()
			schedule(ownerToken, "dispatch.yml", now.Add(-time.Minute), in, http.StatusBadRequest)
			schedule(ownerToken, "dispatch.yml", now.AddDate(0, 0, 31), in, http.StatusBadRequest)
			schedule(ownerToken, "dispatch.yml", now.Add(time.Hour), map[string]string{"extra": "y"}, http.StatusBadRequest) // required input
			schedule(ownerToken, "push.yml", now.Add(time.Hour), nil, http.StatusBadRequest)                                 // no trigger
			func() {
				defer test.MockVariableValue(&setting.Actions.MaxDelayedRunDays, 0)()
				schedule(ownerToken, "dispatch.yml", now.Add(time.Hour), in, http.StatusBadRequest)
			}()
			schedule(readerToken, "dispatch.yml", now.Add(time.Hour), in, http.StatusForbidden)
			MakeRequest(t, NewRequestWithJSON(t, http.MethodPost, base+"/workflows/dispatch.yml/dispatches",
				&api.DispatchWorkflowOption{Ref: repo.DefaultBranch, Inputs: in, RunAt: &now}), http.StatusNotFound) // private repository
			assert.Empty(t, list(ownerToken, http.StatusOK))
			assert.Zero(t, countRuns())
		})

		t.Run("access", func(t *testing.T) {
			sd := schedule(writerToken, "dispatch.yml", time.Now().Add(time.Hour), in, http.StatusCreated)
			assert.Equal(t, writer.Name, sd.Doer)
			assert.Equal(t, "pending", sd.Status)
			assert.Equal(t, "dispatch.yml", sd.Workflow)
			assert.Equal(t, "refs/heads/"+repo.DefaultBranch, sd.Ref)

			list(readerToken, http.StatusForbidden)
			MakeRequest(t, NewRequest(t, http.MethodGet, base+"/scheduled-runs"), http.StatusNotFound) // private repository
			del(readerToken, sd.ID, http.StatusForbidden)
			// ids belong to their repository
			MakeRequest(t, NewRequest(t, http.MethodDelete, fmt.Sprintf("/api/v1/repos/%s/%s/actions/scheduled-runs/%d", other.OwnerName, other.Name, sd.ID)).AddTokenAuth(ownerToken), http.StatusNotFound)
			del(ownerToken, 999999, http.StatusNotFound)
			require.Len(t, list(ownerToken, http.StatusOK), 1)

			// a token restricted to another repository, or to public ones, gets nothing
			restricted := forgery.CreateAccessToken(t, owner, &forgery.CreateAccessTokenOptions{Scope: "write:repository"})
			restricted.ResourceAllRepos = false
			_, err := db.GetEngine(t.Context()).ID(restricted.ID).Cols("resource_all_repos").Update(restricted)
			require.NoError(t, err)
			resource := &auth_model.AccessTokenResourceRepo{TokenID: restricted.ID, RepoID: other.ID}
			require.NoError(t, db.Insert(t.Context(), resource))
			t.Cleanup(func() { unittest.AssertSuccessfulDelete(t, resource) }) // runs before the token is deleted
			list(restricted.Token, http.StatusNotFound)
			schedule(restricted.Token, "dispatch.yml", time.Now().Add(time.Hour), in, http.StatusNotFound)
			publicOnly := getUserToken(t, owner.LowerName, auth_model.AccessTokenScopeWriteRepository, auth_model.AccessTokenScopePublicOnly)
			list(publicOnly, http.StatusNotFound)
			schedule(publicOnly, "dispatch.yml", time.Now().Add(time.Hour), in, http.StatusNotFound)
			del(restricted.Token, sd.ID, http.StatusNotFound)
			require.Len(t, list(ownerToken, http.StatusOK), 1)

			// the scheduler, a repository admin and a site admin remove it; another writer (the owner here) is a repository admin too
			del(writerToken, sd.ID, http.StatusNoContent)
			del(writerToken, sd.ID, http.StatusNotFound)
			for _, token := range []string{adminToken, siteAdminToken, ownerToken} {
				sd = schedule(writerToken, "dispatch.yml", time.Now().Add(time.Hour), in, http.StatusCreated)
				del(token, sd.ID, http.StatusNoContent)
			}
			// a writer who is neither the scheduler nor an admin
			peer := forgery.CreateUser(t, nil)
			require.NoError(t, repo_module.AddCollaborator(t.Context(), repo, peer))
			require.NoError(t, repo_module.ChangeCollaborationAccessMode(t.Context(), repo, peer.ID, perm.AccessModeWrite))
			sd = schedule(ownerToken, "dispatch.yml", time.Now().Add(time.Hour), in, http.StatusCreated)
			del(getUserToken(t, peer.LowerName, auth_model.AccessTokenScopeWriteRepository), sd.ID, http.StatusForbidden)
			del(ownerToken, sd.ID, http.StatusNoContent)
			assert.Empty(t, list(ownerToken, http.StatusOK))
		})

		t.Run("starts once at the time", func(t *testing.T) {
			now := time.Now()
			sd := schedule(writerToken, "dispatch.yml", now.Add(time.Hour), in, http.StatusCreated)

			require.NoError(t, actions_service.StartDueScheduledDispatches(t.Context()))
			assert.Zero(t, countRuns(), "not before the time")

			timeutil.MockSet(now.Add(2 * time.Hour))
			defer timeutil.MockUnset()
			require.NoError(t, actions_service.StartDueScheduledDispatches(t.Context()))
			require.Equal(t, 1, countRuns())
			require.NoError(t, actions_service.StartDueScheduledDispatches(t.Context()))
			require.Equal(t, 1, countRuns(), "never twice")
			assert.Empty(t, list(ownerToken, http.StatusOK))

			run := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{RepoID: repo.ID, TriggerEvent: "workflow_dispatch"})
			assert.Equal(t, "workflow_dispatch", run.TriggerEvent)
			assert.Equal(t, writer.ID, run.TriggerUserID)
			assert.Equal(t, sd.Ref, run.Ref)
			var payload api.WorkflowDispatchPayload
			require.NoError(t, json.Unmarshal([]byte(run.EventPayload), &payload))
			assert.Equal(t, map[string]string{"who": "me", "extra": "x"}, payload.Inputs)
			unittest.AssertSuccessfulDelete(t, &actions_model.ActionRun{ID: run.ID})
		})

		t.Run("permission lost is cancelled", func(t *testing.T) {
			now := time.Now()
			before := countRuns()
			schedule(writerToken, "dispatch.yml", now.Add(time.Hour), in, http.StatusCreated)
			require.NoError(t, repo_module.DeleteCollaboration(t.Context(), repo, writer.ID))

			timeutil.MockSet(now.Add(2 * time.Hour))
			defer timeutil.MockUnset()
			require.NoError(t, actions_service.StartDueScheduledDispatches(t.Context()))
			assert.Equal(t, before, countRuns())
			rows := list(ownerToken, http.StatusOK)
			require.Len(t, rows, 1)
			assert.Equal(t, "cancelled", rows[0].Status)
			assert.Equal(t, "no_permission", rows[0].CancelReason)
			del(ownerToken, rows[0].ID, http.StatusNoContent)
		})

		t.Run("a push to the ref cancels the run", func(t *testing.T) {
			now := time.Now()
			before := countRuns()
			schedule(ownerToken, "dispatch.yml", now.Add(time.Hour), in, http.StatusCreated)

			// someone else pushes to the branch: the run would execute that code as the scheduler
			opts := getCreateFileOptions()
			opts.BranchName, opts.NewBranchName = repo.DefaultBranch, repo.DefaultBranch
			MakeRequest(t, NewRequestWithJSON(t, http.MethodPost, fmt.Sprintf("/api/v1/repos/%s/%s/contents/later.txt", repo.OwnerName, repo.Name), &opts).
				AddTokenAuth(adminToken), http.StatusCreated)

			timeutil.MockSet(now.Add(2 * time.Hour))
			defer timeutil.MockUnset()
			require.NoError(t, actions_service.StartDueScheduledDispatches(t.Context()))
			assert.Equal(t, before, countRuns())
			rows := list(ownerToken, http.StatusOK)
			require.Len(t, rows, 1)
			assert.Equal(t, "cancelled", rows[0].Status)
			assert.Equal(t, "ref_changed", rows[0].CancelReason)
			assert.Empty(t, unittest.AssertExistsAndLoadBean(t, &actions_model.ActionScheduledDispatch{ID: rows[0].ID}).Inputs)
			del(ownerToken, rows[0].ID, http.StatusNoContent)
		})

		t.Run("pending cap", func(t *testing.T) {
			for range 100 {
				require.NoError(t, actions_model.InsertScheduledDispatch(t.Context(), &actions_model.ActionScheduledDispatch{
					RepoID: repo.ID, WorkflowID: "dispatch.yml", Ref: "refs/heads/" + repo.DefaultBranch, Inputs: "{}", DoerID: owner.ID, RunAt: timeutil.TimeStamp(time.Now().Add(time.Hour).Unix()),
				}))
			}
			schedule(ownerToken, "dispatch.yml", time.Now().Add(time.Hour), in, http.StatusBadRequest)
			require.NoError(t, actions_model.DeleteScheduledDispatchesByRepo(t.Context(), repo.ID))
		})

		t.Run("deleting the repository removes the rows", func(t *testing.T) {
			doomed := forgery.CreateRepository(t, owner, &forgery.CreateRepositoryOptions{Files: files, SkipCleanup: true})
			forgery.EnableRepoUnit(t, doomed, unit_model.TypeActions, nil)
			resp := MakeRequest(t, NewRequestWithJSON(t, http.MethodPost, fmt.Sprintf("/api/v1/repos/%s/%s/actions/workflows/dispatch.yml/dispatches", doomed.OwnerName, doomed.Name),
				&api.DispatchWorkflowOption{Ref: doomed.DefaultBranch, Inputs: in, RunAt: new(time.Now().Add(time.Hour))},
			).AddTokenAuth(ownerToken), http.StatusCreated)
			require.NotNil(t, resp)
			assert.Equal(t, 1, unittest.GetCount(t, &actions_model.ActionScheduledDispatch{RepoID: doomed.ID}))
			require.NoError(t, repo_service.DeleteRepository(db.DefaultContext, owner, doomed, false))
			assert.Zero(t, unittest.GetCount(t, &actions_model.ActionScheduledDispatch{RepoID: doomed.ID}))
		})
	})
}
