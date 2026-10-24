// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package pull

import (
	"context"

	git_model "forgejo.org/models/git"
	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/git"
	api "forgejo.org/modules/structs"
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
	approvals, err := issues.GetApprovalCounts(ctx)
	if err != nil {
		return nil, err
	}
	statuses, err := git_model.GetLatestCommitStatusForRepoCommitIDs(ctx, repo.ID, shas)
	if err != nil {
		return nil, err
	}

	res := make(map[int64]StackStatus, len(stack.Entries))
	for _, e := range stack.Entries {
		st := StackStatus{HeadCommitID: heads[e.PR.ID], Review: StackReviewPending}
		for _, c := range approvals[e.PR.IssueID] {
			if c.Type == issues_model.ReviewTypeReject {
				st.Review = StackReviewChangesRequested
				break
			} else if c.Type == issues_model.ReviewTypeApprove {
				st.Review = StackReviewApproved
			}
		}
		if s := git_model.CalcCommitStatus(statuses[st.HeadCommitID]); s != nil {
			st.Check = s.State
		}
		res[e.PR.ID] = st
	}
	return res, nil
}
