// Copyright 2017 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"context"
	"fmt"

	"forgejo.org/models/db"

	"xorm.io/builder"
)

// RepoIndexerType specifies the repository indexer type
type RepoIndexerType int //revive:disable-line:exported

const (
	// RepoIndexerTypeCode code indexer
	RepoIndexerTypeCode RepoIndexerType = iota // 0
	// RepoIndexerTypeStats repository stats indexer
	RepoIndexerTypeStats // 1
)

// RepoIndexerStatus status of a repo's entry in the repo indexer
// For now, implicitly refers to default branch
type RepoIndexerStatus struct { //revive:disable-line:exported
	ID          int64           `xorm:"pk autoincr"`
	RepoID      int64           `xorm:"INDEX(s)"`
	CommitSha   string          `xorm:"VARCHAR(64)"`
	IndexerType RepoIndexerType `xorm:"INDEX(s) NOT NULL DEFAULT 0"`
}

// RepoIndexerBranch is the commit a non-default branch was last indexed at
type RepoIndexerBranch struct { //revive:disable-line:exported
	ID        int64  `xorm:"pk autoincr"`
	RepoID    int64  `xorm:"UNIQUE(s) NOT NULL"`
	Branch    string `xorm:"UNIQUE(s) VARCHAR(255) NOT NULL"`
	CommitSha string `xorm:"VARCHAR(64)"`
}

func init() {
	db.RegisterModel(new(RepoIndexerStatus))
	db.RegisterModel(new(RepoIndexerBranch))
}

// GetIndexerBranches returns the indexed commit of each non-default branch of a repo
func GetIndexerBranches(ctx context.Context, repoID int64) (map[string]string, error) {
	rows := make([]*RepoIndexerBranch, 0, 5)
	if err := db.GetEngine(ctx).Where("repo_id = ?", repoID).Find(&rows); err != nil {
		return nil, err
	}
	branches := make(map[string]string, len(rows))
	for _, row := range rows {
		branches[row.Branch] = row.CommitSha
	}
	return branches, nil
}

// SetIndexerBranch records the indexed commit of a branch
func SetIndexerBranch(ctx context.Context, repoID int64, branch, sha string) error {
	n, err := db.GetEngine(ctx).Where("repo_id = ? AND branch = ?", repoID, branch).Cols("commit_sha").Update(&RepoIndexerBranch{CommitSha: sha})
	if err != nil || n > 0 {
		return err
	}
	_, err = db.GetEngine(ctx).Insert(&RepoIndexerBranch{RepoID: repoID, Branch: branch, CommitSha: sha})
	return err
}

// DeleteIndexerBranches forgets the given branches, or all branches of the repo if none are given
func DeleteIndexerBranches(ctx context.Context, repoID int64, branches ...string) error {
	sess := db.GetEngine(ctx).Where("repo_id = ?", repoID)
	if len(branches) > 0 {
		sess = sess.In("branch", branches)
	}
	_, err := sess.Delete(new(RepoIndexerBranch))
	return err
}

// GetUnindexedRepos returns repos which do not have an indexer status
func GetUnindexedRepos(ctx context.Context, indexerType RepoIndexerType, maxRepoID int64, page, pageSize int) ([]int64, error) {
	ids := make([]int64, 0, 50)
	cond := builder.Cond(builder.IsNull{
		"repo_indexer_status.id",
	}).And(builder.Eq{
		"repository.is_empty": false,
	})
	sess := db.GetEngine(ctx).Table("repository").Join("LEFT OUTER", "repo_indexer_status", "repository.id = repo_indexer_status.repo_id AND repo_indexer_status.indexer_type = ?", indexerType)
	if maxRepoID > 0 {
		cond = builder.And(cond, builder.Lte{
			"repository.id": maxRepoID,
		})
	}
	if page >= 0 && pageSize > 0 {
		start := 0
		if page > 0 {
			start = (page - 1) * pageSize
		}
		sess.Limit(pageSize, start)
	}

	sess.Where(cond).Cols("repository.id").Desc("repository.id")
	err := sess.Find(&ids)
	return ids, err
}

// GetIndexerStatus loads repo codes indxer status
func GetIndexerStatus(ctx context.Context, repo *Repository, indexerType RepoIndexerType) (*RepoIndexerStatus, error) {
	switch indexerType {
	case RepoIndexerTypeCode:
		if repo.CodeIndexerStatus != nil {
			return repo.CodeIndexerStatus, nil
		}
	case RepoIndexerTypeStats:
		if repo.StatsIndexerStatus != nil {
			return repo.StatsIndexerStatus, nil
		}
	}
	status := &RepoIndexerStatus{RepoID: repo.ID}
	if has, err := db.GetEngine(ctx).Where("`indexer_type` = ?", indexerType).Get(status); err != nil {
		return nil, err
	} else if !has {
		status.IndexerType = indexerType
		status.CommitSha = ""
	}
	switch indexerType {
	case RepoIndexerTypeCode:
		repo.CodeIndexerStatus = status
	case RepoIndexerTypeStats:
		repo.StatsIndexerStatus = status
	}
	return status, nil
}

// UpdateIndexerStatus updates indexer status
func UpdateIndexerStatus(ctx context.Context, repo *Repository, indexerType RepoIndexerType, sha string) error {
	status, err := GetIndexerStatus(ctx, repo, indexerType)
	if err != nil {
		return fmt.Errorf("UpdateIndexerStatus: Unable to getIndexerStatus for repo: %s Error: %w", repo.FullName(), err)
	}

	if len(status.CommitSha) == 0 {
		status.CommitSha = sha
		if err := db.Insert(ctx, status); err != nil {
			return fmt.Errorf("UpdateIndexerStatus: Unable to insert repoIndexerStatus for repo: %s Sha: %s Error: %w", repo.FullName(), sha, err)
		}
		return nil
	}
	status.CommitSha = sha
	_, err = db.GetEngine(ctx).ID(status.ID).Cols("commit_sha").
		Update(status)
	if err != nil {
		return fmt.Errorf("UpdateIndexerStatus: Unable to update repoIndexerStatus for repo: %s Sha: %s Error: %w", repo.FullName(), sha, err)
	}
	return nil
}
