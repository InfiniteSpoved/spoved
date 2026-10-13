// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	packages_model "forgejo.org/models/packages"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/repository"
	api "forgejo.org/modules/structs"
	issue_service "forgejo.org/services/issue"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createRefIssue creates an issue by API and returns it.
func createRefIssue(t *testing.T, token, repoPath, title, body string) *issues_model.Issue {
	t.Helper()
	var issue api.Issue
	DecodeJSON(t, MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/repos/"+repoPath+"/issues", &api.CreateIssueOption{Title: title, Body: body}).AddTokenAuth(token), http.StatusCreated), &issue)
	return unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: issue.ID})
}

func refComment(issue, source *issues_model.Issue) *issues_model.Comment {
	return &issues_model.Comment{IssueID: issue.ID, RefIssueID: source.ID, Type: issues_model.CommentTypeIssueRef}
}

// org3/proj/pub and the root repository org3/pub share the leaf name and their first issue index.
func TestProjectCrossReferences(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	f, rootPub := prepareRepoGroupGit(t)
	token2 := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeWriteIssue, auth_model.AccessTokenScopeWriteRepository)
	token5 := getTokenForLoggedInUser(t, loginUser(t, "user5"), auth_model.AccessTokenScopeWriteIssue, auth_model.AccessTokenScopeWriteRepository)

	inProject := createRefIssue(t, token2, "org3/proj/pub/-", "in project", "")
	inRoot := createRefIssue(t, token2, "org3/pub", "in root", "")
	inPriv := createRefIssue(t, token2, "org3/proj/priv/-", "in private project repo", "")
	require.Equal(t, inProject.Index, inRoot.Index)
	require.Equal(t, rootPub.ID, inRoot.RepoID)
	require.Equal(t, f.pub.ID, inProject.RepoID)

	t.Run("root and project repository are told apart", func(t *testing.T) {
		src := createRefIssue(t, token2, "user2/repo1", "refs", fmt.Sprintf("see org3/proj/pub#%[1]d and also org3/proj/priv#%[1]d", inProject.Index))
		unittest.AssertExistsAndLoadBean(t, refComment(inProject, src))
		unittest.AssertExistsAndLoadBean(t, refComment(inPriv, src))
		unittest.AssertNotExistsBean(t, refComment(inRoot, src))

		src = createRefIssue(t, token2, "user2/repo1", "refs root", fmt.Sprintf("see org3/pub#%d", inRoot.Index))
		unittest.AssertExistsAndLoadBean(t, refComment(inRoot, src))
		unittest.AssertNotExistsBean(t, refComment(inProject, src))

		// four segments and unknown projects match nothing
		src = createRefIssue(t, token2, "user2/repo1", "refs none", "org3/proj/pub/x#1 org3/nope/pub#1 nope/proj/pub#1")
		unittest.AssertNotExistsBean(t, &issues_model.Comment{RefIssueID: src.ID, Type: issues_model.CommentTypeIssueRef})
	})

	t.Run("a reader is required, a hidden repository looks missing", func(t *testing.T) {
		// user5 is no member of org3: the private repository takes no reference from them
		src := createRefIssue(t, token5, "user5/repo4", "from a stranger", fmt.Sprintf("org3/proj/priv#%d org3/proj/pub#%d", inPriv.Index, inProject.Index))
		unittest.AssertNotExistsBean(t, refComment(inPriv, src))
		unittest.AssertExistsAndLoadBean(t, refComment(inProject, src)) // public: readable by everyone
	})

	t.Run("trashed repositories are not resolved", func(t *testing.T) {
		_, err := db.GetEngine(db.DefaultContext).Exec("UPDATE repository SET trashed_unix = 1 WHERE id = ?", f.priv.ID)
		require.NoError(t, err)
		defer func() {
			_, err := db.GetEngine(db.DefaultContext).Exec("UPDATE repository SET trashed_unix = 0 WHERE id = ?", f.priv.ID)
			require.NoError(t, err)
		}()
		src := createRefIssue(t, token2, "user2/repo1", "to trash", fmt.Sprintf("org3/proj/priv#%d", inPriv.Index))
		unittest.AssertNotExistsBean(t, refComment(inPriv, src))
	})
}

func TestProjectCloseKeywords(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	_, rootPub := prepareRepoGroupGit(t)
	token2 := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeWriteIssue)
	inProject := createRefIssue(t, token2, "org3/proj/pub/-", "in project", "")
	inRoot := createRefIssue(t, token2, "org3/pub", "in root", "")
	inPriv := createRefIssue(t, token2, "org3/proj/priv/-", "in private project repo", "")
	require.Equal(t, inProject.Index, inRoot.Index)
	require.Equal(t, rootPub.ID, inRoot.RepoID)

	repo1 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	push := func(doer *user_model.User, message string) {
		t.Helper()
		require.NoError(t, repo1.LoadOwner(db.DefaultContext))
		require.NoError(t, issue_service.UpdateIssuesCommit(db.DefaultContext, doer, repo1, []*repository.PushCommit{{
			Sha1: "abcdef1", CommitterEmail: doer.Email, CommitterName: doer.Name, AuthorEmail: doer.Email, AuthorName: doer.Name, Message: message,
		}}, repo1.DefaultBranch))
	}
	closed := func(issue *issues_model.Issue) bool {
		return unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: issue.ID}).IsClosed
	}

	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	user5 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 5})

	// a stranger can neither close a private project repository's issue nor learn that it exists
	push(user5, fmt.Sprintf("fixes org3/proj/priv#%d", inPriv.Index))
	assert.False(t, closed(inPriv))
	unittest.AssertNotExistsBean(t, &issues_model.Comment{IssueID: inPriv.ID, Type: issues_model.CommentTypeCommitRef})

	// the owner closes exactly the repository named, never the root repository with the same leaf name
	push(user2, fmt.Sprintf("fixes org3/proj/pub#%d", inProject.Index))
	assert.True(t, closed(inProject))
	assert.False(t, closed(inRoot))
	push(user2, fmt.Sprintf("closes org3/pub#%d", inRoot.Index))
	assert.True(t, closed(inRoot))
	push(user2, fmt.Sprintf("closes org3/proj/priv#%d", inPriv.Index))
	assert.True(t, closed(inPriv))
}

func TestAPIIssueDependencyProject(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	prepareRepoGroupGit(t)
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeWriteIssue, auth_model.AccessTokenScopeWriteRepository)
	inProject := createRefIssue(t, token, "org3/proj/pub/-", "in project", "")
	second := createRefIssue(t, token, "org3/proj/pub/-", "second in project", "")
	inRoot := createRefIssue(t, token, "org3/pub", "in root", "")
	require.Equal(t, inProject.Index, inRoot.Index)

	add := func(target *issues_model.Issue, path string, meta api.IssueMeta, status int) {
		t.Helper()
		MakeRequest(t, NewRequestWithJSON(t, "POST", fmt.Sprintf("/api/v1/repos/%s/issues/%d/dependencies", path, target.Index), &meta).AddTokenAuth(token), status)
	}
	depends := func(issue, dependency *issues_model.Issue) bool {
		n, err := db.GetEngine(db.DefaultContext).Table("issue_dependency").Where("issue_id = ? AND dependency_id = ?", issue.ID, dependency.ID).Count()
		require.NoError(t, err)
		return n > 0
	}

	// inside the project: the project form names the same repository, the root form another one
	add(second, "org3/proj/pub/-", api.IssueMeta{Index: inProject.Index, Owner: "org3", Name: "pub", Project: "proj"}, http.StatusCreated)
	assert.True(t, depends(second, inProject))
	add(second, "org3/proj/pub/-", api.IssueMeta{Index: inRoot.Index, Owner: "org3", Name: "pub"}, http.StatusCreated)
	assert.True(t, depends(second, inRoot), "the root repository org3/pub")
	assert.False(t, depends(inProject, inRoot))

	// from the root repository the project one is reached with the project field only
	add(inRoot, "org3/pub", api.IssueMeta{Index: inProject.Index, Owner: "org3", Name: "pub", Project: "proj"}, http.StatusCreated)
	assert.True(t, depends(inRoot, inProject))

	// unknown project, unknown repository: the same answer
	add(second, "org3/proj/pub/-", api.IssueMeta{Index: 1, Owner: "org3", Name: "pub", Project: "nope"}, http.StatusNotFound)
	add(second, "org3/proj/pub/-", api.IssueMeta{Index: 1, Owner: "org3", Name: "nope"}, http.StatusNotFound)
}

func TestProjectComparePullHeads(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	f, _ := prepareRepoGroupGit(t)
	session := loginUser(t, "user2")

	// the head repository of a comparison can be named with its project
	for head, status := range map[string]int{
		"org3/proj/priv:main": http.StatusOK,
		"org3/pub:main":       http.StatusOK,
		"org3/nope/pub:main":  http.StatusNotFound,
		"org3/a/b/c:main":     http.StatusNotFound,
	} {
		session.MakeRequest(t, NewRequest(t, "GET", "/org3/proj/pub/compare/main..."+head), status)
	}
	assert.Equal(t, "org3/proj/pub/compare/a...b", f.pub.ComposeCompareURL("a", "b"))
	assert.Equal(t, f.pub.Link()+"/compare/main...org3/proj/priv:feature", f.priv.ComposeBranchCompareURL(f.pub, "feature"))
}

func TestAPIPackageLinkProject(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	f, _ := prepareRepoGroupGit(t)
	token := getTokenForLoggedInUser(t, loginUser(t, "user2"), auth_model.AccessTokenScopeWritePackage, auth_model.AccessTokenScopeWriteOrganization)

	upload := func(name string) *packages_model.Package {
		MakeRequest(t, NewRequestWithBody(t, "PUT", fmt.Sprintf("/api/packages/org3/generic/%s/1.0/file.bin", name), bytes.NewReader([]byte("content"))).AddBasicAuth("user2"), http.StatusCreated)
		pkg, err := packages_model.GetPackageByName(db.DefaultContext, 3, packages_model.TypeGeneric, name)
		require.NoError(t, err)
		return pkg
	}
	link := func(name, path string, status int) {
		t.Helper()
		MakeRequest(t, NewRequest(t, "POST", fmt.Sprintf("/api/v1/packages/org3/generic/%s/-/link/%s", name, path)).AddTokenAuth(token), status)
	}

	upload("linked")
	link("linked", "proj/nope", http.StatusNotFound) // no such repository in the project
	link("linked", "nope/pub", http.StatusNotFound)  // no such project
	link("linked", "secret/pub", http.StatusNotFound)
	link("linked", "pub", http.StatusCreated) // the root repository org3/pub
	pkg, err := packages_model.GetPackageByName(db.DefaultContext, 3, packages_model.TypeGeneric, "linked")
	require.NoError(t, err)
	root := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: pkg.RepoID})
	assert.Zero(t, root.GroupID)

	upload("nested")
	link("nested", "proj/pub", http.StatusCreated)
	pkg, err = packages_model.GetPackageByName(db.DefaultContext, 3, packages_model.TypeGeneric, "nested")
	require.NoError(t, err)
	assert.Equal(t, f.pub.ID, pkg.RepoID)
}
