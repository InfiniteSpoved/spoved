// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// Package mergeforward merges a pull request merged into a release branch forward: into every later release branch
// of the branching model, then into the development branch. A step that conflicts or that the branch rules refuse
// stops it and opens a pull request for that step; merging that pull request continues from there.
package mergeforward

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	audit_model "forgejo.org/models/audit"
	issues_model "forgejo.org/models/issues"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	"forgejo.org/modules/graceful"
	"forgejo.org/modules/json"
	"forgejo.org/modules/log"
	"forgejo.org/modules/queue"
	repo_module "forgejo.org/modules/repository"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/sync"
	asymkey_service "forgejo.org/services/asymkey"
	"forgejo.org/services/branchworkflow"
	notify_service "forgejo.org/services/notify"
	pull_service "forgejo.org/services/pull"
	repo_service "forgejo.org/services/repository"
)

// Reasons a cascade stops, stored in the timeline comment and the audit entry.
const (
	ReasonConflict = "conflict" // the step has merge conflicts
	ReasonRefused  = "refused"  // the branch rules refused the push
	ReasonMerger   = "merger"   // the user who merged can no longer push
	ReasonError    = "error"

	pushAttempts = 3 // a target that moves during a step is retried
)

var (
	mergeForwardQueue *queue.WorkerPoolQueue[int64]
	repoLocks         sync.MutexMap // one cascade at a time per repository in this process

	errConflict = errors.New("merge conflict")
	errRefused  = errors.New("push refused")
)

// Init starts the queue and listens for merges.
func Init() error {
	notify_service.RegisterNotifier(&notifier{})
	mergeForwardQueue = queue.CreateUniqueQueue(graceful.GetManager().ShutdownContext(), "merge_forward", handler)
	if mergeForwardQueue == nil {
		return errors.New("unable to create merge_forward queue")
	}
	go graceful.GetManager().RunWithCancel(mergeForwardQueue)
	return nil
}

type notifier struct {
	notify_service.NullNotifier
}

func (*notifier) MergePullRequest(_ context.Context, _ *user_model.User, pr *issues_model.PullRequest) {
	queuePull(pr)
}

func (*notifier) AutoMergePullRequest(_ context.Context, _ *user_model.User, pr *issues_model.PullRequest) {
	queuePull(pr)
}

func queuePull(pr *issues_model.PullRequest) {
	if err := mergeForwardQueue.Push(pr.ID); err != nil && !errors.Is(err, queue.ErrAlreadyInQueue) {
		log.Error("merge forward: queue pull request %d: %v", pr.ID, err)
	}
}

func handler(items ...int64) (unhandled []int64) {
	ctx := graceful.GetManager().ShutdownContext()
	// the queue retries unhandled items, so cascades wait while maintenance mode is on
	if setting.GetMaintenance(ctx).ReadOnly() {
		return items
	}
	for _, id := range items {
		ran, err := Run(ctx, id)
		if err != nil {
			log.Error("merge forward of pull request %d: %v", id, err)
		} else if !ran {
			unhandled = append(unhandled, id)
		}
	}
	return unhandled
}

// Run merges the merged pull request prID forward. It returns false, to be retried, while another cascade of the
// repository runs.
func Run(ctx context.Context, prID int64) (bool, error) {
	ctx = audit_model.WithSystem(ctx, "merge_forward:"+strconv.FormatInt(prID, 10))
	pr, err := issues_model.GetPullRequestByID(ctx, prID)
	if err != nil {
		return true, err
	}
	if !pr.HasMerged {
		return true, nil
	}
	if err := pr.LoadBaseRepo(ctx); err != nil {
		return true, err
	}
	repo := pr.BaseRepo
	if repo.IsArchived || repo.IsMirror {
		return true, nil
	}
	eff, err := branchworkflow.GetEffectiveModel(ctx, repo)
	if err != nil || !eff.Model.MergeForward {
		return true, err
	}

	// a push of a step can mark another pull request as merged, which queues it while this one still runs
	locked, unlock := repoLocks.TryLock(strconv.FormatInt(repo.ID, 10))
	defer unlock()
	if !locked {
		return false, nil
	}

	chain, err := branchworkflow.GetChain(ctx, repo, eff)
	if err != nil {
		return true, err
	}
	targets := chain.Forward(pr.BaseBranch)
	if chain.DevelopmentMissing && len(targets) > 0 {
		targets = targets[:len(targets)-1]
	}
	if len(targets) == 0 {
		return true, nil
	}
	if err := pr.LoadIssue(ctx); err != nil {
		return true, err
	}

	c := &cascade{pr: pr, repo: repo}
	if c.merger, err = user_model.GetUserByID(ctx, pr.MergerID); err != nil && !user_model.IsErrUserNotExist(err) {
		return true, err
	}
	if c.merger == nil || !c.merger.IsActive || c.merger.ProhibitLogin {
		// nobody to merge as; the ghost user is the poster of the entry
		c.merger = user_model.NewGhostUser()
		return true, c.stop(ctx, nil, pr.BaseBranch, targets[0], "", ReasonMerger)
	}
	return true, c.run(ctx, targets)
}

type cascade struct {
	pr     *issues_model.PullRequest
	repo   *repo_model.Repository
	merger *user_model.User
	tmp    string
}

func (c *cascade) run(ctx context.Context, targets []string) error {
	tmp, err := repo_module.CreateTemporaryPath("merge-forward")
	if err != nil {
		return err
	}
	defer func() {
		if err := repo_module.RemoveTemporaryPath(tmp); err != nil {
			log.Error("merge forward: remove %s: %v", tmp, err)
		}
	}()
	c.tmp = tmp
	if err := git.NewCommand(ctx, "clone", "--bare", "--shared", "--quiet").AddDynamicArguments(c.repo.RepoPath(), tmp).Run(&git.RunOpts{}); err != nil {
		return fmt.Errorf("clone: %w", err)
	}

	var steps []issues_model.MergeForwardStep
	source := c.pr.BaseBranch
	for _, target := range targets {
		sha, sourceSHA, err := c.step(ctx, source, target)
		if err != nil {
			reason := ReasonError
			switch {
			case errors.Is(err, errConflict):
				reason = ReasonConflict
			case errors.Is(err, errRefused):
				reason = ReasonRefused
			default:
				log.Error("merge forward of %-v from %s to %s: %v", c.pr, source, target, err)
			}
			return c.stop(ctx, steps, source, target, sourceSHA, reason)
		}
		if sha != "" {
			steps = append(steps, issues_model.MergeForwardStep{Branch: target, SHA: sha})
			c.record(ctx, "repo.branch.merged_forward", target, map[string]any{"from": source, "commit": sha, "pull": c.pr.Index})
		}
		source = target
	}
	return c.comment(ctx, steps)
}

// step merges source into target and returns the merge commit, "" when target already has source, and the commit of
// source it read.
func (c *cascade) step(ctx context.Context, source, target string) (sha, sourceSHA string, err error) {
	for range pushAttempts {
		if err := git.NewCommand(ctx, "fetch", "--quiet", "origin").AddDynamicArguments("+"+git.BranchPrefix+source+":"+git.BranchPrefix+source, "+"+git.BranchPrefix+target+":"+git.BranchPrefix+target).Run(&git.RunOpts{Dir: c.tmp}); err != nil {
			return "", sourceSHA, fmt.Errorf("fetch: %w", err)
		}
		sourceSHA, err = c.revParse(ctx, source)
		if err != nil {
			return "", sourceSHA, err
		}
		targetSHA, err := c.revParse(ctx, target)
		if err != nil {
			return "", sourceSHA, err
		}
		if err := git.NewCommand(ctx, "merge-base", "--is-ancestor").AddDynamicArguments(sourceSHA, targetSHA).Run(&git.RunOpts{Dir: c.tmp}); err == nil {
			return "", sourceSHA, nil
		} else if !git.IsErrorExitCode(err, 1) {
			return "", sourceSHA, fmt.Errorf("merge-base --is-ancestor: %w", err)
		}
		base, _, err := git.NewCommand(ctx, "merge-base").AddDynamicArguments(sourceSHA, targetSHA).RunStdString(&git.RunOpts{Dir: c.tmp})
		if err != nil {
			return "", sourceSHA, errConflict // no common history
		}
		gitRepo, err := git.OpenRepository(ctx, c.tmp)
		if err != nil {
			return "", sourceSHA, err
		}
		tree, conflict, _, err := pull_service.MergeTree(ctx, gitRepo, strings.TrimSpace(base), targetSHA, sourceSHA, nil)
		gitRepo.Close()
		if err != nil {
			return "", sourceSHA, err
		}
		if conflict {
			return "", sourceSHA, errConflict
		}
		sha, err = c.commit(ctx, tree, source, target, targetSHA, sourceSHA)
		if err != nil {
			return "", sourceSHA, err
		}
		err = git.Push(ctx, c.tmp, git.PushOptions{
			Remote: c.repo.RepoPath(),
			Branch: sha + ":" + git.BranchPrefix + target,
			Env:    repo_module.PushingEnvironment(ctx, c.merger, c.repo),
		})
		switch {
		case err == nil:
			return sha, sourceSHA, nil
		case git.IsErrPushOutOfDate(err):
			continue
		case git.IsErrPushRejected(err):
			return "", sourceSHA, errRefused
		default:
			return "", sourceSHA, fmt.Errorf("push: %w", err)
		}
	}
	return "", sourceSHA, fmt.Errorf("%s kept moving", target)
}

func (c *cascade) revParse(ctx context.Context, branch string) (string, error) {
	sha, _, err := git.NewCommand(ctx, "rev-parse", "--verify").AddDynamicArguments(git.BranchPrefix + branch).RunStdString(&git.RunOpts{Dir: c.tmp})
	return strings.TrimSpace(sha), err
}

// Message is the message of the merge commit of a step, also the title of the pull request opened when it stops.
func Message(pr *issues_model.PullRequest, source, target string) string {
	return fmt.Sprintf("Automatic merge from %s -> %s (PR #%d)", source, target, pr.Index)
}

// commit writes the merge commit as the user who merged, signed like a merge of the pull request.
func (c *cascade) commit(ctx context.Context, tree, source, target, targetSHA, sourceSHA string) (string, error) {
	author := c.merger.NewGitSig()
	committer := author
	cmd := git.NewCommand(ctx, "commit-tree").AddDynamicArguments(tree).AddOptionValues("-p", targetSHA).AddOptionValues("-p", sourceSHA)
	if sign, keyID, signer, _ := asymkey_service.SignMerge(ctx, c.pr, c.merger, c.tmp, targetSHA, sourceSHA); sign {
		cmd.AddOptionFormat("-S%s", keyID)
		if c.repo.GetTrustModel() == repo_model.CommitterTrustModel || c.repo.GetTrustModel() == repo_model.CollaboratorCommitterTrustModel {
			committer = signer
		}
	} else {
		cmd.AddArguments("--no-gpg-sign")
	}
	now := time.Now().Format(time.RFC3339)
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME="+author.Name, "GIT_AUTHOR_EMAIL="+author.Email, "GIT_AUTHOR_DATE="+now,
		"GIT_COMMITTER_NAME="+committer.Name, "GIT_COMMITTER_EMAIL="+committer.Email, "GIT_COMMITTER_DATE="+now,
	)
	stdout := new(bytes.Buffer)
	if err := cmd.Run(&git.RunOpts{Dir: c.tmp, Env: env, Stdin: strings.NewReader(Message(c.pr, source, target) + "\n"), Stdout: stdout}); err != nil {
		return "", fmt.Errorf("commit-tree: %w", err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (c *cascade) record(ctx context.Context, action, target string, data map[string]any) {
	if err := repo_model.RecordRepoTarget(ctx, c.repo, audit_model.Event{Action: action, TargetType: "branch", TargetName: target, Data: data}); err != nil {
		log.Error("merge forward: record %s for %-v: %v", action, c.pr, err)
	}
}

func (c *cascade) comment(ctx context.Context, steps []issues_model.MergeForwardStep) error {
	if len(steps) == 0 {
		return nil
	}
	return c.createComment(ctx, issues_model.CommentTypePRMergedForward, issues_model.MergeForwardInfo{Steps: steps}, nil)
}

func (c *cascade) createComment(ctx context.Context, typ issues_model.CommentType, info issues_model.MergeForwardInfo, ref *issues_model.PullRequest) error {
	content, err := json.Marshal(info)
	if err != nil {
		return err
	}
	opts := &issues_model.CreateCommentOptions{Type: typ, Doer: c.merger, Repo: c.repo, Issue: c.pr.Issue, Content: string(content)}
	if ref != nil {
		opts.RefRepoID, opts.RefIssueID, opts.RefIsPull = ref.BaseRepoID, ref.IssueID, true
	}
	_, err = issues_model.CreateComment(ctx, opts)
	return err
}

// stop records where the cascade stopped and, for a conflict or a refused push, opens a pull request from a new
// branch at sourceSHA into target.
func (c *cascade) stop(ctx context.Context, steps []issues_model.MergeForwardStep, source, target, sourceSHA, reason string) error {
	var opened *issues_model.PullRequest
	if (reason == ReasonConflict || reason == ReasonRefused) && sourceSHA != "" {
		var err error
		if opened, err = c.openPull(ctx, source, target, sourceSHA); err != nil {
			log.Error("merge forward: open a pull request from %s into %s for %-v: %v", source, target, c.pr, err)
		}
	}
	data := map[string]any{"from": source, "reason": reason, "pull": c.pr.Index}
	if opened != nil {
		data["conflict_pull"] = opened.Index
	}
	c.record(ctx, "repo.branch.merge_forward_stopped", target, data)
	if err := c.comment(ctx, steps); err != nil {
		return err
	}
	return c.createComment(ctx, issues_model.CommentTypePRMergeForwardStopped, issues_model.MergeForwardInfo{Branch: target, Reason: reason}, opened)
}

// BranchName is the branch the pull request of a stopped step comes from.
func BranchName(pr *issues_model.PullRequest, target string) string {
	return fmt.Sprintf("automerge/pr-%d/%s", pr.Index, strings.ReplaceAll(target, "/", "-"))
}

// openPull opens the pull request of a stopped step, or returns the one already open for it.
func (c *cascade) openPull(ctx context.Context, source, target, sourceSHA string) (*issues_model.PullRequest, error) {
	branch := BranchName(c.pr, target)
	if existing, err := issues_model.GetUnmergedPullRequest(ctx, c.repo.ID, c.repo.ID, branch, target, issues_model.PullRequestFlowGithub); err == nil {
		return existing, nil
	} else if !issues_model.IsErrPullRequestNotExist(err) {
		return nil, err
	}
	gitRepo, err := git.OpenRepository(ctx, c.repo.RepoPath())
	if err != nil {
		return nil, err
	}
	defer gitRepo.Close()
	// a branch left from an earlier run may hold someone's work: open the pull request from it as it is
	if !gitRepo.IsBranchExist(branch) {
		if err := repo_service.CreateNewBranchFromCommit(ctx, c.merger, c.repo, gitRepo, sourceSHA, branch); err != nil {
			return nil, err
		}
	}

	var assignees []int64
	for _, id := range []int64{c.pr.Issue.PosterID, c.merger.ID} {
		u, err := user_model.GetUserByID(ctx, id)
		if err != nil || slices.Contains(assignees, id) {
			continue
		}
		if ok, err := access_model.CanBeAssigned(ctx, u, c.repo, true); err == nil && ok {
			assignees = append(assignees, id)
		}
	}
	issue := &issues_model.Issue{
		RepoID:   c.repo.ID,
		Repo:     c.repo,
		Title:    Message(c.pr, source, target),
		PosterID: c.merger.ID,
		Poster:   c.merger,
		IsPull:   true,
		Content:  fmt.Sprintf("Merging #%d forward from `%s` into `%s` stopped. Resolve it in this branch and merge this pull request: merging forward then continues from `%s`.", c.pr.Index, source, target, target),
	}
	pr := &issues_model.PullRequest{
		HeadRepoID: c.repo.ID,
		BaseRepoID: c.repo.ID,
		HeadBranch: branch,
		BaseBranch: target,
		HeadRepo:   c.repo,
		BaseRepo:   c.repo,
		Type:       issues_model.PullRequestGitea,
	}
	if err := pull_service.NewPullRequest(ctx, c.repo, issue, nil, nil, pr, assignees); err != nil {
		return nil, err
	}
	return pr, nil
}
