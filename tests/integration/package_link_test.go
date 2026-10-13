// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"forgejo.org/models"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	packages_model "forgejo.org/models/packages"
	"forgejo.org/models/perm"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	repo_service "forgejo.org/services/repository"
	"forgejo.org/tests"
	"forgejo.org/tests/forgery"

	"github.com/PuerkitoBio/goquery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A package can only be linked to a repository where the doer can write packages, and the settings page lists only those.
func TestPackageLinkRepositoryAccess(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	ctx := db.DefaultContext
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	admin := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	org := forgery.CreateOrganisation(t, owner)
	writer := forgery.CreateUser(t, nil)
	team := forgery.CreateTeam(t, org, &forgery.CreateTeamOptions{Mode: perm.AccessModeWrite, Members: []*user_model.User{writer}})
	team.IncludesAllRepositories = false
	_, err := db.GetEngine(ctx).ID(team.ID).Cols("includes_all_repositories").Update(team)
	require.NoError(t, err)

	private := func(name string) *repo_model.Repository {
		return forgery.CreateRepository(t, org.AsUser(), &forgery.CreateRepositoryOptions{Name: name, IsPrivate: true})
	}
	teamRepo := private("pl-team")
	require.NoError(t, models.AddRepository(ctx, team, teamRepo))
	hidden := private("pl-hidden")
	public := forgery.CreateRepository(t, org.AsUser(), &forgery.CreateRepositoryOptions{Name: "pl-public"})
	trashed := private("pl-trashed")
	_, err = db.GetEngine(ctx).Exec("UPDATE repository SET trashed_unix = 1 WHERE id = ?", trashed.ID)
	require.NoError(t, err)
	group, err := repo_model.CreateRepoGroup(ctx, org.AsUser(), "pl-project", "")
	require.NoError(t, err)
	inProject, err := repo_service.CreateRepositoryDirectly(ctx, owner, org.AsUser(), repo_service.CreateRepoOptions{Name: "pl-nested", IsPrivate: true, GroupID: group.ID})
	require.NoError(t, err)
	t.Cleanup(func() { _ = repo_service.DeleteRepository(db.DefaultContext, owner, inProject, false) })
	require.NoError(t, models.AddRepository(ctx, team, inProject))

	req := NewRequestWithBody(t, "PUT", fmt.Sprintf("/api/packages/%s/generic/pl-pkg/1.0/file.bin", org.Name), bytes.NewReader([]byte{1})).
		AddBasicAuth(owner.Name)
	MakeRequest(t, req, http.StatusCreated)
	pkg, err := packages_model.GetPackageByName(ctx, org.ID, packages_model.TypeGeneric, "pl-pkg")
	require.NoError(t, err)
	settingsURL := fmt.Sprintf("/%s/-/packages/generic/pl-pkg/1.0/settings", org.Name)

	picker := func(session *TestSession) map[int64]string {
		resp := session.MakeRequest(t, NewRequest(t, "GET", settingsURL), http.StatusOK)
		items := map[int64]string{}
		NewHTMLParser(t, resp.Body).Find(".menu .item[data-value]").Each(func(_ int, s *goquery.Selection) {
			id, err := strconv.ParseInt(s.AttrOr("data-value", ""), 10, 64)
			require.NoError(t, err)
			items[id] = s.Text()
		})
		return items
	}
	repoID := func() int64 {
		return unittest.AssertExistsAndLoadBean(t, &packages_model.Package{ID: pkg.ID}).RepoID
	}
	link := func(session *TestSession, id int64) (location, flash string) {
		resp := session.MakeRequest(t, NewRequestWithValues(t, "POST", settingsURL, map[string]string{
			"action": "link", "repo_id": strconv.FormatInt(id, 10),
		}), http.StatusSeeOther)
		return resp.Header().Get("Location"), bwFlash(session)
	}

	writerSession := loginUser(t, writer.Name)

	t.Run("Picker", func(t *testing.T) {
		assert.Equal(t, map[int64]string{teamRepo.ID: teamRepo.FullName(), inProject.ID: inProject.FullName()}, picker(writerSession))

		all := picker(loginUser(t, admin.Name))
		for _, r := range []*repo_model.Repository{teamRepo, hidden, public, inProject} {
			assert.Contains(t, all, r.ID)
		}
		assert.NotContains(t, all, trashed.ID)
	})

	t.Run("WebHiddenLikeMissing", func(t *testing.T) {
		hiddenLocation, hiddenFlash := link(writerSession, hidden.ID)
		missingLocation, missingFlash := link(writerSession, 1<<40)
		publicLocation, publicFlash := link(writerSession, public.ID)
		assert.Contains(t, hiddenFlash, "Failed to update repository link")
		assert.Equal(t, hiddenFlash, missingFlash)
		assert.Equal(t, hiddenFlash, publicFlash)
		assert.Equal(t, hiddenLocation, missingLocation)
		assert.Equal(t, hiddenLocation, publicLocation)
		assert.Zero(t, repoID())
	})

	t.Run("WebLink", func(t *testing.T) {
		_, flash := link(writerSession, teamRepo.ID)
		assert.Contains(t, flash, "Repository link was successfully updated")
		assert.Equal(t, teamRepo.ID, repoID())

		// changing or removing the link needs the unlink rule (org admin), like the API
		_, flash = link(writerSession, inProject.ID)
		assert.Contains(t, flash, "Failed to update repository link")
		_, flash = link(writerSession, 0)
		assert.Contains(t, flash, "Failed to update repository link")
		assert.Equal(t, teamRepo.ID, repoID())

		ownerSession := loginUser(t, owner.Name)
		_, flash = link(ownerSession, inProject.ID)
		assert.Contains(t, flash, "Repository link was successfully updated")
		assert.Equal(t, inProject.ID, repoID())
		_, flash = link(ownerSession, 0)
		assert.Contains(t, flash, "Repository link was successfully updated")
		assert.Zero(t, repoID())
	})

	t.Run("API", func(t *testing.T) {
		token := getTokenForLoggedInUser(t, writerSession, auth_model.AccessTokenScopeWritePackage)
		apiLink := func(name string, status int) string {
			req := NewRequest(t, "POST", fmt.Sprintf("/api/v1/packages/%s/generic/pl-pkg/-/link/%s", org.Name, name)).AddTokenAuth(token)
			return MakeRequest(t, req, status).Body.String()
		}
		assert.Equal(t, apiLink("pl-missing", http.StatusNotFound), apiLink(hidden.Name, http.StatusNotFound))
		apiLink(public.Name, http.StatusForbidden)
		apiLink(teamRepo.Name, http.StatusCreated)
		assert.Equal(t, teamRepo.ID, repoID())
	})

	t.Run("PickerQueries", func(t *testing.T) {
		before := stableQueryCount(t, func() { picker(writerSession) })
		for i := range 3 {
			require.NoError(t, models.AddRepository(ctx, team, private(fmt.Sprintf("pl-more-%d", i))))
			private(fmt.Sprintf("pl-other-%d", i))
		}
		assert.Len(t, picker(writerSession), 5)
		assert.Equal(t, before, stableQueryCount(t, func() { picker(writerSession) }))
	})
}
