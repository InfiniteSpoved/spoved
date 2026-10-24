// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package issues

import (
	"context"

	"forgejo.org/models/db"

	"xorm.io/builder"
)

// CanBeStackParent reports whether the head branch of pr is a branch of its base repository, so that other pull
// requests can be based on it.
func (pr *PullRequest) CanBeStackParent() bool {
	return pr.Flow == PullRequestFlowGithub && pr.HeadRepoID == pr.BaseRepoID
}

// StackMark is where an open pull request is in a stack of pull requests based on each other's head branch.
type StackMark struct {
	ParentIndex int64 // the index of the pull request it is based on, 0 if none
	Bottom      bool  // others are based on it, and it is not based on one
}

// FindStackMarks returns the marks of the open pull requests among issues, whose pull requests are loaded, that are in
// a stack, by pull request ID, with one query for all of them. A parent is the only open pull request of the repository with its head branch, like in the
// stack of a single pull request.
func FindStackMarks(ctx context.Context, issues IssueList) (map[int64]StackMark, error) {
	prs := make(PullRequestList, 0, len(issues))
	for _, issue := range issues {
		if issue.PullRequest != nil {
			prs = append(prs, issue.PullRequest)
		}
	}
	repoIDs := make([]int64, 0, len(prs))
	branches := make([]string, 0, 2*len(prs))
	heads := make([]string, 0, len(prs))
	for _, pr := range prs {
		if pr.HasMerged || pr.Issue == nil || pr.Issue.IsClosed {
			continue
		}
		repoIDs = append(repoIDs, pr.BaseRepoID)
		branches = append(branches, pr.BaseBranch, pr.HeadBranch)
		heads = append(heads, pr.HeadBranch)
	}
	marks := map[int64]StackMark{}
	if len(repoIDs) == 0 {
		return marks, nil
	}

	var related PullRequestList
	if err := db.GetEngine(ctx).
		Join("INNER", "issue", "issue.id = pull_request.issue_id").
		Where(builder.Eq{"pull_request.has_merged": false, "issue.is_closed": false}).
		And(builder.In("pull_request.base_repo_id", repoIDs)).
		And(builder.In("pull_request.head_branch", branches).Or(builder.In("pull_request.base_branch", heads))).
		Find(&related); err != nil {
		return nil, err
	}

	type branch struct {
		repoID int64
		name   string
	}
	owners := map[branch][]*PullRequest{} // open pull requests that can be a parent, by their head branch
	hasChild := map[branch]bool{}         // some open pull request is based on this branch
	for _, q := range related {
		if q.CanBeStackParent() {
			owners[branch{q.BaseRepoID, q.HeadBranch}] = append(owners[branch{q.BaseRepoID, q.HeadBranch}], q)
		}
		hasChild[branch{q.BaseRepoID, q.BaseBranch}] = true
	}
	for _, pr := range prs {
		if pr.HasMerged || pr.Issue == nil || pr.Issue.IsClosed {
			continue
		}
		var mark StackMark
		if parents := owners[branch{pr.BaseRepoID, pr.BaseBranch}]; len(parents) == 1 && parents[0].ID != pr.ID {
			mark.ParentIndex = parents[0].Index
		}
		mark.Bottom = mark.ParentIndex == 0 && pr.CanBeStackParent() && hasChild[branch{pr.BaseRepoID, pr.HeadBranch}] &&
			len(owners[branch{pr.BaseRepoID, pr.HeadBranch}]) == 1
		if mark != (StackMark{}) {
			marks[pr.ID] = mark
		}
	}
	return marks, nil
}
