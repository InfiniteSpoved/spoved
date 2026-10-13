// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"forgejo.org/models"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	packages_model "forgejo.org/models/packages"
	"forgejo.org/models/perm"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"
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
	nested := func(name string, isPrivate bool) *repo_model.Repository {
		repo, err := repo_service.CreateRepositoryDirectly(ctx, owner, org.AsUser(), repo_service.CreateRepoOptions{Name: name, IsPrivate: isPrivate, GroupID: group.ID})
		require.NoError(t, err)
		t.Cleanup(func() { _ = repo_service.DeleteRepository(db.DefaultContext, owner, repo, false) })
		return repo
	}
	inProject := nested("pl-nested", true)
	require.NoError(t, models.AddRepository(ctx, team, inProject))
	nestedHidden := nested("pl-nested-hidden", true)
	nestedPublic := nested("pl-nested-public", false)

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

	shownLink := func(session *TestSession) string {
		resp := session.MakeRequest(t, NewRequest(t, "GET", settingsURL), http.StatusOK)
		return NewHTMLParser(t, resp.Body).Find(`input[name="repo_id"]`).AttrOr("value", "")
	}

	writerSession := loginUser(t, writer.Name)
	ownerSession := loginUser(t, owner.Name)

	t.Run("Picker", func(t *testing.T) {
		assert.Equal(t, map[int64]string{teamRepo.ID: teamRepo.Name, inProject.ID: "pl-project/pl-nested"}, picker(writerSession))

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
		assert.Equal(t, strconv.FormatInt(teamRepo.ID, 10), shownLink(writerSession))

		// saving the current link again is a no-op
		_, flash = link(writerSession, teamRepo.ID)
		assert.Contains(t, flash, "Repository link was successfully updated")
		assert.Equal(t, teamRepo.ID, repoID())

		// a package writer who is not an org admin can't change or remove the link, like the API
		_, flash = link(writerSession, inProject.ID)
		assert.Contains(t, flash, "Failed to update repository link")
		_, flash = link(writerSession, 0)
		assert.Contains(t, flash, "Failed to update repository link")
		assert.Equal(t, teamRepo.ID, repoID())

		// a failing link step keeps the old link: repository 1 belongs to another owner
		_, flash = link(ownerSession, 1)
		assert.Contains(t, flash, "Failed to update repository link")
		assert.Equal(t, teamRepo.ID, repoID())

		// a link to a hidden repository is neither shown nor confirmed
		_, flash = link(ownerSession, hidden.ID)
		assert.Contains(t, flash, "Repository link was successfully updated")
		assert.Equal(t, hidden.ID, repoID())
		assert.Equal(t, "0", shownLink(writerSession))
		hiddenLocation, hiddenFlash := link(writerSession, hidden.ID)
		missingLocation, missingFlash := link(writerSession, 1<<40)
		assert.Contains(t, hiddenFlash, "Failed to update repository link")
		assert.Equal(t, hiddenFlash, missingFlash)
		assert.Equal(t, hiddenLocation, missingLocation)
		assert.Equal(t, hidden.ID, repoID())

		_, flash = link(ownerSession, inProject.ID)
		assert.Contains(t, flash, "Repository link was successfully updated")
		assert.Equal(t, inProject.ID, repoID())
		_, flash = link(ownerSession, 0)
		assert.Contains(t, flash, "Repository link was successfully updated")
		assert.Zero(t, repoID())
	})

	t.Run("API", func(t *testing.T) {
		token := getTokenForLoggedInUser(t, writerSession, auth_model.AccessTokenScopeWritePackage)
		apiPost := func(path string, status int) string {
			req := NewRequest(t, "POST", fmt.Sprintf("/api/v1/packages/%s/generic/pl-pkg/-/%s", org.Name, path)).AddTokenAuth(token)
			return MakeRequest(t, req, status).Body.String()
		}
		assert.Equal(t, apiPost("link/pl-missing", http.StatusNotFound), apiPost("link/"+hidden.Name, http.StatusNotFound))
		apiPost("link/"+public.Name, http.StatusForbidden)
		assert.Equal(t, apiPost("link/pl-project/pl-missing", http.StatusNotFound), apiPost("link/pl-project/"+nestedHidden.Name, http.StatusNotFound))
		assert.Equal(t, apiPost("link/pl-project/pl-missing", http.StatusNotFound), apiPost("link/pl-missing", http.StatusNotFound))
		apiPost("link/pl-project/"+nestedPublic.Name, http.StatusForbidden)
		assert.Zero(t, repoID())

		apiPost("link/pl-project/"+inProject.Name, http.StatusCreated)
		assert.Equal(t, inProject.ID, repoID())
		// unlinking needs an org admin
		apiPost("unlink", http.StatusForbidden)
		assert.Equal(t, inProject.ID, repoID())

		require.NoError(t, packages_model.UnlinkRepository(ctx, pkg.ID, repoID()))
		apiPost("link/"+teamRepo.Name, http.StatusCreated)
		assert.Equal(t, teamRepo.ID, repoID())
		require.NoError(t, packages_model.UnlinkRepository(ctx, pkg.ID, repoID()))
	})

	t.Run("Npm", func(t *testing.T) {
		ownRepo := forgery.CreateRepository(t, writer, nil)
		token := "Bearer " + getTokenForLoggedInUser(t, writerSession, auth_model.AccessTokenScopeWritePackage)
		publish := func(version string, repo *repo_model.Repository) {
			body := `{"_id": "pl-npm", "name": "pl-npm", "dist-tags": {"latest": "` + version + `"},
				"versions": {"` + version + `": {"name": "pl-npm", "version": "` + version + `",
					"dist": {"integrity": "sha512-yA4FJsVhetynGfOC1jFf79BuS+jrHbm0fhh+aHzCQkOaOBXKf9oBnC4a6DnLLnEsHQDRLYd00cwj8sCXpC+wIg==", "shasum": "aaa7eaf852a948b0aa05afeda35b1badca155d90"},
					"repository": {"type": "git", "url": "` + setting.AppURL + repo.FullName() + `.git"}}},
				"_attachments": {"pl-npm-` + version + `.tgz": {"data": "H4sIAAAAAAAA/ytITM5OTE/VL4DQelnF+XkMVAYGBgZmJiYK2MRBwNDcSIHB2NTMwNDQzMwAqA7IMDUxA9LUdgg2UFpcklgEdAql5kD8ogCnhwio5lJQUMpLzE1VslJQcihOzi9I1S9JLS7RhSYIJR2QgrLUouLM/DyQGkM9Az1D3YIiqExKanFyUWZBCVQ2BKhVwQVJDKwosbQkI78IJO/tZ+LsbRykxFXLNdA+HwWjYBSMgpENACgAbtAACAAA"}}}`
			req := NewRequestWithBody(t, "PUT", fmt.Sprintf("/api/packages/%s/npm/pl-npm", org.Name), strings.NewReader(body)).AddTokenAuth(token)
			MakeRequest(t, req, http.StatusCreated)
		}
		npmLink := func() int64 {
			p, err := packages_model.GetPackageByName(ctx, org.ID, packages_model.TypeNpm, "pl-npm")
			require.NoError(t, err)
			return p.RepoID
		}

		// a repository of another owner is not linked
		publish("1.0.0", ownRepo)
		assert.Zero(t, npmLink())
		// the first writable repository of the owner is linked
		publish("1.0.1", teamRepo)
		assert.Equal(t, teamRepo.ID, npmLink())
		// a new version doesn't move an existing link
		p, err := packages_model.GetPackageByName(ctx, org.ID, packages_model.TypeNpm, "pl-npm")
		require.NoError(t, err)
		require.NoError(t, packages_model.UnlinkRepository(ctx, p.ID, p.RepoID))
		require.NoError(t, packages_model.SetRepositoryLink(ctx, p.ID, hidden.ID))
		publish("1.0.2", teamRepo)
		assert.Equal(t, hidden.ID, npmLink())
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
