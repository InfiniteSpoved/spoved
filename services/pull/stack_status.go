// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package pull

import (
	"context"
	"slices"

	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/git"
	api "forgejo.org/modules/structs"

	"xorm.io/builder"
)

// Review states of a pull request of a stack.
const (
	StackReviewPending          = "pending"
	StackReviewApproved         = "approved"
	StackReviewChangesRequested = "changes_requested"
)

// StackStatus is the head commit, review state and check state of a pull request of a stack.
type StackStatus struct {
	HeadCommitID string
	Review       string                // one of the StackReview constants
	Check        api.CommitStatusState // empty if the head has no checks
	// RequiredCheck is the state of the checks the rules of the stack base require, empty if they require none
	RequiredCheck api.CommitStatusState
	// ReviewBlocks is whether the rules of the stack base refuse a merge while changes are requested
	ReviewBlocks bool
}

// StackStatuses returns the status of every pull request of the stack of repo, by pull request ID, with a constant
// number of queries. gitRepo is the git repository of repo.
func StackStatuses(ctx context.Context, repo *repo_model.Repository, gitRepo *git.Repository, stack *Stack) (map[int64]StackStatus, error) {
	issues := make(issues_model.IssueList, len(stack.Entries))
	heads := make(map[int64]string, len(stack.Entries))
	shas := make([]string, 0, len(stack.Entries))
	for i, e := range stack.Entries {
		e.PR.Issue.Repo = repo
		issues[i] = e.PR.Issue
		if sha, err := gitRepo.GetRefCommitID(e.PR.GetGitRefName()); err == nil {
			heads[e.PR.ID] = sha
			shas = append(shas, sha)
		}
	}
	// a stale approval is no approval of the head: only rejections and fresh approvals count
	ids := make([]int64, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
	}
	var reviews []*issues_model.ReviewCount
	if err := db.GetEngine(ctx).Table("review").Select("issue_id, type, count(id) as `count`").
		Where(builder.Eq{"official": true, "dismissed": false}).
		And(builder.In("issue_id", ids)).
		And(builder.Or(builder.Eq{"type": issues_model.ReviewTypeReject}, builder.Eq{"type": issues_model.ReviewTypeApprove, "stale": false})).
		GroupBy("issue_id, type").
		Find(&reviews); err != nil {
		return nil, err
	}
	approvals := map[int64][]*issues_model.ReviewCount{}
	for _, c := range reviews {
		approvals[c.IssueID] = append(approvals[c.IssueID], c)
	}
	statuses, err := git_model.GetLatestCommitStatusForRepoCommitIDs(ctx, repo.ID, shas)
	if err != nil {
		return nil, err
	}

	rules, err := git_model.GetMatchedProtectedBranchRules(ctx, repo.ID, stack.BaseBranch)
	if err != nil {
		return nil, err
	}
	blocksOnRejection := slices.ContainsFunc(rules, func(pb *git_model.ProtectedBranch) bool { return pb.BlockOnRejectedReviews })

	res := make(map[int64]StackStatus, len(stack.Entries))
	for _, e := range stack.Entries {
		st := StackStatus{HeadCommitID: heads[e.PR.ID], Review: StackReviewPending}
		for _, pb := range rules {
			if !pb.EnableStatusCheck {
				continue
			}
			if state := MergeRequiredContextsCommitStatus(statuses[st.HeadCommitID], pb.StatusCheckContexts); st.RequiredCheck == "" || state.NoBetterThan(st.RequiredCheck) {
				st.RequiredCheck = state
			}
		}
		for _, c := range approvals[e.PR.IssueID] {
			if c.Type == issues_model.ReviewTypeReject {
				st.Review = StackReviewChangesRequested
				break
			} else if c.Type == issues_model.ReviewTypeApprove {
				st.Review = StackReviewApproved
			}
		}
		st.ReviewBlocks = blocksOnRejection && st.Review == StackReviewChangesRequested
		if s := git_model.CalcCommitStatus(statuses[st.HeadCommitID]); s != nil {
			st.Check = s.State
		}
		res[e.PR.ID] = st
	}
	return res, nil
}
