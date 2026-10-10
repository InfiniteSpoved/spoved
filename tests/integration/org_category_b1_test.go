// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"forgejo.org/models/db"
	"forgejo.org/models/organization"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"
	repo_service "forgejo.org/services/repository"
	"forgejo.org/tests"

	"github.com/PuerkitoBio/goquery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exploreRepos returns the "owner/name" of the repositories on an Explore page, and the page.
func exploreRepos(t *testing.T, s *TestSession, query string, status int) ([]string, *goquery.Document) {
	t.Helper()
	req := NewRequest(t, "GET", "/explore/repos"+query)
	if status != http.StatusOK {
		req.SetHeader("Accept", "text/html")
	}
	var doc *goquery.Document
	if s == nil {
		doc = NewHTMLParser(t, MakeRequest(t, req, status).Body).doc
	} else {
		doc = NewHTMLParser(t, s.MakeRequest(t, req, status).Body).doc
	}
	var repos []string
	doc.Find(".page-content .flex-list .flex-item .flex-item-title a.name:last-of-type").Each(func(_ int, sel *goquery.Selection) {
		repos = append(repos, strings.TrimPrefix(sel.AttrOr("href", ""), "/"))
	})
	return repos, doc
}

// AC1-AC6: the Explore repositories category filter.
func TestOrgCategoryExploreRepos(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	defer test.MockVariableValue(&setting.UI.ExplorePagingNum, 1000)()
	admin := loginUser(t, "user1")

	// AC1: without categories nothing changes
	plain, doc := exploreRepos(t, nil, "", http.StatusOK)
	assert.Zero(t, doc.Find("#repo-search-category").Length())
	ignored, _ := exploreRepos(t, nil, "?category=whatever", http.StatusOK)
	assert.Equal(t, plain, ignored)

	a := createOrgCategory(t, "Alpha", 2)
	b := createOrgCategory(t, "Beta", 1)
	setOrgCategory(t, 3, a.ID)
	setOrgCategory(t, 19, b.ID)

	// AC2, AC4: a category lists only its organizations' repositories, within the usual visibility
	repos, _ := exploreRepos(t, nil, fmt.Sprintf("?category=%d", a.ID), http.StatusOK)
	assert.Equal(t, []string{"org3/repo21"}, repos)
	// for a signed-in user: exactly their own list, narrowed to the category
	user4 := loginUser(t, "user4")
	all, _ := exploreRepos(t, user4, "", http.StatusOK)
	repos, _ = exploreRepos(t, user4, fmt.Sprintf("?category=%d", a.ID), http.StatusOK)
	assert.Equal(t, slices.DeleteFunc(all, func(r string) bool { return !strings.HasPrefix(r, "org3/") }), repos)
	assert.NotContains(t, repos, "org3/repo5")
	repos, _ = exploreRepos(t, admin, fmt.Sprintf("?category=%d&sort=alphabetically", a.ID), http.StatusOK)
	assert.Equal(t, []string{"org3/repo21", "org3/repo3", "org3/repo5"}, repos)
	repos, _ = exploreRepos(t, nil, fmt.Sprintf("?category=%d", b.ID), http.StatusOK)
	assert.Equal(t, []string{"org19/big_test_public_mirror_6"}, repos)
	other, _ := exploreRepos(t, nil, "?category=other", http.StatusOK)
	assert.Len(t, other, len(plain)-2)
	assert.Contains(t, other, "user2/repo1")
	assert.NotContains(t, other, "org3/repo21")
	exploreRepos(t, nil, "?category=9999", http.StatusNotFound)
	exploreRepos(t, nil, "?category=abc", http.StatusNotFound)

	// AC3: combines with the topic filter
	for _, id := range []int64{1, 32, 27} {
		require.NoError(t, repo_model.SaveTopics(db.DefaultContext, id, "shared-topic"))
	}
	repos, _ = exploreRepos(t, nil, "?q=shared-topic&topic=1", http.StatusOK)
	assert.Len(t, repos, 3)
	repos, doc = exploreRepos(t, nil, fmt.Sprintf("?q=shared-topic&topic=1&category=%d", a.ID), http.StatusOK)
	assert.Equal(t, []string{"org3/repo21"}, repos)
	repos, _ = exploreRepos(t, nil, "?q=shared-topic&topic=1&category=other", http.StatusOK)
	assert.Equal(t, []string{"user2/repo1"}, repos)

	// AC5: the form, the topic links and the opener keep and name the category
	assert.Equal(t, "true", doc.Find(`#repo-search-form input[name="topic"]`).AttrOr("value", ""))
	checked := doc.Find(`#repo-search-category input[name="category"][checked]`)
	assert.Equal(t, fmt.Sprint(a.ID), checked.AttrOr("value", ""))
	assert.True(t, checked.Parent().HasClass("active"))
	assert.Equal(t, "Category: Alpha", strings.TrimSpace(doc.Find("#repo-search-category > .text").Text()))
	assert.Equal(t, fmt.Sprintf("/explore/repos?q=shared-topic&topic=1&category=%d", a.ID), doc.Find(".repo-topics a").AttrOr("href", ""))
	_, doc = exploreRepos(t, nil, "?category=other", http.StatusOK)
	assert.Equal(t, "Category: Other", strings.TrimSpace(doc.Find("#repo-search-category > .text").Text()))
	_, doc = exploreRepos(t, nil, "", http.StatusOK)
	assert.Equal(t, "Category", strings.TrimSpace(doc.Find("#repo-search-category > .text").Text()))
	assert.Empty(t, doc.Find(`#repo-search-category input[name="category"][checked]`).AttrOr("value", "none"))

	defer test.MockVariableValue(&setting.UI.ExplorePagingNum, 2)()
	_, doc = exploreRepos(t, nil, "?category=other", http.StatusOK)
	pager := doc.Find(".pagination a.item[href]")
	require.Positive(t, pager.Length())
	pager.Each(func(_ int, sel *goquery.Selection) {
		assert.Contains(t, sel.AttrOr("href", ""), "category=other")
	})

	// AC6: other pages with the repository search are unchanged
	resp := admin.MakeRequest(t, NewRequest(t, "GET", "/admin/repos?category=9999"), http.StatusOK)
	assert.Zero(t, NewHTMLParser(t, resp.Body).Find("#repo-search-category").Length())
	resp = MakeRequest(t, NewRequest(t, "GET", "/user2?category=9999"), http.StatusOK)
	assert.Zero(t, NewHTMLParser(t, resp.Body).Find("#repo-search-category").Length())
}

// dashboardSwitcher returns the dashboard context switcher's organizations, with "#<header>" entries.
func dashboardSwitcher(t *testing.T, s *TestSession) ([]string, string) {
	t.Helper()
	resp := s.MakeRequest(t, NewRequest(t, "GET", "/"), http.StatusOK)
	body := resp.Body.String()
	var items []string
	NewHTMLParser(t, resp.Body).Find(".context.user.overflow.menu .scrolling.menu.items").First().Children().Each(func(i int, sel *goquery.Selection) {
		switch {
		case sel.Is(".header"):
			items = append(items, "#"+strings.TrimSpace(sel.Text()))
		case i > 0 && sel.Is("a.item[title]"):
			items = append(items, sel.AttrOr("title", ""))
		}
	})
	return items, body
}

// AC7-AC9: the dashboard switcher and "My organizations" tab are grouped by category.
func TestOrgCategoryDashboard(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	user5 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 5})
	session := loginUser(t, "user5")

	// AC9: without categories, no headers and the name order
	plain, body := dashboardSwitcher(t, session)
	require.Greater(t, len(plain), 3)
	assert.True(t, slices.IsSorted(plain))
	assert.NotContains(t, body, "'category':")

	orgs, err := organization.GetUserOrgsList(db.DefaultContext, user5)
	require.NoError(t, err)
	a := createOrgCategory(t, "Alpha", 2)
	b := createOrgCategory(t, "Beta", 1)
	setOrgCategory(t, orgs[0].ID, a.ID)
	setOrgCategory(t, orgs[1].ID, b.ID)
	setOrgCategory(t, orgs[2].ID, a.ID)

	// AC7: categories by position, "Other" last, names in order within each group
	items, body := dashboardSwitcher(t, session)
	want := []string{"#Beta", orgs[1].Name, "#Alpha", orgs[0].Name, orgs[2].Name, "#Other"}
	assert.Equal(t, want, items[:6])
	assert.Equal(t, plain[3:], items[6:])

	// AC8: the "My organizations" tab gets the same order and the category of each organization
	last := -1
	for _, name := range []string{orgs[1].Name, orgs[0].Name, orgs[2].Name, plain[3]} {
		i := strings.Index(body, fmt.Sprintf("{'name': %q", name))
		require.Greater(t, i, last, name)
		last = i
	}
	assert.Contains(t, body, fmt.Sprintf("{'name': %q, ", orgs[1].Name))
	assert.Contains(t, body, `'category': {'name': "Beta", 'color': "#1e90ff"}`)
	assert.Contains(t, body, `'category': {'name': "Other", 'color': ''}`)

	// archived organizations stay hidden from the switcher
	_, err = db.GetEngine(db.DefaultContext).Exec("UPDATE `user` SET is_archived = ? WHERE id = ?", true, orgs[1].ID)
	require.NoError(t, err)
	items, _ = dashboardSwitcher(t, session)
	assert.Equal(t, []string{"#Alpha", orgs[0].Name, orgs[2].Name, "#Other"}, items[:4])
}

// AC11: the dashboard and Explore repositories query counts don't grow with organizations, repositories or categories.
func TestOrgCategoryB1Queries(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	ctx := db.DefaultContext
	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	session := loginUser(t, "user2")
	c := createOrgCategory(t, "Counted", 0)
	// countQueries counts the queries of every goroutine: read each page until two readings in a row agree
	stable := func(f func()) int64 {
		var readings []int64
		for len(readings) < 6 {
			readings = append(readings, countQueries(t, f))
			if n := len(readings); n > 1 && readings[n-1] == readings[n-2] {
				return readings[n-1]
			}
		}
		require.FailNow(t, "query count doesn't settle", "%v", readings)
		return 0
	}

	count := func(n int) []int64 {
		for i := range n {
			org := &organization.Organization{Name: fmt.Sprintf("b1-org-%d-%d", n, i)}
			require.NoError(t, organization.CreateOrganization(ctx, org, owner))
			setOrgCategory(t, org.ID, createOrgCategory(t, fmt.Sprintf("b1-cat-%d-%d", n, i), i+1).ID)
			repoOwner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 3})
			_, err := repo_service.CreateRepositoryDirectly(ctx, owner, repoOwner, repo_service.CreateRepoOptions{Name: fmt.Sprintf("b1-repo-%d-%d", n, i)})
			require.NoError(t, err)
		}
		setOrgCategory(t, 3, c.ID)
		var counts []int64
		for _, l := range []string{"/", fmt.Sprintf("/explore/repos?category=%d", c.ID), "/explore/repos?category=other"} {
			session.MakeRequest(t, NewRequest(t, "GET", l), http.StatusOK) // generated avatars are saved once
			counts = append(counts, stable(func() { session.MakeRequest(t, NewRequest(t, "GET", l), http.StatusOK) }))
		}
		return counts
	}
	assert.Equal(t, count(2), count(4))
}

// adminOrgs returns the organization names of an admin organizations page, and the page.
func adminOrgs(t *testing.T, s *TestSession, query string, status int) ([]string, *goquery.Document) {
	t.Helper()
	doc := NewHTMLParser(t, s.MakeRequest(t, NewRequest(t, "GET", "/admin/orgs"+query), status).Body).doc
	var names []string
	doc.Find(".admin-setting-content table tbody tr td:nth-child(2) > a").Each(func(_ int, sel *goquery.Selection) {
		names = append(names, sel.Text())
	})
	return names, doc
}

// AC10: the admin organizations list has a category filter.
func TestOrgCategoryAdminOrgs(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	defer test.MockVariableValue(&setting.UI.Admin.OrgPagingNum, 1000)()
	admin := loginUser(t, "user1")

	plain, doc := adminOrgs(t, admin, "?category=whatever", http.StatusOK)
	assert.Zero(t, doc.Find("#search-category-dropdown").Length())

	a := createOrgCategory(t, "Alpha", 1)
	setOrgCategory(t, 3, a.ID)
	setOrgCategory(t, 6, a.ID)

	names, doc := adminOrgs(t, admin, fmt.Sprintf("?category=%d&sort=alphabetically", a.ID), http.StatusOK)
	assert.Equal(t, []string{"org3", "org6"}, names)
	assert.Equal(t, "Category: Alpha", strings.TrimSpace(doc.Find(`[commandfor="search-category-dropdown"]`).Text()))
	assert.Equal(t, "Alpha", strings.TrimSpace(doc.Find("#search-category-dropdown a.active").Text()))
	assert.Equal(t, fmt.Sprint(a.ID), doc.Find(`form input[name="category"]`).AttrOr("value", ""))
	doc.Find(".admin-setting-content .ui.dropdown .menu a.item").Each(func(_ int, sel *goquery.Selection) {
		assert.Contains(t, sel.AttrOr("href", ""), fmt.Sprintf("&category=%d", a.ID))
	})
	names, _ = adminOrgs(t, admin, fmt.Sprintf("?category=%d&q=org6", a.ID), http.StatusOK)
	assert.Equal(t, []string{"org6"}, names)
	other, _ := adminOrgs(t, admin, "?category=other", http.StatusOK)
	assert.Len(t, other, len(plain)-2)
	assert.NotContains(t, other, "org3")
	adminOrgs(t, admin, "?category=9999", http.StatusNotFound)

	defer test.MockVariableValue(&setting.UI.Admin.OrgPagingNum, 1)()
	_, doc = adminOrgs(t, admin, fmt.Sprintf("?category=%d", a.ID), http.StatusOK)
	pager := doc.Find(".pagination a.item[href]")
	require.Positive(t, pager.Length())
	pager.Each(func(_ int, sel *goquery.Selection) {
		assert.Contains(t, sel.AttrOr("href", ""), fmt.Sprintf("category=%d", a.ID))
	})

	loginUser(t, "user2").MakeRequest(t, NewRequest(t, "GET", fmt.Sprintf("/admin/orgs?category=%d", a.ID)), http.StatusForbidden)
}
