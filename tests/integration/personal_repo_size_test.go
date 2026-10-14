// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	issues_model "forgejo.org/models/issues"
	quota_model "forgejo.org/models/quota"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	"forgejo.org/modules/lfs"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/test"
	migrations_allowlist "forgejo.org/services/migrations/allowlist"
	pull_service "forgejo.org/services/pull"
	repo_service "forgejo.org/services/repository"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const personalLimitMessage = "Personal repository size limit reached ("

// setPersonalRepoSizeLimit stores a site size limit in bytes (policy allowed); the returned function removes it.
func setPersonalRepoSizeLimit(t *testing.T, limit int64) func() {
	t.Helper()
	admin := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	require.NoError(t, repo_service.SetPersonalRepoPolicy(db.DefaultContext, admin, setting.PersonalReposAllowed, limit))
	bumpSettingsRevision(t)
	return func() { resetPersonalRepoMode(t) }
}

func personalRepoUsage(t *testing.T, uid int64) int64 {
	t.Helper()
	used, err := quota_model.GetPersonalRepoUsage(db.DefaultContext, uid)
	require.NoError(t, err)
	return used
}

// lockPersonalRepos sets the site limit to the user's current usage (at the limit: locked) or one byte above it.
func lockPersonalRepos(t *testing.T, uid int64, locked bool) {
	t.Helper()
	limit := personalRepoUsage(t, uid)
	if !locked {
		limit++
	}
	require.Positive(t, limit)
	setPersonalRepoSizeLimit(t, limit)
}

func setPersonalRepoOverride(t *testing.T, uid, limit int64) {
	t.Helper()
	require.NoError(t, user_model.UpdateUserCols(db.DefaultContext, &user_model.User{ID: uid, PersonalRepoSizeLimit: limit}, "personal_repo_size_limit"))
}

func gitPushResult(dir string, args ...string) (string, error) {
	_, stderr, err := git.NewCommand(git.DefaultContext, "push").AddArguments(git.ToTrustedCmdArgs(args)...).RunStdString(&git.RunOpts{Dir: dir})
	return stderr, err
}

func commitFile(t *testing.T, dir, name string, size int) {
	t.Helper()
	data := make([]byte, size)
	_, err := rand.Read(data)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0o644))
	require.NoError(t, git.AddChanges(dir, true))
	require.NoError(t, git.CommitChanges(dir, git.CommitChangesOptions{
		Committer: &git.Signature{Email: "user2@example.com", Name: "User Two", When: time.Now()},
		Author:    &git.Signature{Email: "user2@example.com", Name: "User Two", When: time.Now()},
		Message:   "add " + name,
	}))
}

// cloneWiki is doGitClone for a wiki, which has no README.md.
func cloneWiki(t *testing.T, dir string, u *url.URL) {
	t.Helper()
	require.NoError(t, git.CloneWithArgs(t.Context(), git.AllowLFSFiltersArgs(), u.String(), dir, git.CloneRepoOptions{}))
	doGitSetRemoteURL(dir, "origin", u)(t)
}

// AC8, AC10, AC11, M2: every git push at the limit is refused before it starts, a ref delete and a wiki push too;
// one byte under is allowed; orgs, admins and overrides.
func TestPersonalRepoSizeLockGit(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		defer resetPersonalRepoMode(t)
		defer setPersonalRepoOverride(t, 2, 0)
		token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/user/repos", &api.CreateRepoOption{Name: "lock-git", AutoInit: true, DefaultBranch: "main", Readme: "Default"}).AddTokenAuth(token), http.StatusCreated)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/lock-git/wiki/new", &api.CreateWikiPageOptions{Title: "Home", ContentBase64: "aG9tZQ=="}).AddTokenAuth(token), http.StatusCreated)
		MakeRequest(t, NewRequestWithJSON(t, "PUT", "/api/v1/repos/user2/lock-git/collaborators/user4", &api.AddCollaboratorOption{}).AddTokenAuth(token), http.StatusNoContent)

		cloneURL := func(user, path string) *url.URL {
			c := *u
			c.Path = path
			c.User = url.UserPassword(user, userPassword)
			return &c
		}
		dir, wikiDir := t.TempDir(), t.TempDir()
		doGitClone(dir, cloneURL("user2", "/user2/lock-git.git"))(t)
		cloneWiki(t, wikiDir, cloneURL("user2", "/user2/lock-git.wiki.git"))
		doGitCreateBranch(dir, "to-delete")(t)
		doGitPushTestRepository(dir, "origin", "to-delete")(t)
		doGitCheckoutBranch(dir, "-b", "work")(t)
		commitFile(t, dir, "a.bin", 1000)
		commitFile(t, wikiDir, "Page.md", 100)

		lockPersonalRepos(t, 2, true)
		for name, args := range map[string][]string{"push at the limit": {"origin", "work"}, "ref delete": {"origin", ":to-delete"}} {
			stderr, err := gitPushResult(dir, args...)
			require.Error(t, err, name)
			assert.Contains(t, stderr, personalLimitMessage, name)
		}
		stderr, err := gitPushResult(wikiDir, "origin", "HEAD")
		require.Error(t, err)
		assert.Contains(t, stderr, personalLimitMessage, "wiki push")

		// a collaborator is refused without the owner's numbers
		collabDir := t.TempDir()
		doGitClone(collabDir, cloneURL("user4", "/user2/lock-git.git"))(t)
		stderr, err = gitPushResult(collabDir, "origin", ":to-delete")
		require.Error(t, err)
		assert.Contains(t, stderr, "The owner's personal repositories reached their size limit")
		assert.NotContains(t, stderr, "KiB")

		// LFS uploads
		req := NewRequestWithJSON(t, "POST", "/user2/lock-git.git/info/lfs/objects/batch", &lfs.BatchRequest{
			Operation: "upload",
			Objects:   []lfs.Pointer{{Oid: "83de2e488b89a0aa1c97496b888120a28b0c1e15463a4adb8405578c540f36d4", Size: 6}},
		}).SetHeader("Accept", lfs.AcceptHeader).SetHeader("Content-Type", lfs.MediaType).AddBasicAuth("user2")
		resp := MakeRequest(t, req, http.StatusRequestEntityTooLarge)
		assert.Contains(t, resp.Body.String(), personalLimitMessage)

		// push-to-create
		defer test.MockVariableValue(&setting.Repository.EnablePushCreateUser, true)()
		newDir := t.TempDir()
		doGitInitTestRepository(newDir, git.Sha1ObjectFormat)(t)
		doGitAddRemote(newDir, "origin", cloneURL("user2", "/user2/lock-new.git"))(t)
		stderr, err = gitPushResult(newDir, "origin", "master")
		require.Error(t, err)
		assert.Contains(t, stderr, personalLimitMessage)
		assert.False(t, repoExists(t, "user2", "lock-new"))

		// organizations and admins are never locked, even with a site limit of one byte
		setPersonalRepoSizeLimit(t, 1)
		orgToken := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/orgs/org3/repos", &api.CreateRepoOption{Name: "lock-org", AutoInit: true, DefaultBranch: "main"}).AddTokenAuth(orgToken), http.StatusCreated)
		orgDir := t.TempDir()
		doGitClone(orgDir, cloneURL("user2", "/org3/lock-org.git"))(t)
		commitFile(t, orgDir, "org.bin", 100)
		doGitPushTestRepository(orgDir, "origin", "HEAD")(t)
		adminToken := getTokenForLoggedInUser(t, loginUser(t, "user1"), auth_model.AccessTokenScopeAll)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/user/repos", &api.CreateRepoOption{Name: "lock-admin", AutoInit: true, DefaultBranch: "main"}).AddTokenAuth(adminToken), http.StatusCreated)

		// a per-user override: unlimited, then higher
		setPersonalRepoOverride(t, 2, -1)
		doGitPushTestRepository(dir, "origin", "work")(t)
		setPersonalRepoOverride(t, 2, personalRepoUsage(t, 2)+1)
		doGitPushTestRepository(wikiDir, "origin", "HEAD")(t)
		setPersonalRepoOverride(t, 2, 0)

		// one byte under the limit: allowed; the push that crosses it is accepted, the next is refused
		lockPersonalRepos(t, 2, false)
		before := personalRepoUsage(t, 2)
		commitFile(t, dir, "b.bin", 2000)
		doGitPushTestRepository(dir, "origin", "work")(t)
		assert.Greater(t, personalRepoUsage(t, 2), before, "the push refreshed the size")
		commitFile(t, dir, "c.bin", 10)
		stderr, err = gitPushResult(dir, "origin", "work")
		require.Error(t, err)
		assert.Contains(t, stderr, personalLimitMessage)
		// and the wiki push refreshed the wiki size
		assert.Positive(t, unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{OwnerName: "user2", LowerName: "lock-git"}).WikiSize)

		// writes reopen when the usage drops (here: a higher limit)
		lockPersonalRepos(t, 2, false)
		doGitPushTestRepository(dir, "origin", ":to-delete")(t)
	})
}

// S1: a push to the wiki is measured again.
func TestPersonalRepoSizeWikiPushMeasured(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/user/repos", &api.CreateRepoOption{Name: "wiki-size"}).AddTokenAuth(token), http.StatusCreated)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/wiki-size/wiki/new", &api.CreateWikiPageOptions{Title: "Home", ContentBase64: "aG9tZQ=="}).AddTokenAuth(token), http.StatusCreated)
		before := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{OwnerName: "user2", LowerName: "wiki-size"}).WikiSize
		dir := t.TempDir()
		c := *u
		c.Path = "/user2/wiki-size.wiki.git"
		c.User = url.UserPassword("user2", userPassword)
		cloneWiki(t, dir, &c)
		commitFile(t, dir, "Big.md", 200000)
		doGitPushTestRepository(dir, "origin", "HEAD")(t)
		assert.Greater(t, unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{OwnerName: "user2", LowerName: "wiki-size"}).WikiSize, before+100000)
	})
}

// AC8: the other writes that add content are refused at the limit, with the message; they work again below it.
func TestPersonalRepoSizeLockWrites(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		defer resetPersonalRepoMode(t)
		defer test.MockVariableValue(&setting.Migrations.AllowLocalNetworks, true)()
		defer test.MockVariableValue(&setting.Migrations.AllowUnencrypted, true)()
		require.NoError(t, migrations_allowlist.Init())
		session := loginUser(t, "user2")
		token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeAll)
		other := getTokenForLoggedInUser(t, loginUser(t, "user5"), auth_model.AccessTokenScopeAll)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/user/repos", &api.CreateRepoOption{Name: "lock-writes", AutoInit: true, DefaultBranch: "main"}).AddTokenAuth(token), http.StatusCreated)
		var release api.Release
		DecodeJSON(t, MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/lock-writes/releases", &api.CreateReleaseOption{TagName: "v1", Title: "v1"}).AddTokenAuth(token), http.StatusCreated), &release)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/user/repos", &api.CreateRepoOption{Name: "transfer-me", AutoInit: true, DefaultBranch: "main"}).AddTokenAuth(other), http.StatusCreated)
		var readme api.ContentsResponse
		DecodeJSON(t, MakeRequest(t, NewRequest(t, "GET", "/api/v1/repos/user2/lock-writes/contents/README.md").AddTokenAuth(token), http.StatusOK), &readme)
		var branch api.Branch
		DecodeJSON(t, MakeRequest(t, NewRequest(t, "GET", "/api/v1/repos/user2/lock-writes/branches/main").AddTokenAuth(token), http.StatusOK), &branch)
		sha := branch.Commit.ID
		MakeRequest(t, NewRequestWithJSON(t, "PUT", "/api/v1/repos/user2/lock-writes/collaborators/user4", &api.AddCollaboratorOption{}).AddTokenAuth(token), http.StatusNoContent)
		collaborator := getTokenForLoggedInUser(t, loginUser(t, "user4"), auth_model.AccessTokenScopeAll)

		lockPersonalRepos(t, 2, true)
		refusedAPI := func(t *testing.T, req *RequestWrapper) {
			t.Helper()
			var apiErr api.APIError
			DecodeJSON(t, MakeRequest(t, req, http.StatusRequestEntityTooLarge), &apiErr)
			assert.Contains(t, apiErr.Message, personalLimitMessage)
		}
		content := "aGVsbG8="
		refusedAPI(t, NewRequestWithJSON(t, "POST", "/api/v1/user/repos", &api.CreateRepoOption{Name: "lock-new"}).AddTokenAuth(token))
		refusedAPI(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user27/template1/generate", &api.GenerateRepoOption{Owner: "user2", Name: "lock-tpl", GitContent: true}).AddTokenAuth(token))
		refusedAPI(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user5/transfer-me/forks", &api.CreateForkOption{}).AddTokenAuth(token))
		cloneURL := *u
		cloneURL.Path = "/user2/repo1.git"
		refusedAPI(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/migrate", &api.MigrateRepoOptions{CloneAddr: cloneURL.String(), RepoOwner: "user2", RepoName: "lock-mig"}).AddTokenAuth(token))
		refusedAPI(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/lock-writes/contents/new.txt", &api.CreateFileOptions{ContentBase64: content}).AddTokenAuth(token))
		refusedAPI(t, NewRequestWithJSON(t, "DELETE", "/api/v1/repos/user2/lock-writes/contents/README.md", &api.DeleteFileOptions{SHA: readme.SHA}).AddTokenAuth(token)) // only the web UI deletes
		refusedAPI(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user5/transfer-me/transfer", &api.TransferRepoOption{NewOwner: "user2"}).AddTokenAuth(other))
		// git notes add objects too
		refusedAPI(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/lock-writes/git/notes/"+sha, &api.NoteOptions{Message: "note"}).AddTokenAuth(token))
		session.MakeRequest(t, NewRequestWithValues(t, "POST", "/user2/lock-writes/commit/"+sha+"/notes", map[string]string{"notes": "note"}), http.StatusRequestEntityTooLarge)
		// a collaborator is refused without the owner's numbers
		var apiErr api.APIError
		DecodeJSON(t, MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/lock-writes/contents/collab.txt", &api.CreateFileOptions{ContentBase64: content}).AddTokenAuth(collaborator), http.StatusRequestEntityTooLarge), &apiErr)
		assert.Contains(t, apiErr.Message, "The owner's personal repositories reached their size limit")
		assert.NotContains(t, apiErr.Message, "KiB")

		body := &bytes.Buffer{}
		form := multipart.NewWriter(body)
		file, err := form.CreateFormFile("attachment", "a.txt")
		require.NoError(t, err)
		_, _ = file.Write([]byte("attachment"))
		require.NoError(t, form.Close())
		req := NewRequestWithBody(t, "POST", fmt.Sprintf("/api/v1/repos/user2/lock-writes/releases/%d/assets?name=a.txt", release.ID), body).AddTokenAuth(token)
		req.Header.Add("Content-Type", form.FormDataContentType())
		refusedAPI(t, req)

		resp := MakeRequest(t, NewRequestWithBody(t, "PUT", "/api/packages/user2/generic/lock/1.0/f.txt", strings.NewReader("package")).AddBasicAuth("user2"), http.StatusRequestEntityTooLarge)
		assert.Contains(t, resp.Body.String(), personalLimitMessage)

		// the web editor refuses with the 413 page and the message
		resp = session.MakeRequest(t, NewRequest(t, "GET", "/user2/lock-writes/_new/main/"), http.StatusRequestEntityTooLarge)
		assert.Contains(t, resp.Body.String(), "Personal repository size limit reached")

		// below the limit again: allowed
		lockPersonalRepos(t, 2, false)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/lock-writes/git/notes/"+sha, &api.NoteOptions{Message: "note"}).AddTokenAuth(token), http.StatusOK)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/lock-writes/contents/new.txt", &api.CreateFileOptions{ContentBase64: content}).AddTokenAuth(token), http.StatusCreated)
	})
}

// S3: writes that copy objects into another user's repositories are checked against that user's limit.
func TestPersonalRepoSizeLockCrossRepo(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		defer resetPersonalRepoMode(t)
		owner := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeAll)
		forker := loginUser(t, "user5")
		forkerToken := getTokenForLoggedInUser(t, forker, auth_model.AccessTokenScopeAll)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/user/repos", &api.CreateRepoOption{Name: "cross-base", AutoInit: true, DefaultBranch: "main"}).AddTokenAuth(owner), http.StatusCreated)
		forkName := "cross-fork"
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/cross-base/forks", &api.CreateForkOption{Name: &forkName}).AddTokenAuth(forkerToken), http.StatusAccepted)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user5/cross-fork/contents/fork.txt", &api.CreateFileOptions{ContentBase64: "Zm9yaw==", FileOptions: api.FileOptions{NewBranchName: "feature"}}).AddTokenAuth(forkerToken), http.StatusCreated)
		var pr api.PullRequest
		DecodeJSON(t, MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/cross-base/pulls", &api.CreatePullRequestOption{Head: "user5:feature", Base: "main", Title: "first"}).AddTokenAuth(forkerToken), http.StatusCreated), &pr)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/cross-base/contents/base.txt", &api.CreateFileOptions{ContentBase64: "YmFzZQ=="}).AddTokenAuth(owner), http.StatusCreated)

		// the base owner is locked: a PR from the fork copies the head into the base repository
		lockPersonalRepos(t, 2, true)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user5/cross-fork/contents/two.txt", &api.CreateFileOptions{ContentBase64: "dHdv", FileOptions: api.FileOptions{NewBranchName: "feature2"}}).AddTokenAuth(forkerToken), http.StatusCreated)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/cross-base/pulls", &api.CreatePullRequestOption{Head: "user5:feature2", Base: "main", Title: "second"}).AddTokenAuth(forkerToken), http.StatusRequestEntityTooLarge)
		forker.MakeRequest(t, NewRequestWithValues(t, "POST", "/user2/cross-base/compare/main...user5:feature2", map[string]string{"title": "second web"}), http.StatusRequestEntityTooLarge)
		prModel, err := issues_model.GetPullRequestByID(db.DefaultContext, pr.ID)
		require.NoError(t, err)
		require.ErrorAs(t, pull_service.PushToBaseRepo(db.DefaultContext, prModel), &quota_model.ErrPersonalRepoLimit{})
		resetPersonalRepoMode(t)

		// the fork owner is locked: syncing the fork and updating the PR write to the fork
		lockPersonalRepos(t, 5, true)
		MakeRequest(t, NewRequest(t, "POST", "/api/v1/repos/user5/cross-fork/sync_fork").AddTokenAuth(forkerToken), http.StatusRequestEntityTooLarge)
		forker.MakeRequest(t, NewRequestWithValues(t, "POST", "/user5/cross-fork/sync_fork", map[string]string{"branch": "main"}), http.StatusRequestEntityTooLarge)
		MakeRequest(t, NewRequest(t, "POST", fmt.Sprintf("/api/v1/repos/user2/cross-base/pulls/%d/update", pr.Index)).AddTokenAuth(forkerToken), http.StatusRequestEntityTooLarge)
		forker.MakeRequest(t, NewRequest(t, "POST", fmt.Sprintf("/user2/cross-base/pulls/%d/update", pr.Index)), http.StatusRequestEntityTooLarge)
		resetPersonalRepoMode(t)
		MakeRequest(t, NewRequest(t, "POST", fmt.Sprintf("/api/v1/repos/user2/cross-base/pulls/%d/update", pr.Index)).AddTokenAuth(forkerToken), http.StatusOK)
	})
}

// AC9, S2, Q4/Q7: every web delete works at the limit; the gc after it frees the space and writes reopen.
func TestPersonalRepoSizeLockDeletes(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		defer resetPersonalRepoMode(t)
		defer test.MockVariableValue(&repo_service.ShrinkPruneExpire, "now")()
		defer test.MockVariableValue(&setting.LFS.StartServer, true)()
		session := loginUser(t, "user2")
		token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeAll)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/user/repos", &api.CreateRepoOption{Name: "lock-del", AutoInit: true, DefaultBranch: "main", Readme: "Default"}).AddTokenAuth(token), http.StatusCreated)
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{OwnerName: "user2", LowerName: "lock-del"})
		for _, f := range []string{"a.txt", "b.txt"} {
			MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/lock-del/contents/"+f, &api.CreateFileOptions{ContentBase64: "ZmlsZQ=="}).AddTokenAuth(token), http.StatusCreated)
		}
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/lock-del/tags", &api.CreateTagOption{TagName: "t1"}).AddTokenAuth(token), http.StatusCreated)
		var release api.Release
		DecodeJSON(t, MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/lock-del/releases", &api.CreateReleaseOption{TagName: "v1", Title: "v1"}).AddTokenAuth(token), http.StatusCreated), &release)
		body := &bytes.Buffer{}
		form := multipart.NewWriter(body)
		file, err := form.CreateFormFile("attachment", "a.txt")
		require.NoError(t, err)
		_, _ = file.Write([]byte("attachment"))
		require.NoError(t, form.Close())
		req := NewRequestWithBody(t, "POST", fmt.Sprintf("/api/v1/repos/user2/lock-del/releases/%d/assets?name=a.txt", release.ID), body).AddTokenAuth(token)
		req.Header.Add("Content-Type", form.FormDataContentType())
		var attachment api.Attachment
		DecodeJSON(t, MakeRequest(t, req, http.StatusCreated), &attachment)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/lock-del/wiki/new", &api.CreateWikiPageOptions{Title: "Gone", ContentBase64: "Z29uZQ=="}).AddTokenAuth(token), http.StatusCreated)
		lfsData := []byte("lfs object of lock-del")
		oid := storeObjectInRepo(t, repo.ID, &lfsData)
		trash := "lock-trash"
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/user/repos", &api.CreateRepoOption{Name: trash}).AddTokenAuth(token), http.StatusCreated)

		// a branch with a large file: deleting it frees the space once the gc prunes it
		dir := t.TempDir()
		c := *u
		c.Path = "/user2/lock-del.git"
		c.User = url.UserPassword("user2", userPassword)
		doGitClone(dir, &c)(t)
		doGitCheckoutBranch(dir, "-b", "big")(t)
		commitFile(t, dir, "big.bin", 600000)
		doGitPushTestRepository(dir, "origin", "big")(t)

		lockPersonalRepos(t, 2, true)
		locked := personalRepoUsage(t, 2)

		// the delete page: no new branch, and the default message instead of the user's
		lastCommit := func() string {
			doc := NewHTMLParser(t, session.MakeRequest(t, NewRequest(t, "GET", "/user2/lock-del/_delete/main/a.txt"), http.StatusOK).Body)
			return doc.GetInputValueByName("last_commit")
		}
		resp := session.MakeRequest(t, NewRequestWithValues(t, "POST", "/user2/lock-del/_delete/main/a.txt", map[string]string{
			"last_commit": lastCommit(), "commit_choice": "commit-to-new-branch", "new_branch_name": "sneaky", "commit_mail_id": "-1",
		}), http.StatusOK)
		assert.Contains(t, NewHTMLParser(t, resp.Body).Find(".flash-error").Text(), "only deletions are possible")
		session.MakeRequest(t, NewRequestWithValues(t, "POST", "/user2/lock-del/_delete/main/a.txt", map[string]string{
			"last_commit": lastCommit(), "commit_choice": "direct", "commit_summary": "big summary", "commit_message": strings.Repeat("x", 100000), "commit_mail_id": "-1",
		}), http.StatusSeeOther)
		gitRepo, err := git.OpenRepository(git.DefaultContext, repo.RepoPath())
		require.NoError(t, err)
		commit, err := gitRepo.GetBranchCommit("main")
		require.NoError(t, err)
		assert.Equal(t, "Delete a.txt\n", commit.CommitMessage)
		assert.False(t, git.IsBranchExist(git.DefaultContext, repo.RepoPath(), "sneaky"))
		gitRepo.Close()

		session.MakeRequest(t, NewRequestWithValues(t, "POST", "/user2/lock-del/tags/delete", map[string]string{"id": strconv.FormatInt(unittest.AssertExistsAndLoadBean(t, &repo_model.Release{RepoID: repo.ID, TagName: "t1"}).ID, 10)}), http.StatusOK)
		unittest.AssertNotExistsBean(t, &repo_model.Release{RepoID: repo.ID, TagName: "t1"})
		session.MakeRequest(t, NewRequestWithValues(t, "POST", "/user2/lock-del/releases/attachments/remove", map[string]string{"file": attachment.UUID}), http.StatusOK)
		unittest.AssertNotExistsBean(t, &repo_model.Attachment{UUID: attachment.UUID})
		session.MakeRequest(t, NewRequestWithValues(t, "POST", "/user2/lock-del/releases/delete", map[string]string{"id": strconv.FormatInt(release.ID, 10)}), http.StatusOK)
		assert.True(t, unittest.AssertExistsAndLoadBean(t, &repo_model.Release{ID: release.ID}).IsTag, "the release is gone, its tag stays")
		session.MakeRequest(t, NewRequest(t, "POST", "/user2/lock-del/wiki/Gone?action=_delete"), http.StatusOK)
		session.MakeRequest(t, NewRequest(t, "POST", "/user2/lock-del/settings/lfs/delete/"+oid), http.StatusSeeOther)
		unittest.AssertNotExistsBean(t, &git_model.LFSMetaObject{RepositoryID: repo.ID, Pointer: lfs.Pointer{Oid: oid}})
		session.MakeRequest(t, NewRequestWithValues(t, "POST", "/user2/"+trash+"/settings", map[string]string{"action": "delete", "repo_name": "user2/" + trash}), http.StatusSeeOther)
		assert.False(t, repoExists(t, "user2", trash))

		session.MakeRequest(t, NewRequestWithValues(t, "POST", "/user2/lock-del/branches/delete", map[string]string{"name": "big"}), http.StatusOK)
		assert.False(t, git.IsBranchExist(git.DefaultContext, repo.RepoPath(), "big"))
		assert.Less(t, personalRepoUsage(t, 2), locked-300000, "the gc after the delete freed the space")

		// writes work again
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/user2/lock-del/contents/back.txt", &api.CreateFileOptions{ContentBase64: "YmFjaw=="}).AddTokenAuth(token), http.StatusCreated)
	})
}

// AC4, AC5: the admin page, the per-user override (web and API) and the usage on the admin list and settings.
func TestPersonalRepoSizeAdmin(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	defer resetPersonalRepoMode(t)
	defer setPersonalRepoOverride(t, 2, 0)
	admin := loginUser(t, "user1")

	logChecker, cleanup := test.NewLogChecker(log.DEFAULT, log.INFO)
	logChecker.Filter("Personal repository size limit changed from 0 to 5242880 bytes by user1", "Personal repository size limit of user2 changed from 0 to 1 by user1",
		"Personal repository size limit of user5 changed from 0 to -1 by user1")
	defer cleanup()
	admin.MakeRequest(t, NewRequestWithValues(t, "POST", "/admin/personal-repos", map[string]string{"mode": "allowed", "size_limit_mib": "5"}), http.StatusSeeOther)
	bumpSettingsRevision(t)
	assert.EqualValues(t, 5<<20, setting.Config().PersonalRepos.Value(db.DefaultContext).EffectiveSizeLimit())
	admin.MakeRequest(t, NewRequestWithValues(t, "POST", "/admin/personal-repos", map[string]string{"mode": "allowed", "size_limit_mib": "-1"}), http.StatusBadRequest)

	// the API override: bytes; organizations and values below -1 are refused
	adminToken := getTokenForLoggedInUser(t, admin, auth_model.AccessTokenScopeAll)
	one, minusTwo, minusOne := int64(1), int64(-2), int64(-1)
	MakeRequest(t, NewRequestWithJSON(t, "PATCH", "/api/v1/admin/users/user2", &api.EditUserOption{PersonalRepoSizeLimit: &one}).AddTokenAuth(adminToken), http.StatusOK)
	MakeRequest(t, NewRequestWithJSON(t, "PATCH", "/api/v1/admin/users/user2", &api.EditUserOption{PersonalRepoSizeLimit: &minusTwo}).AddTokenAuth(adminToken), http.StatusUnprocessableEntity)
	MakeRequest(t, NewRequestWithJSON(t, "PATCH", "/api/v1/admin/users/org3", &api.EditUserOption{PersonalRepoSizeLimit: &minusOne}).AddTokenAuth(adminToken), http.StatusUnprocessableEntity)
	assert.EqualValues(t, 1, unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2}).PersonalRepoSizeLimit)

	// the web override: MiB, -1 unlimited
	user5 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 5})
	admin.MakeRequest(t, NewRequestWithValues(t, "POST", "/admin/users/5/edit", map[string]string{
		"login_type": "0-0", "login_name": user5.LoginName, "email": user5.Email, "max_repo_creation": "-1", "personal_repo_size_limit": "-1",
	}), http.StatusSeeOther)
	defer setPersonalRepoOverride(t, 5, 0)
	assert.EqualValues(t, -1, unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 5}).PersonalRepoSizeLimit)
	filtered, _ := logChecker.Check(5 * time.Second)
	assert.Equal(t, []bool{true, true, true}, filtered)

	// user2 is over its 1-byte override: listed, shown in the admin list and on its settings
	doc := NewHTMLParser(t, admin.MakeRequest(t, NewRequest(t, "GET", "/admin/personal-repos"), http.StatusOK).Body)
	assert.Contains(t, doc.Find("#personal-repo-over-limit").Text(), "user2")
	assert.NotContains(t, doc.Find("#personal-repo-over-limit").Text(), "user5")
	doc = NewHTMLParser(t, admin.MakeRequest(t, NewRequest(t, "GET", "/admin/users?q=user2"), http.StatusOK).Body)
	assert.Contains(t, doc.Find(".personal-repo-usage").First().Text(), "/ 1 B")
	doc = NewHTMLParser(t, loginUser(t, "user2").MakeRequest(t, NewRequest(t, "GET", "/user/settings/repos"), http.StatusOK).Body)
	assert.Contains(t, doc.Find("#personal-repo-limit").Text(), "only deletions are possible")
}

// M3: with the limit off a quota check reads only the cached setting; with it on, the user and the usage.
func TestPersonalRepoSizeQueries(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	defer resetPersonalRepoMode(t)
	evaluate := func() {
		_, err := quota_model.EvaluateForUser(db.DefaultContext, 2, quota_model.LimitSubjectSizeReposAll)
		require.NoError(t, err)
	}
	assert.EqualValues(t, 0, stableQueryCount(t, evaluate))
	setPersonalRepoSizeLimit(t, 1<<40)
	assert.EqualValues(t, 2, stableQueryCount(t, evaluate))
}
