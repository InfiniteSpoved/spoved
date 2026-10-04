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
	"forgejo.org/modules/setting"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/test"
	"forgejo.org/services/convert"
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
		user.MakeRequest(t, NewRequest(t, "GET", "/admin/storage?format=json"), http.StatusForbidden)
		user.MakeRequest(t, NewRequest(t, "GET", "/admin/repos?format=csv"), http.StatusForbidden)
		user.MakeRequest(t, NewRequest(t, "GET", "/admin/repos?format=json"), http.StatusForbidden)
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

	t.Run("OwnerJSON", func(t *testing.T) {
		defer test.MockVariableValue(&setting.UI.Admin.RepoPagingNum, 1)() // exports must not page
		resp := admin.MakeRequest(t, NewRequest(t, "GET", "/admin/storage?format=json&sort=reversegitsize"), http.StatusOK)
		assert.Equal(t, "application/json; charset=utf-8", resp.Header().Get("Content-Type"))
		assert.Equal(t, "nosniff", resp.Header().Get("X-Content-Type-Options"))
		assert.Contains(t, resp.Header().Get("Content-Disposition"), `attachment; filename="storage.json"`)
		var got []*api.OwnerStorage
		DecodeJSON(t, resp, &got)

		totals, err := repo_model.GetStorageTotals(db.DefaultContext)
		require.NoError(t, err)
		owners, _, err := repo_model.FindOwnerStorage(db.DefaultContext, repo_model.FindOwnerStorageOptions{OrderBy: "reversegitsize"})
		require.NoError(t, err)
		require.Greater(t, len(owners), 1)
		want := make([]*api.OwnerStorage, len(owners))
		for i, o := range owners {
			want[i] = convert.ToOwnerStorage(o, totals)
		}
		assert.Equal(t, want, got)
		assert.Equal(t, "org3", got[0].OwnerName)
	})

	repoCSV := func(t *testing.T, query string) [][]string {
		t.Helper()
		resp := admin.MakeRequest(t, NewRequest(t, "GET", "/admin/repos?format=csv"+query), http.StatusOK)
		assert.Equal(t, "text/csv; charset=utf-8", resp.Header().Get("Content-Type"))
		assert.Contains(t, resp.Header().Get("Content-Disposition"), `attachment; filename="repo-storage.csv"`)
		rows, err := csv.NewReader(resp.Body).ReadAll()
		require.NoError(t, err)
		require.NotEmpty(t, rows)
		assert.Equal(t, []string{"repo_id", "owner", "repo", "private", "git_size", "lfs_size", "total_size", "percent"}, rows[0])
		return rows[1:]
	}
	type repoRow struct {
		RepoID    int64   `json:"repo_id"`
		OwnerName string  `json:"owner_name"`
		RepoName  string  `json:"repo_name"`
		Private   bool    `json:"private"`
		GitSize   int64   `json:"git_size"`
		LFSSize   int64   `json:"lfs_size"`
		TotalSize int64   `json:"total_size"`
		Percent   float64 `json:"percent"`
	}
	repoJSON := func(t *testing.T, query string) []repoRow {
		t.Helper()
		resp := admin.MakeRequest(t, NewRequest(t, "GET", "/admin/repos?format=json"+query), http.StatusOK)
		assert.Equal(t, "application/json; charset=utf-8", resp.Header().Get("Content-Type"))
		assert.Contains(t, resp.Header().Get("Content-Disposition"), `attachment; filename="repo-storage.json"`)
		var rows []repoRow
		DecodeJSON(t, resp, &rows)
		return rows
	}

	t.Run("RepoCSV", func(t *testing.T) {
		defer test.MockVariableValue(&setting.UI.Admin.RepoPagingNum, 1)() // exports must not page
		rows := repoCSV(t, "&sort=reversesize")
		assert.Len(t, rows, unittest.GetCount(t, &repo_model.Repository{}))
		assert.Equal(t, []string{"3", "org3", "repo3", "true", "1073741824", "1048576", "1074790400"}, rows[0][:7])
		assert.Equal(t, rows, repoCSV(t, "&sort=bogus"), "unknown sort keys fall back to reversesize")
		assert.Equal(t, rows, repoCSV(t, "&uid=0&sort=reversesize"), "uid=0 means all owners")

		rows = repoCSV(t, "&uid=2&sort=gitsize")
		assert.Len(t, rows, unittest.GetCount(t, &repo_model.Repository{OwnerID: 2}))
		for _, row := range rows {
			assert.Equal(t, "user2", row[1])
		}
	})

	t.Run("RepoJSON", func(t *testing.T) {
		defer test.MockVariableValue(&setting.UI.Admin.RepoPagingNum, 1)() // exports must not page
		rows := repoJSON(t, "&sort=reversesize")
		assert.Len(t, rows, unittest.GetCount(t, &repo_model.Repository{}))
		assert.Equal(t, repoRow{3, "org3", "repo3", true, 1 << 30, 1 << 20, 1<<30 + 1<<20, rows[0].Percent}, rows[0])
		assert.Positive(t, rows[0].Percent)

		rows = repoJSON(t, "&uid=2")
		assert.Len(t, rows, unittest.GetCount(t, &repo_model.Repository{OwnerID: 2}))
		for _, row := range rows {
			assert.Equal(t, "user2", row.OwnerName)
		}

		resp := admin.MakeRequest(t, NewRequest(t, "GET", "/admin/repos?format=json&uid=999999"), http.StatusOK)
		assert.Equal(t, "[]\n", resp.Body.String())
	})

	t.Run("RepoExportBadUID", func(t *testing.T) {
		admin.MakeRequest(t, NewRequest(t, "GET", "/admin/repos?format=csv&uid=abc"), http.StatusBadRequest)
		admin.MakeRequest(t, NewRequest(t, "GET", "/admin/repos?format=json&uid=2%20OR%201=1"), http.StatusBadRequest)
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

	t.Run("HostileNames", func(t *testing.T) {
		_, err := db.GetEngine(db.DefaultContext).Exec("UPDATE repository SET name = ?, lower_name = ? WHERE id = 1", `=HYPERLINK("http://evil")`, `=hyperlink("http://evil")`)
		require.NoError(t, err)
		_, err = db.GetEngine(db.DefaultContext).Exec("UPDATE repository SET name = '-1+1', lower_name = '-1+1' WHERE id = 3")
		require.NoError(t, err)
		_, err = db.GetEngine(db.DefaultContext).Exec("UPDATE `user` SET name = '@cmd', lower_name = '@cmd' WHERE id = 3")
		require.NoError(t, err)
		_, err = db.GetEngine(db.DefaultContext).Exec("UPDATE repository SET owner_name = '@cmd' WHERE owner_id = 3")
		require.NoError(t, err)

		resp := admin.MakeRequest(t, NewRequest(t, "GET", "/admin/storage?format=csv"), http.StatusOK)
		rows, err := csv.NewReader(resp.Body).ReadAll()
		require.NoError(t, err)
		assert.Equal(t, "'@cmd", rows[1][0])

		rows = repoCSV(t, "&sort=reversesize")
		assert.Equal(t, []string{"3", "'@cmd", "'-1+1"}, rows[0][:3])
		names := map[string]string{}
		for _, row := range rows {
			names[row[0]] = row[2]
		}
		assert.Equal(t, `'=HYPERLINK("http://evil")`, names["1"])

		resp = admin.MakeRequest(t, NewRequest(t, "GET", "/admin/storage?format=json"), http.StatusOK)
		var owners []*api.OwnerStorage
		DecodeJSON(t, resp, &owners)
		assert.Equal(t, "@cmd", owners[0].OwnerName)
		repos := repoJSON(t, "&sort=reversesize")
		assert.Equal(t, "@cmd", repos[0].OwnerName)
		assert.Equal(t, "-1+1", repos[0].RepoName)
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
