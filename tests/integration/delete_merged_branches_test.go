// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"net/http"
	"net/url"
	"path"
	"testing"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	repo_service "forgejo.org/services/repository"
	"forgejo.org/tests"

	"github.com/PuerkitoBio/goquery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dmRepo creates org3/<name> with a branch "merged-old" whose tip is behind master, and returns the repository.
func dmRepo(t *testing.T, s *TestSession, name string) *repo_model.Repository {
	t.Helper()
	bwRepo(t, name)
	testAPICreateBranch(t, s, "org3", name, "master", "merged-old", http.StatusCreated)
	testEditFile(t, s, "org3", name, "master", "README.md", "advanced\n")
	return unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{OwnerName: "org3", LowerName: name})
}

func dmBranches(t *testing.T, repo *repo_model.Repository) []string {
	t.Helper()
	gitRepo, err := git.OpenRepository(t.Context(), repo.RepoPath())
	require.NoError(t, err)
	defer gitRepo.Close()
	names, _, err := gitRepo.GetBranchNames(0, 0)
	require.NoError(t, err)
	return names
}

func TestDeleteMergedBranches(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, _ *url.URL) {
		defer tests.PrintCurrentTest(t)()
		s := loginUser(t, "user2")
		org := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "org3"})

		// one repository: only "merged-old" goes
		repo := dmRepo(t, s, "dm-repo")
		for _, b := range []string{"prot-merged", "pr-base"} {
			testAPICreateBranch(t, s, "org3", repo.Name, "merged-old", b, http.StatusCreated)
		}
		testAPICreateBranch(t, s, "org3", repo.Name, "master", "at-tip", http.StatusCreated)
		testNewFileToNewBranch(t, s, "org3", repo.Name, "merged-old", "unmerged", "unmerged.txt", "unmerged\n")
		testPullCreate(t, s, "org3", repo.Name, true, "pr-base", "unmerged", "PR into a merged branch")
		ctx := NewAPITestContext(t, "user2", repo.Name, auth_model.AccessTokenScopeWriteRepository)
		bwProtect(t, ctx, repo.Name, "prot-merged", parameterProtectBranch{"enable_push": "true"})

		link := path.Join("/org3", repo.Name, "settings/branch_workflow/delete_merged")
		resp := s.MakeRequest(t, NewRequest(t, "GET", link), http.StatusOK)
		labels := NewHTMLParser(t, resp.Body).doc.Find("#delete-merged + .segment .label")
		assert.Equal(t, []string{"merged-old"}, labels.Map(func(_ int, l *goquery.Selection) string { return l.Text() }))
		assert.ElementsMatch(t, []string{"master", "merged-old", "prot-merged", "pr-base", "at-tip", "unmerged"}, dmBranches(t, repo))

		other := loginUser(t, "user4")
		other.MakeRequest(t, NewRequest(t, "GET", link), http.StatusNotFound)
		other.MakeRequest(t, NewRequest(t, "POST", link), http.StatusNotFound)

		since := auditMaxID(t)
		s.MakeRequest(t, NewRequest(t, "POST", link), http.StatusSeeOther)
		assert.Contains(t, bwFlash(s), "Deleted 1 merged branches.")
		assert.ElementsMatch(t, []string{"master", "prot-merged", "pr-base", "at-tip", "unmerged"}, dmBranches(t, repo))
		e := repoOne(t, since, "repo.merged_branches.deleted", repo)
		assert.Equal(t, map[string]any{"deleted": float64(1), "failed": float64(0), "repos": float64(1)}, auditData(t, e)["counts"])
		assert.Len(t, auditActions(t, since, "repo.branch.deleted"), 1)
		unittest.AssertExistsAndLoadBean(t, &git_model.Branch{RepoID: repo.ID, Name: "merged-old", IsDeleted: true}) // restorable

		// a project: only its repositories
		inProject := dmRepo(t, s, "dm-project")
		outside := dmRepo(t, s, "dm-org")
		archived := dmRepo(t, s, "dm-archived")
		_, err := db.GetEngine(t.Context()).Exec("UPDATE repository SET is_archived = ? WHERE id = ?", true, archived.ID)
		require.NoError(t, err)
		group, err := repo_model.CreateRepoGroup(t.Context(), org, "dm-group", "")
		require.NoError(t, err)
		require.NoError(t, repo_service.MoveRepository(t.Context(), unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2}), inProject, group.ID))
		inProject = unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: inProject.ID})

		since = auditMaxID(t)
		s.MakeRequest(t, NewRequest(t, "POST", group.Link("org3")+"/-/settings/delete_merged"), http.StatusSeeOther)
		assert.Equal(t, []string{"master"}, dmBranches(t, inProject))
		assert.Contains(t, dmBranches(t, outside), "merged-old")
		e = orgOne(t, since, "org.project.merged_branches_deleted")
		assert.Equal(t, group.ID, e.GroupID)

		// the organization: every other repository, archived ones skipped
		other.MakeRequest(t, NewRequest(t, "POST", "/org/org3/settings/branch_workflow/delete_merged"), http.StatusNotFound)
		resp = s.MakeRequest(t, NewRequest(t, "GET", "/org/org3/settings/branch_workflow/delete_merged"), http.StatusOK)
		assert.Contains(t, resp.Body.String(), "org3/dm-org")
		assert.NotContains(t, resp.Body.String(), "org3/dm-archived")
		since = auditMaxID(t)
		s.MakeRequest(t, NewRequest(t, "POST", "/org/org3/settings/branch_workflow/delete_merged"), http.StatusSeeOther)
		assert.Equal(t, []string{"master"}, dmBranches(t, outside))
		assert.Contains(t, dmBranches(t, archived), "merged-old")
		assert.ElementsMatch(t, []string{"master", "prot-merged", "pr-base", "at-tip", "unmerged"}, dmBranches(t, repo))
		orgOne(t, since, "org.merged_branches.deleted")
	})
}
