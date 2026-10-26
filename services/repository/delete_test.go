// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT
package repository_test

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	auth_model "forgejo.org/models/auth"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/storage"
	repo_service "forgejo.org/services/repository"
	"forgejo.org/tests/forgery"

	"github.com/stretchr/testify/require"
)

func TestDeleteRepository(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

	t.Run("Normal", func(t *testing.T) {
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})

		require.NoError(t, repo_service.DeleteRepository(t.Context(), user2, repo, false))

		unittest.AssertExistsIf(t, false, &repo_model.Repository{ID: 1})
	})

	t.Run("Foreign key reference", func(t *testing.T) {
		repo := forgery.CreateRepository(t, user2, nil)
		accessToken := forgery.CreateAccessToken(t, user2, &forgery.CreateAccessTokenOptions{
			RepositoryIDs: []int64{repo.ID},
		})

		require.NoError(t, repo_service.DeleteRepository(t.Context(), user2, repo, false))

		unittest.AssertExistsIf(t, true, &auth_model.AccessToken{ID: accessToken.ID})
		unittest.AssertExistsIf(t, false, &auth_model.AccessTokenResourceRepo{RepoID: repo.ID})
	})

	t.Run("Dependency graph", func(t *testing.T) {
		repo := forgery.CreateRepository(t, user2, nil)
		other := forgery.CreateRepository(t, user2, nil)
		for _, r := range []*repo_model.Repository{repo, other} {
			require.NoError(t, replaceDependencies(t.Context(),
				&repo_model.DependencyScan{RepoID: r.ID, CommitSHA: "abc", Status: repo_model.DependencyScanOK},
				[]*repo_model.Dependency{{RepoID: r.ID, Ecosystem: "Go", Name: "x", Version: "v1"}}))
		}

		require.NoError(t, repo_service.DeleteRepository(t.Context(), user2, repo, false))

		unittest.AssertExistsIf(t, false, &repo_model.Dependency{RepoID: repo.ID})
		unittest.AssertExistsIf(t, false, &repo_model.DependencyScan{RepoID: repo.ID})
		unittest.AssertExistsIf(t, true, &repo_model.Dependency{RepoID: other.ID})
		unittest.AssertExistsIf(t, true, &repo_model.DependencyScan{RepoID: other.ID})
	})

	t.Run("Attachment", func(t *testing.T) {
		repo := forgery.CreateRepository(t, user2, nil)

		attachment := &repo_model.Attachment{
			RepoID: repo.ID,
			UUID:   "sleepy",
		}
		unittest.AssertSuccessfulInsert(t, attachment)
		storage.Attachments.Save("s/l/sleepy", strings.NewReader("Sleepy cat photo"), 16)

		require.NoError(t, repo_service.DeleteRepository(t.Context(), user2, repo, false))

		unittest.AssertExistsIf(t, false, &repo_model.Attachment{ID: attachment.ID})
		_, err := storage.Attachments.Stat("s/l/sleepy")
		require.ErrorIs(t, err, fs.ErrNotExist)
	})
}

// replaceDependencies stores a scan row and its dependencies as a scan that read the row as it is now.
func replaceDependencies(ctx context.Context, scan *repo_model.DependencyScan, deps []*repo_model.Dependency) error {
	cur, err := repo_model.GetRepoDependencyScan(ctx, scan.RepoID)
	if err != nil {
		return err
	}
	_, err = repo_model.ReplaceRepoDependenciesIf(ctx, scan, deps, cur)
	return err
}
