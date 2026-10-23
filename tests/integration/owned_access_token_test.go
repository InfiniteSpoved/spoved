// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	audit_model "forgejo.org/models/audit"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	perm_model "forgejo.org/models/perm"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/timeutil"
	repo_service "forgejo.org/services/repository"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newOwnedAccessToken creates a token of an organization, a project or a repository (#28) and returns its secret.
func newOwnedAccessToken(t *testing.T, ownerType auth_model.AccessTokenOwnerType, ownerID, groupID int64, mode perm_model.AccessMode, scope auth_model.AccessTokenScope, repoIDs ...int64) *auth_model.AccessToken {
	t.Helper()
	token := &auth_model.AccessToken{
		Name:      fmt.Sprintf("owned-token-%d", tokenCounter.Add(1)),
		OwnerType: ownerType, OwnerID: ownerID, GroupID: groupID, Mode: mode, Scope: scope,
		ResourceAllRepos: len(repoIDs) == 0,
	}
	resources := make([]*auth_model.AccessTokenResourceRepo, 0, len(repoIDs))
	for _, id := range repoIDs {
		resources = append(resources, &auth_model.AccessTokenResourceRepo{RepoID: id})
	}
	require.NoError(t, auth_model.NewAccessToken(audit_model.WithSystem(db.DefaultContext, "test"), token, resources))
	return token
}

func TestOwnedAccessToken(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		repo3 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 3}) // org3, private
		repo5 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 5}) // org3, private

		gitURL := func(token string, repo *repo_model.Repository) *url.URL {
			c := *u
			c.Path = "/" + repo.FullName() + ".git"
			c.User = url.UserPassword("anything", token)
			return &c
		}
		apiStatus := func(t *testing.T, method, path, token string, body any) int {
			t.Helper()
			req := NewRequestWithJSON(t, method, path, body)
			if body == nil {
				req = NewRequest(t, method, path)
			}
			return MakeRequest(t, req.AddTokenAuth(token), NoExpectedStatus).Code
		}
		push := func(t *testing.T, token string, repo *repo_model.Repository, branch string) (string, error) {
			t.Helper()
			dst := t.TempDir()
			require.NoError(t, git.Clone(t.Context(), gitURL(token, repo).String(), dst, git.CloneRepoOptions{}))
			doGitSetRemoteURL(dst, "origin", gitURL(token, repo))(t)
			generateCommitWithNewData(t, 64, dst, "bot@example.com", "Bot", "data-")
			_, stderr, err := git.NewCommand(git.DefaultContext, "push", "origin").AddDynamicArguments("HEAD:refs/heads/" + branch).RunStdString(&git.RunOpts{Dir: dst})
			return stderr, err
		}

		t.Run("repository token with write", func(t *testing.T) {
			token := newOwnedAccessToken(t, auth_model.AccessTokenOwnerRepo, repo3.ID, 0, perm_model.AccessModeWrite, "write:repository")
			_, err := push(t, token.Token, repo3, "bot-write")
			require.NoError(t, err)

			assert.Equal(t, http.StatusOK, apiStatus(t, "GET", "/api/v1/repos/org3/repo3", token.Token, nil))
			assert.Equal(t, http.StatusOK, apiStatus(t, "GET", "/api/v1/repos/org3/repo3/branches/bot-write", token.Token, nil))
			assert.Equal(t, http.StatusNotFound, apiStatus(t, "GET", "/api/v1/repos/org3/repo5", token.Token, nil), "another repository of the organization")
			assert.Equal(t, http.StatusNotFound, apiStatus(t, "GET", "/api/v1/repos/user2/repo1", token.Token, nil), "a public repository it doesn't cover")
			doGitCloneFail(gitURL(token.Token, repo5))(t)

			// what the token used is recorded
			at := unittest.AssertExistsAndLoadBean(t, &auth_model.AccessToken{ID: token.ID})
			assert.Positive(t, at.UpdatedUnix)
		})

		t.Run("repository token with read", func(t *testing.T) {
			token := newOwnedAccessToken(t, auth_model.AccessTokenOwnerRepo, repo3.ID, 0, perm_model.AccessModeRead, "write:repository,write:issue")
			stderr, err := push(t, token.Token, repo3, "bot-read")
			require.Error(t, err)
			assert.Contains(t, stderr, "is not allowed to push")
			assert.Equal(t, http.StatusNotFound, apiStatus(t, "GET", "/api/v1/repos/org3/repo3/branches/bot-read", token.Token, nil))

			assert.Equal(t, http.StatusOK, apiStatus(t, "GET", "/api/v1/repos/org3/repo3/issues", token.Token, nil))
			// write scopes don't raise a read permission: the lower of the two applies
			assert.Equal(t, http.StatusNotFound, apiStatus(t, "POST", "/api/v1/repos/org3/repo3/issues", token.Token, &api.CreateIssueOption{Title: "by a read token"}))
		})

		t.Run("issues and comments with write", func(t *testing.T) {
			token := newOwnedAccessToken(t, auth_model.AccessTokenOwnerRepo, repo3.ID, 0, perm_model.AccessModeWrite, "write:issue")
			req := NewRequestWithJSON(t, "POST", "/api/v1/repos/org3/repo3/issues", &api.CreateIssueOption{Title: "by a repository token"}).AddTokenAuth(token.Token)
			apiIssue := &api.Issue{}
			DecodeJSON(t, MakeRequest(t, req, http.StatusCreated), apiIssue)
			issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: apiIssue.ID})
			assert.Equal(t, user_model.AccessTokenBotID(token.ID), issue.PosterID)
			assert.Equal(t, token.Name+" [bot]", apiIssue.Poster.FullName)

			assert.Equal(t, http.StatusForbidden, apiStatus(t, "GET", "/api/v1/repos/org3/repo3", token.Token, nil), "no repository scope")
		})

		t.Run("organization token", func(t *testing.T) {
			all := newOwnedAccessToken(t, auth_model.AccessTokenOwnerOrg, 3, 0, perm_model.AccessModeRead, "read:repository")
			selected := newOwnedAccessToken(t, auth_model.AccessTokenOwnerOrg, 3, 0, perm_model.AccessModeRead, "read:repository", repo5.ID)
			for _, repo := range []*repo_model.Repository{repo3, repo5} {
				require.NoError(t, git.Clone(t.Context(), gitURL(all.Token, repo).String(), t.TempDir(), git.CloneRepoOptions{}), repo.FullName())
			}
			require.NoError(t, git.Clone(t.Context(), gitURL(selected.Token, repo5).String(), t.TempDir(), git.CloneRepoOptions{}))
			doGitCloneFail(gitURL(selected.Token, repo3))(t)
			assert.Equal(t, http.StatusNotFound, apiStatus(t, "GET", "/api/v1/repos/org3/repo3", selected.Token, nil))
		})

		t.Run("project token", func(t *testing.T) {
			org3 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 3})
			group, err := repo_model.CreateRepoGroup(audit_model.WithSystem(db.DefaultContext, "test"), org3, "bots", "")
			require.NoError(t, err)
			token := newOwnedAccessToken(t, auth_model.AccessTokenOwnerProject, 3, group.ID, perm_model.AccessModeRead, "read:repository")
			doGitCloneFail(gitURL(token.Token, repo5))(t)
			assert.Equal(t, http.StatusNotFound, apiStatus(t, "GET", "/api/v1/repos/org3/repo5", token.Token, nil))

			user1 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
			require.NoError(t, repo_service.MoveRepository(audit_model.WithSystem(db.DefaultContext, "test"), user1, repo5, group.ID))
			repo5 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 5})
			require.NoError(t, git.Clone(t.Context(), gitURL(token.Token, repo5).String(), t.TempDir(), git.CloneRepoOptions{}))
			assert.Equal(t, http.StatusOK, apiStatus(t, "GET", "/api/v1/repos/org3/bots/repo5/-", token.Token, nil))
		})

		t.Run("person and instance endpoints are refused", func(t *testing.T) {
			token := newOwnedAccessToken(t, auth_model.AccessTokenOwnerRepo, repo3.ID, 0, perm_model.AccessModeAdmin, "write:repository,write:issue,write:package,read:organization")
			for _, path := range []string{"/api/v1/user", "/api/v1/user/repos", "/api/v1/notifications", "/api/v1/admin/users"} {
				assert.Equal(t, http.StatusForbidden, apiStatus(t, "GET", path, token.Token, nil), path)
			}
			assert.Equal(t, http.StatusForbidden, apiStatus(t, "POST", "/api/v1/user/repos", token.Token, &api.CreateRepoOption{Name: "by-a-bot"}))
			assert.Equal(t, http.StatusForbidden, apiStatus(t, "GET", "/api/v1/users/user2/tokens", token.Token, nil), "a token can't manage tokens")

			// the token's identity has no namespace: nothing can be created in it
			assert.Equal(t, http.StatusForbidden, apiStatus(t, "POST", "/api/v1/repos/org3/repo3/forks", token.Token, &api.CreateForkOption{}))
			assert.NotEqual(t, http.StatusCreated, apiStatus(t, "POST", "/api/v1/repos/migrate", token.Token, &api.MigrateRepoOptions{CloneAddr: "https://example.com/a.git", RepoName: "by-a-bot"}))
			unittest.AssertNotExistsBean(t, &repo_model.Repository{OwnerID: user_model.AccessTokenBotID(token.ID)})
		})

		t.Run("stored scopes that aren't allowed are refused on use", func(t *testing.T) {
			token := newOwnedAccessToken(t, auth_model.AccessTokenOwnerRepo, repo3.ID, 0, perm_model.AccessModeRead, "read:repository")
			_, err := db.GetEngine(db.DefaultContext).Exec("UPDATE access_token SET scope = 'all' WHERE id = ?", token.ID)
			require.NoError(t, err)
			assert.Equal(t, http.StatusUnauthorized, apiStatus(t, "GET", "/api/v1/repos/org3/repo3", token.Token, nil))
			doGitCloneFail(gitURL(token.Token, repo3))(t)
		})

		t.Run("expired and deleted tokens are refused", func(t *testing.T) {
			expired := newOwnedAccessToken(t, auth_model.AccessTokenOwnerRepo, repo3.ID, 0, perm_model.AccessModeRead, "read:repository")
			_, err := db.GetEngine(db.DefaultContext).Exec("UPDATE access_token SET expires_unix = ? WHERE id = ?", timeutil.TimeStampNow().AddDuration(-time.Hour), expired.ID)
			require.NoError(t, err)
			assert.Equal(t, http.StatusUnauthorized, apiStatus(t, "GET", "/api/v1/repos/org3/repo3", expired.Token, nil))
			doGitCloneFail(gitURL(expired.Token, repo3))(t)

			deleted := newOwnedAccessToken(t, auth_model.AccessTokenOwnerOrg, 3, 0, perm_model.AccessModeRead, "read:repository")
			assert.Equal(t, http.StatusOK, apiStatus(t, "GET", "/api/v1/repos/org3/repo3", deleted.Token, nil))
			_, err = db.GetEngine(db.DefaultContext).ID(deleted.ID).Delete(&auth_model.AccessToken{})
			require.NoError(t, err)
			assert.Equal(t, http.StatusUnauthorized, apiStatus(t, "GET", "/api/v1/repos/org3/repo3", deleted.Token, nil))
		})

		t.Run("sees owners and packages as an anonymous visitor does", func(t *testing.T) {
			token := newOwnedAccessToken(t, auth_model.AccessTokenOwnerOrg, 3, 0, perm_model.AccessModeAdmin, "read:organization,write:package")
			bot, err := user_model.GetPossibleUserByID(db.DefaultContext, user_model.AccessTokenBotID(token.ID))
			require.NoError(t, err)
			assert.Equal(t, fmt.Sprintf("token+%d", token.ID), bot.Name, "never a valid user name")
			limitedOrg := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 22})
			assert.False(t, user_model.IsUserVisibleToViewer(db.DefaultContext, limitedOrg, bot))

			assert.Equal(t, http.StatusNotFound, apiStatus(t, "GET", "/api/v1/orgs/limited_org", token.Token, nil))
			var orgs []*api.Organization
			DecodeJSON(t, MakeRequest(t, NewRequest(t, "GET", "/api/v1/orgs?limit=50").AddTokenAuth(token.Token), http.StatusOK), &orgs)
			for _, org := range orgs {
				assert.Equal(t, "public", org.Visibility, org.UserName)
			}
			for _, owner := range []string{"user2", "limited_org", "org3"} {
				assert.NotEqual(t, http.StatusOK, apiStatus(t, "GET", "/api/v1/packages/"+owner, token.Token, nil), owner)
			}
		})

		t.Run("transfer deletes the repository's tokens and stops the old owner's", func(t *testing.T) {
			repoToken := newOwnedAccessToken(t, auth_model.AccessTokenOwnerRepo, repo3.ID, 0, perm_model.AccessModeRead, "read:repository")
			orgToken := newOwnedAccessToken(t, auth_model.AccessTokenOwnerOrg, 3, 0, perm_model.AccessModeRead, "read:repository", repo3.ID)
			_, err := db.GetEngine(db.DefaultContext).Exec("DELETE FROM repo_transfer WHERE repo_id = ?", repo3.ID)
			require.NoError(t, err)
			_, err = db.GetEngine(db.DefaultContext).Exec("UPDATE repository SET status = ? WHERE id = ?", repo_model.RepositoryReady, repo3.ID)
			require.NoError(t, err)
			user1 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
			repo3 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 3})
			require.NoError(t, repo_service.TransferOwnership(audit_model.WithSystem(db.DefaultContext, "test"), user1, unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2}), repo3, nil))

			unittest.AssertNotExistsBean(t, &auth_model.AccessToken{ID: repoToken.ID})
			unittest.AssertExistsAndLoadBean(t, &auth_model.AccessToken{ID: orgToken.ID})
			unittest.AssertNotExistsBean(t, &auth_model.AccessTokenResourceRepo{TokenID: orgToken.ID})
			assert.Equal(t, http.StatusNotFound, apiStatus(t, "GET", "/api/v1/repos/user2/repo3", orgToken.Token, nil))
		})
	})
}
