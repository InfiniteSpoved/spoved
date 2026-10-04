// Copyright 2018 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	org_model "forgejo.org/models/organization"
	"forgejo.org/models/perm"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	"forgejo.org/modules/gitrepo"
	"forgejo.org/modules/setting"
	api "forgejo.org/modules/structs"
	"forgejo.org/services/forms"
	release_service "forgejo.org/services/release"
	files_service "forgejo.org/services/repository/files"
	"forgejo.org/tests"
	"forgejo.org/tests/forgery"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPIReleaseList(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	token := getUserToken(t, user2.LowerName, auth_model.AccessTokenScopeReadRepository)

	link, _ := url.Parse(fmt.Sprintf("/api/v1/repos/%s/%s/releases", user2.Name, repo.Name))
	resp := MakeRequest(t, NewRequest(t, "GET", link.String()).AddTokenAuth(token), http.StatusOK)
	var apiReleases []*api.Release
	DecodeJSON(t, resp, &apiReleases)
	if assert.Len(t, apiReleases, 3) {
		for _, release := range apiReleases {
			switch release.ID {
			case 1:
				assert.False(t, release.IsDraft)
				assert.False(t, release.IsPrerelease)
				assert.True(t, strings.HasSuffix(release.UploadURL, "/api/v1/repos/user2/repo1/releases/1/assets"), release.UploadURL)
			case 4:
				assert.True(t, release.IsDraft)
				assert.False(t, release.IsPrerelease)
				assert.True(t, strings.HasSuffix(release.UploadURL, "/api/v1/repos/user2/repo1/releases/4/assets"), release.UploadURL)
			case 5:
				assert.False(t, release.IsDraft)
				assert.True(t, release.IsPrerelease)
				assert.True(t, strings.HasSuffix(release.UploadURL, "/api/v1/repos/user2/repo1/releases/5/assets"), release.UploadURL)
			default:
				require.NoError(t, fmt.Errorf("unexpected release: %v", release))
			}
		}
	}

	// test filter
	testFilterByLen := func(auth bool, query url.Values, expectedLength int, msgAndArgs ...string) {
		link.RawQuery = query.Encode()
		req := NewRequest(t, "GET", link.String())
		if auth {
			req.AddTokenAuth(token)
		}
		resp = MakeRequest(t, req, http.StatusOK)
		DecodeJSON(t, resp, &apiReleases)
		assert.Len(t, apiReleases, expectedLength, msgAndArgs)
	}

	testFilterByLen(false, url.Values{"draft": {"true"}}, 0, "anon should not see drafts")
	testFilterByLen(true, url.Values{"draft": {"true"}}, 1, "repo owner should see drafts")
	testFilterByLen(true, url.Values{"draft": {"false"}}, 2, "exclude drafts")
	testFilterByLen(true, url.Values{"draft": {"false"}, "pre-release": {"false"}}, 1, "exclude drafts and pre-releases")
	testFilterByLen(true, url.Values{"pre-release": {"true"}}, 1, "only get pre-release")
	testFilterByLen(true, url.Values{"draft": {"true"}, "pre-release": {"true"}}, 0, "there is no pre-release draft")
	testFilterByLen(true, url.Values{"q": {"release"}}, 3, "keyword")
}

func createNewReleaseUsingAPI(t *testing.T, token string, owner *user_model.User, repo *repo_model.Repository, name, target, title, desc string) *api.Release {
	urlStr := fmt.Sprintf("/api/v1/repos/%s/%s/releases", owner.Name, repo.Name)
	req := NewRequestWithJSON(t, "POST", urlStr, &api.CreateReleaseOption{
		TagName:      name,
		Title:        title,
		Note:         desc,
		IsDraft:      false,
		IsPrerelease: false,
		Target:       target,
	}).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusCreated)

	var newRelease api.Release
	DecodeJSON(t, resp, &newRelease)
	rel := &repo_model.Release{
		ID:      newRelease.ID,
		TagName: newRelease.TagName,
		Title:   newRelease.Title,
	}
	unittest.AssertExistsAndLoadBean(t, rel)
	assert.Equal(t, newRelease.Note, rel.Note)

	return &newRelease
}

func TestAPIReleaseCreateAndUpdate(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: repo.OwnerID})
	session := loginUser(t, owner.LowerName)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)

	gitRepo, err := gitrepo.OpenRepository(git.DefaultContext, repo)
	require.NoError(t, err)
	defer gitRepo.Close()

	err = gitRepo.CreateTag("v0.0.1", "master")
	require.NoError(t, err)

	target, err := gitRepo.GetTagCommitID("v0.0.1")
	require.NoError(t, err)

	newRelease := createNewReleaseUsingAPI(t, token, owner, repo, "v0.0.1", target, "v0.0.1", "test")

	urlStr := fmt.Sprintf("/api/v1/repos/%s/%s/releases/%d", owner.Name, repo.Name, newRelease.ID)
	req := NewRequest(t, "GET", urlStr).
		AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)

	var release api.Release
	DecodeJSON(t, resp, &release)

	assert.Equal(t, newRelease.TagName, release.TagName)
	assert.Equal(t, newRelease.Title, release.Title)
	assert.Equal(t, newRelease.Note, release.Note)
	assert.False(t, newRelease.HideArchiveLinks)

	hideArchiveLinks := true

	req = NewRequestWithJSON(t, "PATCH", urlStr, &api.EditReleaseOption{
		TagName:          release.TagName,
		Title:            release.Title,
		Note:             "updated",
		IsDraft:          &release.IsDraft,
		IsPrerelease:     &release.IsPrerelease,
		Target:           release.Target,
		HideArchiveLinks: &hideArchiveLinks,
	}).AddTokenAuth(token)
	resp = MakeRequest(t, req, http.StatusOK)

	DecodeJSON(t, resp, &newRelease)
	rel := &repo_model.Release{
		ID:      newRelease.ID,
		TagName: newRelease.TagName,
		Title:   newRelease.Title,
	}
	unittest.AssertExistsAndLoadBean(t, rel)
	assert.Equal(t, rel.Note, newRelease.Note)
	assert.True(t, newRelease.HideArchiveLinks)
}

func TestAPIReleaseCreateProtectedTag(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 4})
	writer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
	session := loginUser(t, writer.LowerName)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)

	gitRepo, err := gitrepo.OpenRepository(git.DefaultContext, repo)
	require.NoError(t, err)
	defer gitRepo.Close()

	commit, err := gitRepo.GetBranchCommit("master")
	require.NoError(t, err)

	req := NewRequestWithJSON(t, "POST", fmt.Sprintf("/api/v1/repos/%s/%s/releases", repo.OwnerName, repo.Name), &api.CreateReleaseOption{
		TagName:      "v0.0.1",
		Title:        "v0.0.1",
		IsDraft:      false,
		IsPrerelease: false,
		Target:       commit.ID.String(),
	}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusUnprocessableEntity)
}

func TestAPIReleaseCreateToDefaultBranch(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: repo.OwnerID})
	session := loginUser(t, owner.LowerName)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)

	createNewReleaseUsingAPI(t, token, owner, repo, "v0.0.1", "", "v0.0.1", "test")
}

func TestAPIReleaseCreateToDefaultBranchOnExistingTag(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: repo.OwnerID})
	session := loginUser(t, owner.LowerName)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)

	gitRepo, err := gitrepo.OpenRepository(git.DefaultContext, repo)
	require.NoError(t, err)
	defer gitRepo.Close()

	err = gitRepo.CreateTag("v0.0.1", "master")
	require.NoError(t, err)

	createNewReleaseUsingAPI(t, token, owner, repo, "v0.0.1", "", "v0.0.1", "test")
}

func TestAPIReleaseCreateGivenInvalidTarget(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: repo.OwnerID})
	session := loginUser(t, owner.LowerName)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)

	urlStr := fmt.Sprintf("/api/v1/repos/%s/%s/releases", owner.Name, repo.Name)
	req := NewRequestWithJSON(t, "POST", urlStr, &api.CreateReleaseOption{
		TagName: "i-point-to-an-invalid-target",
		Title:   "Invalid Target",
		Target:  "invalid-target",
	}).AddTokenAuth(token)

	MakeRequest(t, req, http.StatusNotFound)
}

func TestAPIReleaseGetLatest(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: repo.OwnerID})

	req := NewRequest(t, "GET", fmt.Sprintf("/api/v1/repos/%s/%s/releases/latest", owner.Name, repo.Name))
	resp := MakeRequest(t, req, http.StatusOK)

	var release *api.Release
	DecodeJSON(t, resp, &release)

	assert.Equal(t, "testing-release", release.Title)
}

func TestAPIReleaseGetByTag(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: repo.OwnerID})

	tag := "v1.1"

	req := NewRequest(t, "GET", fmt.Sprintf("/api/v1/repos/%s/%s/releases/tags/%s", owner.Name, repo.Name, tag))
	resp := MakeRequest(t, req, http.StatusOK)

	var release *api.Release
	DecodeJSON(t, resp, &release)

	assert.Equal(t, "testing-release", release.Title)

	nonexistingtag := "nonexistingtag"

	req = NewRequest(t, "GET", fmt.Sprintf("/api/v1/repos/%s/%s/releases/tags/%s", owner.Name, repo.Name, nonexistingtag))
	resp = MakeRequest(t, req, http.StatusNotFound)
	var err *api.APIError
	DecodeJSON(t, resp, &err)
	assert.NotEmpty(t, err.Message)
}

func TestAPIReleaseGetDraftByTag(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	rel := unittest.AssertExistsAndLoadBean(t, &repo_model.Release{
		RepoID:  repo.ID,
		TagName: "draft-release",
	})
	assert.True(t, rel.IsDraft)

	req := NewRequest(t, "GET", fmt.Sprintf("/api/v1/repos/%s/%s/releases/tags/%s", repo.OwnerName, repo.Name, rel.TagName))
	resp := MakeRequest(t, req, http.StatusNotFound)
	var err *api.APIError
	DecodeJSON(t, resp, &err)
	assert.NotEmpty(t, err.Message)
}

func TestAPIReleaseGet(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadRepository)

	t.Run("draft release, no permission", func(t *testing.T) {
		rel := unittest.AssertExistsAndLoadBean(t, &repo_model.Release{
			RepoID:  repo.ID,
			TagName: "draft-release",
		})
		assert.True(t, rel.IsDraft)
		assert.False(t, rel.IsTag) // wouldn't test the CanWrite(TypeReleases) check for the draft, if this were the case

		req := NewRequest(t, "GET", fmt.Sprintf("/api/v1/repos/%s/%s/releases/%d", repo.OwnerName, repo.Name, rel.ID))
		resp := MakeRequest(t, req, http.StatusNotFound)
		var err *api.APIError
		DecodeJSON(t, resp, &err)
		assert.NotEmpty(t, err.Message)
	})

	t.Run("draft release, w/ permission", func(t *testing.T) {
		rel := unittest.AssertExistsAndLoadBean(t, &repo_model.Release{
			RepoID:  repo.ID,
			TagName: "draft-release",
		})
		req := NewRequest(t, "GET", fmt.Sprintf("/api/v1/repos/%s/%s/releases/%d", repo.OwnerName, repo.Name, rel.ID)).
			AddTokenAuth(token)
		resp := MakeRequest(t, req, http.StatusOK)
		var apiRelease *api.Release
		DecodeJSON(t, resp, &apiRelease)
		assert.Equal(t, rel.TagName, apiRelease.TagName)
	})

	t.Run("published release", func(t *testing.T) {
		rel := unittest.AssertExistsAndLoadBean(t, &repo_model.Release{
			RepoID:  repo.ID,
			TagName: "v1.1",
		})
		assert.False(t, rel.IsDraft)
		req := NewRequest(t, "GET", fmt.Sprintf("/api/v1/repos/%s/%s/releases/%d", repo.OwnerName, repo.Name, rel.ID))
		resp := MakeRequest(t, req, http.StatusOK)
		var apiRelease *api.Release
		DecodeJSON(t, resp, &apiRelease)
		assert.Equal(t, rel.TagName, apiRelease.TagName)
	})
}

func TestAPIReleaseGetAssets(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})

	session := loginUser(t, "user2")
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadRepository)

	t.Run("draft release, no permission", func(t *testing.T) {
		rel := unittest.AssertExistsAndLoadBean(t, &repo_model.Release{
			RepoID:  repo.ID,
			TagName: "draft-release",
		})
		assert.True(t, rel.IsDraft)
		assert.False(t, rel.IsTag) // wouldn't test the CanWrite(TypeReleases) check for the draft, if this were the case

		req := NewRequest(t, "GET", fmt.Sprintf("/api/v1/repos/%s/%s/releases/%d/assets", repo.OwnerName, repo.Name, rel.ID))
		resp := MakeRequest(t, req, http.StatusNotFound)
		var err *api.APIError
		DecodeJSON(t, resp, &err)
		assert.NotEmpty(t, err.Message)
	})

	t.Run("draft release, w/ permission", func(t *testing.T) {
		rel := unittest.AssertExistsAndLoadBean(t, &repo_model.Release{
			RepoID:  repo.ID,
			TagName: "draft-release",
		})
		req := NewRequest(t, "GET", fmt.Sprintf("/api/v1/repos/%s/%s/releases/%d/assets", repo.OwnerName, repo.Name, rel.ID)).
			AddTokenAuth(token)
		resp := MakeRequest(t, req, http.StatusOK)
		var attach []*api.Attachment
		DecodeJSON(t, resp, &attach)
		assert.Empty(t, attach)
	})

	t.Run("published release", func(t *testing.T) {
		rel := unittest.AssertExistsAndLoadBean(t, &repo_model.Release{
			RepoID:  repo.ID,
			TagName: "v1.1",
		})
		assert.False(t, rel.IsDraft)
		req := NewRequest(t, "GET", fmt.Sprintf("/api/v1/repos/%s/%s/releases/%d/assets", repo.OwnerName, repo.Name, rel.ID))
		resp := MakeRequest(t, req, http.StatusOK)
		var attach []*api.Attachment
		DecodeJSON(t, resp, &attach)
		assert.Len(t, attach, 1)
	})
}

func TestAPIReleaseDraftAttachmentUnauthorizedAccess(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: repo.OwnerID})
	ownerSession := loginUser(t, owner.LowerName)
	ownerToken := getTokenForLoggedInUser(t, ownerSession, auth_model.AccessTokenScopeWriteRepository)

	rel := unittest.AssertExistsAndLoadBean(t, &repo_model.Release{
		RepoID:  repo.ID,
		TagName: "draft-release",
	})
	assert.True(t, rel.IsDraft)

	// sanity checks: the draft release itself and its attachment list are not
	// visible to anonymous users (only the repository read permission is held)
	req := NewRequest(t, "GET", fmt.Sprintf("/api/v1/repos/%s/%s/releases/%d", owner.Name, repo.Name, rel.ID))
	MakeRequest(t, req, http.StatusNotFound)
	req = NewRequest(t, "GET", fmt.Sprintf("/api/v1/repos/%s/%s/releases/%d/assets", owner.Name, repo.Name, rel.ID))
	MakeRequest(t, req, http.StatusNotFound)

	// the owner uploads an attachment to the draft release
	filename := "draft-secret.png"
	buff := generateImg()
	body := &bytes.Buffer{}
	contentType := tests.WriteImageBody(t, buff, filename, body)

	assetURL := fmt.Sprintf("/api/v1/repos/%s/%s/releases/%d/assets", owner.Name, repo.Name, rel.ID)
	req = NewRequestWithBody(t, http.MethodPost, assetURL, bytes.NewReader(body.Bytes())).
		AddTokenAuth(ownerToken).
		SetHeader("Content-Type", contentType)
	resp := MakeRequest(t, req, http.StatusCreated)
	var attachment *api.Attachment
	DecodeJSON(t, resp, &attachment)
	require.NotNil(t, attachment)

	// attachments of a draft release must not be accessible without write
	// permission on the releases unit: the attachment metadata ...
	req = NewRequest(t, "GET", fmt.Sprintf("/api/v1/repos/%s/%s/releases/%d/assets/%d", owner.Name, repo.Name, rel.ID, attachment.ID))
	MakeRequest(t, req, http.StatusNotFound)

	// ... nor the attachment content
	req = NewRequest(t, "GET", "/attachments/"+attachment.UUID)
	MakeRequest(t, req, http.StatusNotFound)
}

func TestAPIReleaseDeleteByTagName(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: repo.OwnerID})
	session := loginUser(t, owner.LowerName)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)

	createNewReleaseUsingAPI(t, token, owner, repo, "release-tag", "", "Release Tag", "test")

	// delete release
	req := NewRequestf(t, http.MethodDelete, "/api/v1/repos/%s/%s/releases/tags/release-tag", owner.Name, repo.Name).
		AddTokenAuth(token)
	_ = MakeRequest(t, req, http.StatusNoContent)

	// make sure release is deleted
	req = NewRequestf(t, http.MethodDelete, "/api/v1/repos/%s/%s/releases/tags/release-tag", owner.Name, repo.Name).
		AddTokenAuth(token)
	_ = MakeRequest(t, req, http.StatusNotFound)

	// delete release tag too
	req = NewRequestf(t, http.MethodDelete, "/api/v1/repos/%s/%s/tags/release-tag", owner.Name, repo.Name).
		AddTokenAuth(token)
	_ = MakeRequest(t, req, http.StatusNoContent)
}

func TestAPIReleaseUploadAsset(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: repo.OwnerID})
	session := loginUser(t, owner.LowerName)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)

	r := createNewReleaseUsingAPI(t, token, owner, repo, "release-tag", "", "Release Tag", "test")

	filename := "image.png"
	buff := generateImg()

	assetURL := fmt.Sprintf("/api/v1/repos/%s/%s/releases/%d/assets", owner.Name, repo.Name, r.ID)

	t.Run("multipart/form-data", func(t *testing.T) {
		defer tests.PrintCurrentTest(t)()

		body := &bytes.Buffer{}
		contentType := tests.WriteImageBody(t, buff, filename, body)

		req := NewRequestWithBody(t, http.MethodPost, assetURL, bytes.NewReader(body.Bytes())).
			AddTokenAuth(token).
			SetHeader("Content-Type", contentType)
		resp := MakeRequest(t, req, http.StatusCreated)

		var attachment *api.Attachment
		DecodeJSON(t, resp, &attachment)

		assert.Equal(t, filename, attachment.Name)
		assert.EqualValues(t, 101, attachment.Size)

		req = NewRequestWithBody(t, http.MethodPost, assetURL+"?name=test-asset", bytes.NewReader(body.Bytes())).
			AddTokenAuth(token).
			SetHeader("Content-Type", contentType)
		resp = MakeRequest(t, req, http.StatusCreated)

		var attachment2 *api.Attachment
		DecodeJSON(t, resp, &attachment2)

		assert.Equal(t, "test-asset", attachment2.Name)
		assert.EqualValues(t, 101, attachment2.Size)
	})

	t.Run("application/octet-stream", func(t *testing.T) {
		defer tests.PrintCurrentTest(t)()

		req := NewRequestWithBody(t, http.MethodPost, assetURL, bytes.NewReader(buff.Bytes())).
			AddTokenAuth(token)
		MakeRequest(t, req, http.StatusBadRequest)

		req = NewRequestWithBody(t, http.MethodPost, assetURL+"?name=stream.bin", bytes.NewReader(buff.Bytes())).
			AddTokenAuth(token)
		resp := MakeRequest(t, req, http.StatusCreated)

		var attachment *api.Attachment
		DecodeJSON(t, resp, &attachment)

		assert.Equal(t, "stream.bin", attachment.Name)
		assert.EqualValues(t, 101, attachment.Size)
		assert.Equal(t, "attachment", attachment.Type)
	})
}

func TestAPIReleaseGetArchiveDownloadCount(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: repo.OwnerID})
	session := loginUser(t, owner.LowerName)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)

	name := "ReleaseDownloadCount"

	createNewReleaseUsingAPI(t, token, owner, repo, name, "", name, "test")

	urlStr := fmt.Sprintf("/api/v1/repos/%s/%s/releases/tags/%s", owner.Name, repo.Name, name)

	req := NewRequest(t, "GET", urlStr)
	resp := MakeRequest(t, req, http.StatusOK)

	var release *api.Release
	DecodeJSON(t, resp, &release)

	// Check if everything defaults to 0
	assert.Equal(t, int64(0), release.ArchiveDownloadCount.TarGz)
	assert.Equal(t, int64(0), release.ArchiveDownloadCount.Zip)

	// Download the tarball to increase the count
	MakeRequest(t, NewRequest(t, "GET", release.TarURL), http.StatusOK)

	// Check if the count has increased
	resp = MakeRequest(t, req, http.StatusOK)

	DecodeJSON(t, resp, &release)

	assert.Equal(t, int64(1), release.ArchiveDownloadCount.TarGz)
	assert.Equal(t, int64(0), release.ArchiveDownloadCount.Zip)
}

func TestAPIReleaseExternalAsset(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: repo.OwnerID})
	session := loginUser(t, owner.LowerName)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)

	r := createNewReleaseUsingAPI(t, token, owner, repo, "release-tag", "", "Release Tag", "test")

	req := NewRequest(t, http.MethodPost, fmt.Sprintf("/api/v1/repos/%s/%s/releases/%d/assets?name=test-asset&external_url=https%%3A%%2F%%2Fforgejo.org%%2F", owner.Name, repo.Name, r.ID)).
		AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusCreated)

	var attachment *api.Attachment
	DecodeJSON(t, resp, &attachment)

	assert.Equal(t, "test-asset", attachment.Name)
	assert.EqualValues(t, 0, attachment.Size)
	assert.Equal(t, "https://forgejo.org/", attachment.DownloadURL)
	assert.Equal(t, "external", attachment.Type)
}

func TestAPIReleaseAllowedAPIURL(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: repo.OwnerID})
	session := loginUser(t, owner.LowerName)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)

	r := createNewReleaseUsingAPI(t, token, owner, repo, "release-tag", "", "Release Tag", "test")
	internalURL := "https://localhost:3003/api/packages/owner/generic/test/1.0.0/test.txt"

	req := NewRequest(t, http.MethodPost, fmt.Sprintf("/api/v1/repos/%s/%s/releases/%d/assets?name=test-asset&external_url=%s", owner.Name, repo.Name, r.ID, url.QueryEscape(internalURL))).
		AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusCreated)

	var attachment *api.Attachment
	DecodeJSON(t, resp, &attachment)

	assert.Equal(t, "test-asset", attachment.Name)
	assert.EqualValues(t, 0, attachment.Size)
	assert.Equal(t, internalURL, attachment.DownloadURL)
	assert.Equal(t, "external", attachment.Type)
}

func TestAPIReleaseDuplicateAsset(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: repo.OwnerID})
	session := loginUser(t, owner.LowerName)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)

	r := createNewReleaseUsingAPI(t, token, owner, repo, "release-tag", "", "Release Tag", "test")

	filename := "image.png"
	buff := generateImg()
	body := &bytes.Buffer{}
	contentType := tests.WriteImageBody(t, buff, filename, body)

	req := NewRequestWithBody(t, http.MethodPost, fmt.Sprintf("/api/v1/repos/%s/%s/releases/%d/assets?name=test-asset&external_url=https%%3A%%2F%%2Fforgejo.org%%2F", owner.Name, repo.Name, r.ID), body).
		AddTokenAuth(token)
	req.Header.Add("Content-Type", contentType)
	MakeRequest(t, req, http.StatusBadRequest)
}

func TestAPIReleaseMissingAsset(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: repo.OwnerID})
	session := loginUser(t, owner.LowerName)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)

	r := createNewReleaseUsingAPI(t, token, owner, repo, "release-tag", "", "Release Tag", "test")

	req := NewRequest(t, http.MethodPost, fmt.Sprintf("/api/v1/repos/%s/%s/releases/%d/assets?name=test-asset", owner.Name, repo.Name, r.ID)).
		AddTokenAuth(token)
	MakeRequest(t, req, http.StatusBadRequest)
}

func TestAPIReleaseMissingTitle(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: repo.OwnerID})
	session := loginUser(t, owner.LowerName)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)

	gitRepo, err := gitrepo.OpenRepository(git.DefaultContext, repo)
	require.NoError(t, err)
	defer gitRepo.Close()

	err = gitRepo.CreateTag("v0.0.1", "master")
	require.NoError(t, err)

	target, err := gitRepo.GetTagCommitID("v0.0.1")
	require.NoError(t, err)

	r := createNewReleaseUsingAPI(t, token, owner, repo, target, "", "", "")
	assert.Equal(t, r.Title, target)
}

func TestAPIReleaseGithubFormat(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	token := getUserToken(t, user2.LowerName, auth_model.AccessTokenScopeReadRepository)

	req := NewRequest(t, "GET", fmt.Sprintf("/api/v1/repos/%s/%s/releases/1", user2.Name, repo.Name)).AddTokenAuth(token)
	req.Header.Add("Accept", "application/vnd.github+json")
	resp := MakeRequest(t, req, http.StatusOK)

	var apiRelease *api.Release
	DecodeJSON(t, resp, &apiRelease)

	assert.True(t, strings.HasSuffix(apiRelease.UploadURL, "/api/v1/repos/user2/repo1/releases/1/assets{?name,label}"), apiRelease.UploadURL)
}

func TestAPIGenerateReleaseNotes(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, _ *url.URL) {
		owner := forgery.CreateUser(t, nil)
		org := forgery.CreateOrganisation(t, owner)
		repo := forgery.CreateRepository(t, org.AsUser(), &forgery.CreateRepositoryOptions{Files: forgery.FilesInit{}})
		ctx := NewAPITestContext(t, owner.Name, repo.Name, auth_model.AccessTokenScopeWriteRepository)
		link := fmt.Sprintf("/api/v1/repos/%s/%s/releases/generate-notes", org.Name, repo.Name)

		commitFile := func(branch, newBranch, path, content string) {
			_, err := files_service.ChangeRepoFiles(t.Context(), repo, owner, &files_service.ChangeRepoFilesOptions{
				Files:     []*files_service.ChangeRepoFile{{Operation: "create", TreePath: path, ContentReader: strings.NewReader(content)}},
				OldBranch: branch,
				NewBranch: newBranch,
			})
			require.NoError(t, err)
		}
		mergePR := func(branch string, style repo_model.MergeStyle) int64 {
			commitFile("main", branch, branch+".txt", branch)
			pr, err := doAPICreatePullRequest(ctx, org.Name, repo.Name, "main", branch)(t)
			require.NoError(t, err)
			doAPIMergePullRequestForm(t, ctx, org.Name, repo.Name, pr.Index, &forms.MergePullRequestForm{Do: string(style)})
			return pr.Index
		}
		tag := func(name, msg string) {
			require.NoError(t, release_service.CreateNewTag(t.Context(), owner, repo, "main", name, msg))
		}
		generate := func(token string, opts api.GenerateReleaseNotesOption, status int) string {
			resp := MakeRequest(t, NewRequestWithJSON(t, "POST", link, opts).AddTokenAuth(token), status)
			if status != http.StatusOK {
				return resp.Body.String()
			}
			var notes api.GeneratedReleaseNotes
			DecodeJSON(t, resp, &notes)
			assert.Equal(t, opts.TagName, notes.Name)
			return notes.Body
		}
		line := func(index int64) string {
			return fmt.Sprintf("by @%s in #%d\n", owner.Name, index)
		}

		pr1 := mergePR("b1", repo_model.MergeStyleMerge)
		tag("v1", "")
		pr2 := mergePR("b2", repo_model.MergeStyleSquash)
		pr3 := mergePR("b3", repo_model.MergeStyleRebase)
		tag("v2", "annotated")
		pr4 := mergePR("b4", repo_model.MergeStyleMerge)

		t.Run("between tags", func(t *testing.T) {
			body := generate(ctx.Token, api.GenerateReleaseNotesOption{TagName: "v2"}, http.StatusOK)
			assert.NotContains(t, body, line(pr1))
			assert.Contains(t, body, line(pr2))
			assert.Contains(t, body, line(pr3))
			assert.NotContains(t, body, line(pr4))
			assert.Contains(t, body, fmt.Sprintf("**Full Changelog**: %s%s/%s/compare/v1...v2\n", setting.AppURL, org.Name, repo.Name))
		})

		t.Run("first release", func(t *testing.T) {
			body := generate(ctx.Token, api.GenerateReleaseNotesOption{TagName: "v1"}, http.StatusOK)
			assert.Contains(t, body, line(pr1))
			assert.NotContains(t, body, line(pr2))
			assert.Contains(t, body, "/commits/tag/v1\n")
		})

		t.Run("new tag and explicit previous tag", func(t *testing.T) {
			body := generate(ctx.Token, api.GenerateReleaseNotesOption{TagName: "v3"}, http.StatusOK)
			assert.Equal(t, []bool{false, false, false, true}, []bool{strings.Contains(body, line(pr1)), strings.Contains(body, line(pr2)), strings.Contains(body, line(pr3)), strings.Contains(body, line(pr4))})

			body = generate(ctx.Token, api.GenerateReleaseNotesOption{TagName: "v3", Target: "main", PreviousTagName: "v1"}, http.StatusOK)
			assert.Equal(t, []bool{false, true, true, true}, []bool{strings.Contains(body, line(pr1)), strings.Contains(body, line(pr2)), strings.Contains(body, line(pr3)), strings.Contains(body, line(pr4))})
			assert.Contains(t, body, "/compare/v1...v3\n")
		})

		t.Run("config", func(t *testing.T) {
			commitFile("main", "", ".github/release.yml", "changelog:\n  categories:\n    - title: GitHub\n      labels: [\"*\"]\n")
			commitFile("main", "", "custom.yml", "changelog:\n  categories:\n    - title: Custom\n      labels: [\"*\"]\n")
			commitFile("main", "", "broken.yml", "changelog: [secret-detail\n")

			body := generate(ctx.Token, api.GenerateReleaseNotesOption{TagName: "v3"}, http.StatusOK)
			assert.Contains(t, body, "### GitHub\n* ")
			body = generate(ctx.Token, api.GenerateReleaseNotesOption{TagName: "v3", ConfigurationFilePath: "custom.yml"}, http.StatusOK)
			assert.Contains(t, body, "### Custom\n* ")

			body = generate(ctx.Token, api.GenerateReleaseNotesOption{TagName: "v3", ConfigurationFilePath: "missing.yml"}, http.StatusUnprocessableEntity)
			assert.Contains(t, body, "release notes configuration file not found")
			body = generate(ctx.Token, api.GenerateReleaseNotesOption{TagName: "v3", ConfigurationFilePath: "broken.yml"}, http.StatusUnprocessableEntity)
			assert.Contains(t, body, "invalid release notes configuration")
			assert.NotContains(t, body, "secret-detail")
			assert.NotContains(t, body, "yaml")
		})

		t.Run("validation", func(t *testing.T) {
			generate(ctx.Token, api.GenerateReleaseNotesOption{TagName: "bad..tag"}, http.StatusUnprocessableEntity)
			generate(ctx.Token, api.GenerateReleaseNotesOption{TagName: "v3", PreviousTagName: "--exec"}, http.StatusNotFound)
			generate(ctx.Token, api.GenerateReleaseNotesOption{TagName: "v3", PreviousTagName: "missing"}, http.StatusNotFound)
			generate(ctx.Token, api.GenerateReleaseNotesOption{TagName: "v3", Target: "missing"}, http.StatusNotFound)
		})

		t.Run("permissions", func(t *testing.T) {
			MakeRequest(t, NewRequestWithJSON(t, "POST", link, api.GenerateReleaseNotesOption{TagName: "v2"}), http.StatusUnauthorized)

			reader := forgery.CreateUser(t, nil)
			forgery.CreateTeam(t, org, &forgery.CreateTeamOptions{Mode: perm.AccessModeRead, Members: []*user_model.User{reader}})
			generate(getUserToken(t, reader.Name, auth_model.AccessTokenScopeWriteRepository), api.GenerateReleaseNotesOption{TagName: "v2"}, http.StatusForbidden)

			setUnits := func(name string, units map[unit.Type]perm.AccessMode) *user_model.User {
				user := forgery.CreateUser(t, nil)
				team := forgery.CreateTeam(t, org, &forgery.CreateTeamOptions{Name: name, Mode: perm.AccessModeWrite, Members: []*user_model.User{user}})
				for tp, mode := range units {
					_, err := db.GetEngine(t.Context()).Where("team_id = ? AND `type` = ?", team.ID, tp).Cols("access_mode").Update(&org_model.TeamUnit{AccessMode: mode})
					require.NoError(t, err)
				}
				return user
			}
			noCode := setUnits("no-code", map[unit.Type]perm.AccessMode{unit.TypeCode: perm.AccessModeNone})
			generate(getUserToken(t, noCode.Name, auth_model.AccessTokenScopeWriteRepository), api.GenerateReleaseNotesOption{TagName: "v2"}, http.StatusForbidden)
			loginUser(t, noCode.Name).MakeRequest(t, NewRequestWithValues(t, "POST", fmt.Sprintf("/%s/%s/releases/generate-notes", org.Name, repo.Name), map[string]string{"tag_name": "v2"}), http.StatusNotFound)

			noPulls := setUnits("no-pulls", map[unit.Type]perm.AccessMode{unit.TypePullRequests: perm.AccessModeNone})
			generate(getUserToken(t, noPulls.Name, auth_model.AccessTokenScopeWriteRepository), api.GenerateReleaseNotesOption{TagName: "v2"}, http.StatusForbidden)
		})

		t.Run("web", func(t *testing.T) {
			session := loginUser(t, owner.Name)
			webLink := fmt.Sprintf("/%s/%s/releases/generate-notes", org.Name, repo.Name)
			resp := session.MakeRequest(t, NewRequestWithValues(t, "POST", webLink, map[string]string{"tag_name": "v2"}), http.StatusOK)
			var notes struct{ Body string }
			DecodeJSON(t, resp, &notes)
			assert.Contains(t, notes.Body, line(pr2))
			assert.NotContains(t, notes.Body, line(pr4))

			session.MakeRequest(t, NewRequestWithValues(t, "POST", webLink, map[string]string{"tag_name": "bad..tag"}), http.StatusBadRequest)
			session.MakeRequest(t, NewRequestWithValues(t, "POST", webLink, map[string]string{"tag_name": "v3", "previous_tag_name": "missing"}), http.StatusBadRequest)
		})
	})
}
