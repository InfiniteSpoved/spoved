// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	stdCtx "context"
	"errors"
	"html/template"
	"strconv"
	"strings"
	"time"

	"forgejo.org/models"
	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"forgejo.org/modules/git"
	"forgejo.org/modules/gitrepo"
	"forgejo.org/modules/setting"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/web"
	"forgejo.org/services/branchworkflow"
	"forgejo.org/services/context"
	"forgejo.org/services/forms"
	pull_service "forgejo.org/services/pull"
	repo_service "forgejo.org/services/repository"
)

// stackState is a review or check state of a pull request of a stack, in words and an icon, never by colour only.
type stackState struct {
	Text  template.HTML
	Icon  string
	Class string
}

// stackRow is a pull request of the stack panel.
type stackRow struct {
	Index        int64
	Title        string
	Link         string
	Depth        int
	Current      bool
	Commits      int64
	HeadCommitID string
	Review       stackState
	Check        stackState
	Blocker      string // why it can't be merged yet: "checks", "review" or ""
}

type stackStyle struct {
	Value string
	Text  template.HTML
}

// stackView is the stack panel and the merge stack part of the merge box.
type stackView struct {
	Rows      []*stackRow
	Base      string
	Truncated bool

	Parent         *stackRow   // the pull request this one is based on, nil if it is the bottom
	Chain          []*stackRow // the pull requests merged by Merge stack, bottom first, this one last
	HasDescendants bool
	CanMerge       bool          // the viewer may merge the stack
	TooLong        bool          // the chain is longer than one merge takes
	MaxMerge       int           // the most pull requests one merge takes
	Blocked        template.HTML // why Merge stack is disabled
	Styles         []stackStyle
	DefaultStyle   string
	DeleteDefault  bool
	DeleteForced   bool
}

func stackReviewState(ctx *context.Context, review string) stackState {
	switch review {
	case pull_service.StackReviewApproved:
		return stackState{ctx.Tr("repo.pulls.stack.review.approved"), "octicon-check", "green"}
	case pull_service.StackReviewChangesRequested:
		return stackState{ctx.Tr("repo.pulls.stack.review.changes_requested"), "octicon-x", "red"}
	}
	return stackState{ctx.Tr("repo.pulls.stack.review.pending"), "octicon-circle", "grey"}
}

func stackCheckState(ctx *context.Context, check api.CommitStatusState) stackState {
	switch check {
	case api.CommitStatusSuccess:
		return stackState{ctx.Tr("repo.pulls.stack.check.success"), "octicon-dot-fill", "green"}
	case api.CommitStatusPending:
		return stackState{ctx.Tr("repo.pulls.stack.check.pending"), "octicon-dot-fill", "yellow"}
	case api.CommitStatusFailure:
		return stackState{ctx.Tr("repo.pulls.stack.check.failure"), "octicon-x", "red"}
	case api.CommitStatusError:
		return stackState{ctx.Tr("repo.pulls.stack.check.error"), "octicon-x", "red"}
	case api.CommitStatusWarning:
		return stackState{ctx.Tr("repo.pulls.stack.check.warning"), "octicon-dot-fill", "yellow"}
	}
	return stackState{ctx.Tr("repo.pulls.stack.check.none"), "octicon-circle", "grey"}
}

// prepareStack sets the data of the stack panel and of Merge stack for the open pull request pull. allowMerge is
// whether the viewer may merge it.
func prepareStack(ctx *context.Context, pull *issues_model.PullRequest, allowMerge bool) {
	if pull.HasMerged || pull.Issue.IsClosed {
		return
	}
	stack, err := pull_service.GetStack(ctx, pull)
	if err != nil {
		ctx.ServerError("GetStack", err)
		return
	}
	if len(stack.Entries) == 0 {
		return
	}
	statuses, err := pull_service.StackStatuses(ctx, ctx.Repo.Repository, ctx.Repo.GitRepo, stack)
	if err != nil {
		ctx.ServerError("StackStatuses", err)
		return
	}

	view := &stackView{Base: stack.BaseBranch, Truncated: stack.Truncated, Rows: make([]*stackRow, len(stack.Entries))}
	current := 0
	for i, e := range stack.Entries {
		st := statuses[e.PR.ID]
		row := &stackRow{
			Index:        e.PR.Index,
			Title:        e.PR.Issue.Title,
			Link:         e.PR.Issue.Link(),
			Depth:        e.Depth,
			Current:      e.PR.ID == pull.ID,
			Commits:      int64(e.PR.CommitsAhead),
			HeadCommitID: st.HeadCommitID,
			Review:       stackReviewState(ctx, st.Review),
			Check:        stackCheckState(ctx, st.Check),
		}
		switch {
		case st.Check == api.CommitStatusFailure || st.Check == api.CommitStatusError:
			row.Blocker = "checks"
		case st.Review == pull_service.StackReviewChangesRequested:
			row.Blocker = "review"
		}
		view.Rows[i] = row
		if row.Current {
			current = i
		}
	}
	for _, row := range view.Rows {
		if row.Index == stack.Entries[current].ParentIndex {
			view.Parent = row
		}
	}
	view.Chain = view.Rows[:current+1]
	view.HasDescendants = current+1 < len(view.Rows)
	ctx.Data["Stack"] = view
	if view.Parent == nil {
		return
	}

	view.MaxMerge = pull_service.MaxMergeStackSize
	view.TooLong = len(view.Chain) > view.MaxMerge
	for _, row := range view.Chain {
		if row.Blocker == "checks" {
			view.Blocked = ctx.Tr("repo.pulls.stack.blocked_checks", "#"+strconv.FormatInt(row.Index, 10))
			break
		} else if row.Blocker == "review" && view.Blocked == "" {
			view.Blocked = ctx.Tr("repo.pulls.stack.blocked_review", "#"+strconv.FormatInt(row.Index, 10))
		}
	}

	cfg := ctx.Repo.Repository.MustGetUnit(ctx, unit.TypePullRequests).PullRequestsConfig()
	for _, s := range []stackStyle{
		{string(repo_model.MergeStyleMerge), ctx.Tr("repo.pulls.merge_pull_request")},
		{string(repo_model.MergeStyleRebase), ctx.Tr("repo.pulls.rebase_merge_pull_request")},
		{string(repo_model.MergeStyleRebaseMerge), ctx.Tr("repo.pulls.rebase_merge_commit_pull_request")},
		{string(repo_model.MergeStyleSquash), ctx.Tr("repo.pulls.squash_merge_pull_request")},
		{string(repo_model.MergeStyleFastForwardOnly), ctx.Tr("repo.pulls.fast_forward_only_merge_pull_request")},
	} {
		if cfg.IsMergeStyleAllowed(repo_model.MergeStyle(s.Value)) {
			view.Styles = append(view.Styles, s)
		}
	}
	view.DefaultStyle = string(cfg.GetDefaultMergeStyle())
	view.DeleteDefault, _ = ctx.Data["DefaultDeleteBranchAfterMerge"].(bool)
	view.DeleteForced, _ = ctx.Data["DeleteBranchForced"].(bool)
	view.CanMerge = allowMerge && !ctx.Repo.PersonalRepoLocked && len(view.Styles) > 0
}

// stackCause returns why the cause of a failed Merge stack is a failure, and whether it stands for itself: it is not
// about the pull request that stopped the merge but about the stack as a whole.
func stackCause(ctx *context.Context, issue *issues_model.Issue, stopped int64, merged int, cause error) (reason any, alone, ok bool) {
	if message, ok := mergeCheckMessage(ctx, issue, cause); ok {
		return message, false, true
	}
	switch {
	case errors.Is(cause, pull_service.ErrStackChanged):
		return ctx.Tr("repo.pulls.stack.changed"), true, true
	case errors.Is(cause, pull_service.ErrStackTooLong):
		return ctx.Tr("repo.pulls.stack.too_long"), true, true
	case errors.Is(cause, pull_service.ErrStackBaseIsHead):
		return ctx.Tr("repo.pulls.stack.base_is_head"), false, true
	case issues_model.IsErrPullRequestAlreadyExists(cause):
		return ctx.Tr("repo.pulls.stack.already_exists"), false, true
	case models.IsErrSHADoesNotMatch(cause):
		if merged == 0 {
			return ctx.Tr("repo.pulls.stack.head_changed", stopped), true, true
		}
		return ctx.Tr("repo.pulls.head_out_of_date"), false, true
	case models.IsErrInvalidMergeStyle(cause):
		return ctx.Tr("repo.pulls.invalid_merge_option"), false, true
	case models.IsErrMergeConflicts(cause):
		return ctx.Tr("repo.pulls.merge_conflict"), false, true
	case models.IsErrRebaseConflicts(cause):
		return ctx.Tr("repo.pulls.rebase_conflict", cause.(models.ErrRebaseConflicts).CommitSHA), false, true
	case models.IsErrMergeUnrelatedHistories(cause):
		return ctx.Tr("repo.pulls.unrelated_histories"), false, true
	case models.IsErrPullRequestHasMerged(cause):
		return ctx.Tr("repo.pulls.has_merged"), false, true
	case git.IsErrPushOutOfDate(cause):
		return ctx.Tr("repo.pulls.merge_out_of_date"), false, true
	case git.IsErrPushRejected(cause):
		return ctx.Tr("repo.pulls.push_rejected"), false, true
	}
	return nil, false, false
}

// MergeStack merges the pull requests of the stack of a pull request, from the bottom up to it.
func MergeStack(ctx *context.Context) {
	form := web.GetForm(ctx).(*forms.MergeStackForm)
	issue, ok := getPullInfo(ctx)
	if !ok {
		return
	}
	pr := issue.PullRequest
	pr.Issue = issue
	pr.Issue.Repo = ctx.Repo.Repository

	heads := make([]pull_service.StackHead, len(form.Heads))
	for i, h := range form.Heads {
		index, sha, _ := strings.Cut(h, ":")
		heads[i].Index, _ = strconv.ParseInt(index, 10, 64) // 0 never matches a pull request
		heads[i].HeadCommitID = sha
	}

	merged, mergeErr := pull_service.MergeStack(ctx, ctx.Doer, &ctx.Repo.Permission, pr, repo_model.MergeStyle(form.Do), heads)

	// If the HTTP request is cancelled by the user agent, don't stop work: the pull requests are merged.
	workCtx, cancelWorkCtx := stdCtx.WithTimeout(stdCtx.WithoutCancel(ctx), time.Duration(setting.Git.Timeout.Default)*time.Second)
	defer cancelWorkCtx()

	base := ""
	if len(merged) > 0 {
		alwaysDelete, err := branchworkflow.IsAlways(workCtx, ctx.Repo.Repository)
		if err != nil {
			ctx.ServerError("IsAlways", err)
			return
		}
		var kept []string
		for _, index := range merged {
			mpr, err := issues_model.GetPullRequestByIndex(workCtx, ctx.Repo.Repository.ID, index)
			if err == nil {
				err = mpr.LoadIssue(workCtx)
			}
			if err == nil {
				err = mpr.LoadHeadRepo(workCtx)
			}
			if err == nil {
				err = mpr.LoadBaseRepo(workCtx)
			}
			if err != nil {
				ctx.ServerError("GetPullRequestByIndex", err)
				return
			}
			base = mpr.BaseBranch
			if err := stopTimerIfAvailable(workCtx, ctx.Doer, mpr.Issue); err != nil {
				ctx.ServerError("stopTimerIfAvailable", err)
				return
			}
			if !form.DeleteBranchAfterMerge && !alwaysDelete {
				continue
			}
			headRepo := ctx.Repo.GitRepo
			if mpr.HeadRepoID != ctx.Repo.Repository.ID {
				if headRepo, err = gitrepo.OpenRepository(workCtx, mpr.HeadRepo); err != nil {
					ctx.ServerError("OpenRepository", err)
					return
				}
				defer headRepo.Close()
			}
			if err := repo_service.DeleteBranchAfterMerge(workCtx, ctx.Doer, mpr, headRepo); err != nil {
				message, ok := branchKeptMessage(ctx, err)
				if !ok {
					ctx.ServerError("DeleteBranchAfterMerge", err)
					return
				}
				kept = append(kept, string(ctx.Tr("repo.pulls.stack.branch_kept", index, message)))
			}
		}
		if len(kept) > 0 {
			flash := ctx.Flash.Error
			if !form.DeleteBranchAfterMerge {
				flash = ctx.Flash.Info // deletion was forced by the setting: a kept branch is not an error
			}
			flash(template.HTML(strings.Join(kept, "<br>")))
		}
	}

	total := len(heads)
	if mergeErr == nil {
		ctx.Flash.Success(ctx.Locale.TrPluralString(len(merged), "repo.pulls.stack.merged", len(merged), base))
		ctx.Redirect(issue.Link())
		return
	}

	var stopped pull_service.ErrStackMerge
	cause := mergeErr
	if errors.As(mergeErr, &stopped) {
		cause = stopped.Err
	}
	reason, alone, ok := stackCause(ctx, issue, stopped.Index, len(merged), cause)
	if !ok {
		ctx.ServerError("MergeStack", mergeErr)
		return
	}
	switch {
	case len(merged) > 0:
		ctx.Flash.Error(ctx.Locale.TrPluralString(total, "repo.pulls.stack.merged_partial", len(merged), total, stopped.Index, reason))
	case alone:
		ctx.Flash.Error(ctx.Tr("repo.pulls.stack.nothing_merged", reason))
	default:
		ctx.Flash.Error(ctx.Tr("repo.pulls.stack.nothing_merged", ctx.Tr("repo.pulls.stack.cant_merge", stopped.Index, reason)))
	}
	ctx.Redirect(issue.Link())
}
