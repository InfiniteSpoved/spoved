// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"testing"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	"forgejo.org/tests"

	"github.com/PuerkitoBio/goquery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdminStorage(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	require.NoError(t, repo_model.UpdateRepoSize(db.DefaultContext, 3, 1<<30, 1<<20)) // org3/repo3, private

	admin := loginUser(t, "user1")
	user := loginUser(t, "user2")

	t.Run("NonAdmin", func(t *testing.T) {
		user.MakeRequest(t, NewRequest(t, "GET", "/admin/storage"), http.StatusForbidden)
		user.MakeRequest(t, NewRequest(t, "GET", "/admin/storage?format=csv"), http.StatusForbidden)
	})

	t.Run("Page", func(t *testing.T) {
		resp := admin.MakeRequest(t, NewRequest(t, "GET", "/admin/storage"), http.StatusOK)
		htmlDoc := NewHTMLParser(t, resp.Body)
		first := htmlDoc.Find("table tbody tr td a[href^='/admin/repos?uid=']").First()
		assert.Equal(t, "org3", first.Text())
		assert.Equal(t, "/admin/repos?uid=3&sort=reversesize", first.AttrOr("href", ""))
	})

	t.Run("CSV", func(t *testing.T) {
		resp := admin.MakeRequest(t, NewRequest(t, "GET", "/admin/storage?format=csv&sort=reversegitsize"), http.StatusOK)
		assert.Equal(t, "text/csv; charset=utf-8", resp.Header().Get("Content-Type"))
		assert.Contains(t, resp.Header().Get("Content-Disposition"), "attachment")
		rows, err := csv.NewReader(resp.Body).ReadAll()
		require.NoError(t, err)

		owners, _, err := repo_model.FindOwnerStorage(db.DefaultContext, repo_model.FindOwnerStorageOptions{OrderBy: "reversegitsize"})
		require.NoError(t, err)
		require.Len(t, rows, len(owners)+1)
		assert.Equal(t, []string{"owner", "type", "repos", "git_size", "lfs_size", "total_size", "percent"}, rows[0])
		assert.Equal(t, []string{"org3", "org", strconv.FormatInt(owners[0].NumRepos, 10), "1073741824", "1048576", "1074790400"}, rows[1][:6])
		for i, o := range owners {
			assert.Equal(t, o.Owner.Name, rows[i+1][0])
			assert.Equal(t, strconv.FormatInt(o.Size(), 10), rows[i+1][5])
		}
	})

	t.Run("RepoListOwnerFilter", func(t *testing.T) {
		resp := admin.MakeRequest(t, NewRequest(t, "GET", "/admin/repos?uid=2"), http.StatusOK)
		htmlDoc := NewHTMLParser(t, resp.Body)
		rows := htmlDoc.Find("table tbody tr")
		assert.Equal(t, unittest.GetCount(t, &repo_model.Repository{OwnerID: 2}), rows.Length())
		rows.Each(func(_ int, row *goquery.Selection) {
			assert.Equal(t, "user2", row.Find("td").Eq(1).Find("a").Text())
		})
		assert.Equal(t, 1, htmlDoc.Find("table tbody tr td a[href='/user2/repo2']").Length(), "private repos are listed")

		resp = admin.MakeRequest(t, NewRequest(t, "GET", "/admin/repos?sort=reversesize"), http.StatusOK)
		htmlDoc = NewHTMLParser(t, resp.Body)
		assert.Equal(t, "/org3/repo3", htmlDoc.Find("table tbody tr").First().Find("td").Eq(2).Find("a").AttrOr("href", ""))
	})

	t.Run("ExploreIgnoresOwnerFilter", func(t *testing.T) {
		links := func(url string) []string {
			resp := user.MakeRequest(t, NewRequest(t, "GET", url), http.StatusOK)
			return NewHTMLParser(t, resp.Body).Find(".flex-item-title a").Map(func(_ int, s *goquery.Selection) string {
				return s.AttrOr("href", "")
			})
		}
		want := links("/explore/repos")
		assert.NotEmpty(t, want)
		assert.Equal(t, want, links("/explore/repos?uid=5"))
	})
}
