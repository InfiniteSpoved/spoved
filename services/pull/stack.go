// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package pull

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"forgejo.org/models"
	issues_model "forgejo.org/models/issues"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/gitrepo"
	"forgejo.org/modules/setting"
)

const (
	// MaxStackSize is the most pull requests GetStack returns.
	MaxStackSize = 50
	// MaxMergeStackSize is the most pull requests MergeStack merges in one call.
	MaxMergeStackSize = 20
)

var (
	ErrStackChanged = errors.New("the stack changed")
	ErrStackTooLong = errors.New("the stack is too long to merge at once")
)

// StackEntry is a pull request of a stack.
type StackEntry struct {
	PR          *issues_model.PullRequest
	ParentIndex int64 // 0 for the bottom pull request
	Depth       int
}

// Stack is a tree of open pull requests of one repository, each based on the head branch of its parent.
type Stack struct {
	BaseBranch string // the base branch of the bottom pull request
	Truncated  bool   // more than MaxStackSize pull requests
	Entries    []*StackEntry
}

// canBeParent reports whether the head branch of pr is a branch of its base repository.
func canBeParent(pr *issues_model.PullRequest) bool {
	return pr.Flow == issues_model.PullRequestFlowGithub && pr.HeadRepoID == pr.BaseRepoID
}

// GetStack returns the stack of the open pull request pr: its ancestors from the bottom up, pr and its descendants
// depth-first by index. A parent is the only open pull request of the repository whose head branch is the base branch
// of its child; a parent that is ambiguous ends the chain. The stack has no entries when pr has no parent and no
// children.
func GetStack(ctx context.Context, pr *issues_model.PullRequest) (*Stack, error) {
	if err := pr.LoadIssue(ctx); err != nil {
		return nil, err
	}
	if pr.HasMerged || pr.Issue.IsClosed {
		return &Stack{}, nil
	}

	seen := map[int64]bool{pr.ID: true}
	truncated := false
	var ancestors []*issues_model.PullRequest // parent first
	for cur := pr; ; {
		prs, err := issues_model.GetUnmergedPullRequestsByBranches(ctx, pr.BaseRepoID, []string{cur.BaseBranch})
		if err != nil {
			return nil, err
		}
		var parent *issues_model.PullRequest
		n := 0
		for _, q := range prs {
			if canBeParent(q) && q.HeadBranch == cur.BaseBranch {
				parent = q
				n++
			}
		}
		if n != 1 || seen[parent.ID] {
			break
		}
		if len(ancestors)+1 >= MaxStackSize {
			truncated = true
			break
		}
		seen[parent.ID] = true
		ancestors = append(ancestors, parent)
		cur = parent
	}

	children := map[int64][]*issues_model.PullRequest{}
	count := len(ancestors) + 1
	for level := []*issues_model.PullRequest{pr}; len(level) > 0 && !truncated; {
		parents := map[string]*issues_model.PullRequest{}
		heads := make([]string, 0, len(level))
		for _, p := range level {
			if canBeParent(p) {
				parents[p.HeadBranch] = p
				heads = append(heads, p.HeadBranch)
			}
		}
		if len(heads) == 0 {
			break
		}
		prs, err := issues_model.GetUnmergedPullRequestsByBranches(ctx, pr.BaseRepoID, heads)
		if err != nil {
			return nil, err
		}
		owners := map[string]int{} // open pull requests with this head branch
		for _, q := range prs {
			if canBeParent(q) {
				owners[q.HeadBranch]++
			}
		}
		slices.SortFunc(prs, func(a, b *issues_model.PullRequest) int { return cmp.Compare(a.Index, b.Index) })
		level = nil
		for _, q := range prs {
			p := parents[q.BaseBranch]
			if p == nil || owners[q.BaseBranch] != 1 || seen[q.ID] {
				continue
			}
			if count >= MaxStackSize {
				truncated = true
				break
			}
			seen[q.ID] = true
			count++
			children[p.ID] = append(children[p.ID], q)
			level = append(level, q)
		}
	}

	if len(ancestors) == 0 && len(children[pr.ID]) == 0 {
		return &Stack{}, nil
	}

	stack := &Stack{Truncated: truncated, Entries: make([]*StackEntry, 0, count)}
	var parentIndex int64
	for _, a := range slices.Backward(ancestors) {
		stack.Entries = append(stack.Entries, &StackEntry{PR: a, ParentIndex: parentIndex, Depth: len(stack.Entries)})
		parentIndex = a.Index
	}
	var walk func(p *issues_model.PullRequest, parentIndex int64, depth int)
	walk = func(p *issues_model.PullRequest, parentIndex int64, depth int) {
		stack.Entries = append(stack.Entries, &StackEntry{PR: p, ParentIndex: parentIndex, Depth: depth})
		for _, c := range children[p.ID] {
			walk(c, p.Index, depth+1)
		}
	}
	walk(pr, parentIndex, len(ancestors))
	stack.BaseBranch = stack.Entries[0].PR.BaseBranch

	prs := make(issues_model.PullRequestList, len(stack.Entries))
	for i, e := range stack.Entries {
		prs[i] = e.PR
	}
	if _, err := prs.LoadIssues(ctx); err != nil {
		return nil, err
	}
	return stack, nil
}

// StackHead is a pull request of a stack and its head commit as the client saw them.
type StackHead struct {
	Index        int64
	HeadCommitID string
}

// ErrStackMerge is the pull request of a stack that could not be merged and why.
type ErrStackMerge struct {
	Index int64
	Err   error
}

func (e ErrStackMerge) Error() string {
	return fmt.Sprintf("pull request #%d: %v", e.Index, e.Err)
}

func (e ErrStackMerge) Unwrap() error {
	return e.Err
}

// MergeStack merges the pull requests of the stack from the bottom up to pr into the base branch of the stack, each
// one retargeted to it first. expected lists them as the client saw them, from the bottom; the call is refused with
// ErrStackChanged unless it is the current chain. Every pull request must pass the merge checks as if it targeted the
// base branch before anything is merged. It stops at the first failure with ErrStackMerge and returns the indexes of
// the pull requests it merged, which stay merged. Branches are not deleted.
func MergeStack(ctx context.Context, doer *user_model.User, perm *access_model.Permission, pr *issues_model.PullRequest, style repo_model.MergeStyle, expected []StackHead) ([]int64, error) {
	chain := []*issues_model.PullRequest{pr}
	stack, err := GetStack(ctx, pr)
	if err != nil {
		return nil, err
	}
	for i, e := range stack.Entries {
		if e.PR.ID == pr.ID {
			chain = make([]*issues_model.PullRequest, i+1)
			for j := range chain {
				chain[j] = stack.Entries[j].PR
			}
			break
		}
	}
	if len(chain) > MaxMergeStackSize {
		return nil, ErrStackTooLong
	}
	if len(chain) != len(expected) {
		return nil, ErrStackChanged
	}
	for i, p := range chain {
		if p.Index != expected[i].Index || expected[i].HeadCommitID == "" {
			return nil, ErrStackChanged
		}
	}

	base := chain[0].BaseBranch
	for _, p := range chain {
		check := *p // never written
		check.BaseBranch = base
		if err := CheckPullMergeable(ctx, doer, perm, &check, MergeCheckTypeGeneral, false, style); err != nil {
			return nil, ErrStackMerge{Index: p.Index, Err: err}
		}
	}

	merged := make([]int64, 0, len(chain))
	for i, p := range chain {
		if err := mergeStackPull(ctx, doer, perm, p, expected[i].HeadCommitID, base, style); err != nil {
			if models.IsErrPullRequestHasMerged(err) {
				err = ErrHasMerged
			}
			return merged, ErrStackMerge{Index: p.Index, Err: err}
		}
		merged = append(merged, p.Index)
	}
	return merged, nil
}

// mergeStackPull retargets the pull request p of a stack to base, unless it already targets it, and merges it.
func mergeStackPull(ctx context.Context, doer *user_model.User, perm *access_model.Permission, p *issues_model.PullRequest, headCommitID, base string, style repo_model.MergeStyle) error {
	// like the web merge, finish the work even if the request is cancelled
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(setting.Git.Timeout.Default)*time.Second)
	defer cancel()

	pr, err := issues_model.GetPullRequestByID(ctx, p.ID)
	if err != nil {
		return err
	}
	if pr.HasMerged {
		return ErrHasMerged
	}
	if err := pr.LoadIssue(ctx); err != nil {
		return err
	}
	if err := pr.Issue.LoadRepo(ctx); err != nil {
		return err
	}
	if pr.BaseBranch != base {
		if pr.BaseBranch != p.BaseBranch { // retargeted elsewhere meanwhile
			return ErrStackChanged
		}
		if err := changeTargetBranch(ctx, pr, doer, base, false); err != nil {
			return err
		}
	}

	err = CheckPullMergeable(ctx, doer, perm, pr, MergeCheckTypeGeneral, false, style)
	if errors.Is(err, ErrIsChecking) {
		// a push to base may have queued a check of the retargeted pull request: run it now
		testPRProtected(ctx, pr.ID)
		if pr, err = issues_model.GetPullRequestByID(ctx, p.ID); err != nil {
			return err
		}
		err = CheckPullMergeable(ctx, doer, perm, pr, MergeCheckTypeGeneral, false, style)
	}
	if err != nil {
		return err
	}

	if err := pr.LoadBaseRepo(ctx); err != nil {
		return err
	}
	gitRepo, err := gitrepo.OpenRepository(ctx, pr.BaseRepo)
	if err != nil {
		return err
	}
	defer gitRepo.Close()
	message, _, err := GetDefaultMergeMessage(ctx, gitRepo, pr, style)
	if err != nil {
		return err
	}
	return Merge(ctx, pr, doer, gitRepo, style, headCommitID, message, false)
}
