// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// Package mergequeue merges pull requests one after the other, each tested combined with the ones ahead of it.
//
// Each entry of the queue of a branch is built on top of the entry ahead of it (the first on the branch): the
// pull request is merged with its merge style and the result pushed to the branch TempBranch, without hooks. The
// required status checks of the branch protection must pass on that commit. When the first entry passes, or a later
// one within the group size, the branch is fast-forwarded to each tested commit in turn through the normal hooks, so
// what lands is exactly what was tested. An entry that fails leaves the queue; the ones behind it are built again.
package mergequeue

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"forgejo.org/models"
	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	issues_model "forgejo.org/models/issues"
	access_model "forgejo.org/models/perm/access"
	pull_model "forgejo.org/models/pull"
	quota_model "forgejo.org/models/quota"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	"forgejo.org/modules/gitrepo"
	"forgejo.org/modules/graceful"
	"forgejo.org/modules/log"
	"forgejo.org/modules/process"
	"forgejo.org/modules/queue"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"
	"forgejo.org/services/branchworkflow"
	issue_service "forgejo.org/services/issue"
	notify_service "forgejo.org/services/notify"
	pull_service "forgejo.org/services/pull"
	repo_service "forgejo.org/services/repository"
	shared "forgejo.org/services/shared/mergequeue"
)

var (
	// ErrNotRequired is returned when a pull request is added to the queue of a branch that has none
	ErrNotRequired = errors.New("the target branch does not use a merge queue")
	// ErrNotQueued is returned when a pull request that is not queued is removed
	ErrNotQueued = errors.New("the pull request is not in the merge queue")
)

// Reasons an entry leaves the queue without being merged, shown by repo/issue/view_content/merge_queue_reason
const (
	ReasonRemoved       = "removed" // by a user
	ReasonChecksFailed  = "checks_failed"
	ReasonTimeout       = "timeout"
	ReasonHeadMoved     = "head_moved"
	ReasonClosed        = "closed"
	ReasonRetargeted    = "retargeted"
	ReasonPermission    = "permission"
	ReasonBlocked       = "blocked"
	ReasonConflict      = "conflict"
	ReasonWIP           = "wip"
	ReasonDisabled      = "disabled"
	ReasonBranchMissing = "branch_missing"
	ReasonMergeFailed   = "merge_failed"
)

// one pass at a time per branch in this process; between processes, every state change is a conditional update and
// the fast-forward push refuses anything else. A pass asked for while one runs (from another worker, or from a
// notifier of the running pass when the queue runs immediately) makes the running one go again.
var running = struct {
	sync.Mutex
	again map[string]bool // the branches with a running pass, true when it must run again
}{again: map[string]bool{}}

// maxRounds bounds the passes in a row; more are left to the queue
const maxRounds = 20

func claim(key string) bool {
	running.Lock()
	defer running.Unlock()
	if _, ok := running.again[key]; ok {
		running.again[key] = true
		return false
	}
	running.again[key] = false
	return true
}

// release ends the claim unless the pass must run again, which it reports
func release(key string, again bool) bool {
	running.Lock()
	defer running.Unlock()
	if again || running.again[key] {
		running.again[key] = false
		return true
	}
	delete(running.again, key)
	return false
}

// Init starts the queue that runs the passes
func Init() error {
	notify_service.RegisterNotifier(&notifier{})
	shared.Queue = queue.CreateUniqueQueue(graceful.GetManager().ShutdownContext(), "merge_queue", handler)
	if shared.Queue == nil {
		return errors.New("unable to create merge_queue queue")
	}
	go graceful.GetManager().RunWithCancel(shared.Queue)
	return nil
}

func handler(items ...string) []string {
	// the queue retries unhandled items, so the merge queues wait while maintenance mode is on
	if setting.GetMaintenance(graceful.GetManager().ShutdownContext()).ReadOnly() {
		return items
	}
	for _, key := range items {
		repoID, branch, ok := shared.ParseKey(key)
		if !ok {
			log.Error("could not parse merge queue key %q", key)
			continue
		}
		ctx, _, finished := process.GetManager().AddContext(graceful.GetManager().HammerContext(), fmt.Sprintf("Merge queue of %s in repository %d", branch, repoID))
		if err := Process(ctx, repoID, branch); err != nil {
			log.Error("Merge queue of %s in repository %d: %v", branch, repoID, err)
		}
		finished()
	}
	return nil
}

// TriggerAll starts a pass of every queue, for the time-outs and the wait times
func TriggerAll(ctx context.Context) error {
	keys, err := pull_model.FindMergeQueueKeys(ctx)
	if err != nil {
		return err
	}
	for _, k := range keys {
		shared.Trigger(k.RepoID, k.BaseBranch)
	}
	return nil
}

// Rule returns the protection rule that sets the merge queue of the base branch of pr, nil if it has none
func Rule(ctx context.Context, pr *issues_model.PullRequest) (*git_model.ProtectedBranch, error) {
	rules, err := git_model.GetMatchedProtectedBranchRules(ctx, pr.BaseRepoID, pr.BaseBranch)
	if err != nil {
		return nil, err
	}
	return git_model.MergeQueueRule(rules), nil
}

// queueStyle is the merge style the queue uses for a pull request the user chose style for
func queueStyle(rule *git_model.ProtectedBranch, style repo_model.MergeStyle) repo_model.MergeStyle {
	if rule.MergeQueueMergeStyle != "" {
		return repo_model.MergeStyle(rule.MergeQueueMergeStyle)
	}
	return style
}

// Add adds pr to the merge queue of its base branch for doer, after the checks of a merge with style. expectedHead,
// if set, must be the head of the pull request.
func Add(ctx context.Context, doer *user_model.User, perm *access_model.Permission, pr *issues_model.PullRequest, style repo_model.MergeStyle, expectedHead string, deleteBranch, adminSkipProtectionCheck bool) error {
	if err := pr.LoadBaseRepo(ctx); err != nil {
		return err
	}
	rule, err := Rule(ctx, pr)
	if err != nil {
		return err
	} else if rule == nil {
		return ErrNotRequired
	}
	style = queueStyle(rule, style)
	// fast-forward only can't be combined with the pull requests ahead
	if style != repo_model.MergeStyleMerge && style != repo_model.MergeStyleRebase && style != repo_model.MergeStyleRebaseMerge && style != repo_model.MergeStyleSquash {
		return models.ErrInvalidMergeStyle{ID: pr.BaseRepoID, Style: style}
	}
	prUnit, err := pr.BaseRepo.GetUnit(ctx, unit.TypePullRequests)
	if err != nil {
		return err
	}
	if !prUnit.PullRequestsConfig().IsMergeStyleAllowed(style) {
		return models.ErrInvalidMergeStyle{ID: pr.BaseRepoID, Style: style}
	}
	if l, err := quota_model.GetPersonalRepoLimit(ctx, pr.BaseRepo.OwnerID); err != nil {
		return err
	} else if l.Reached() {
		return quota_model.ErrPersonalRepoLimit{PersonalRepoLimit: l, OwnerID: pr.BaseRepo.OwnerID}
	}
	if err := pull_service.CheckPullMergeable(ctx, doer, perm, pr, pull_service.MergeCheckTypeGeneral, adminSkipProtectionCheck, style); err != nil {
		return err
	}

	gitRepo, closer, err := gitrepo.RepositoryFromContextOrOpen(ctx, pr.BaseRepo)
	if err != nil {
		return err
	}
	defer closer.Close()
	head, err := gitRepo.GetRefCommitID(pr.GetGitRefName())
	if err != nil {
		return err
	}
	if expectedHead != "" && expectedHead != head {
		return models.ErrSHADoesNotMatch{GivenSHA: expectedHead, CurrentSHA: head}
	}

	if err := db.WithTx(ctx, func(ctx context.Context) error {
		if err := pull_model.AddToMergeQueue(ctx, &pull_model.MergeQueueEntry{
			RepoID:       pr.BaseRepoID,
			BaseBranch:   pr.BaseBranch,
			PullID:       pr.ID,
			MergeStyle:   style,
			DeleteBranch: deleteBranch,
			HeadCommitID: head,
			EnqueuedByID: doer.ID,
		}); err != nil {
			return err
		}
		_, err := issues_model.CreateAutoMergeComment(ctx, issues_model.CommentTypePRAddedToMergeQueue, pr, doer, issues_model.AutoMergeInfo{})
		return err
	}); err != nil {
		return err
	}
	shared.Trigger(pr.BaseRepoID, pr.BaseBranch)
	return nil
}

// MayRemove reports whether doer may take pr, queued by entry, out of the queue: the author of the pull request, who
// added it, and everyone who may merge it
func MayRemove(ctx context.Context, doer *user_model.User, perm access_model.Permission, pr *issues_model.PullRequest, entry *pull_model.MergeQueueEntry) (bool, error) {
	if doer == nil {
		return false, nil
	}
	if err := pr.LoadIssue(ctx); err != nil {
		return false, err
	}
	if doer.ID == entry.EnqueuedByID || doer.ID == pr.Issue.PosterID {
		return true, nil
	}
	return pull_service.IsUserAllowedToMerge(ctx, pr, perm, doer)
}

// Remove takes pr out of the merge queue for doer; the pull requests behind it are built again
func Remove(ctx context.Context, doer *user_model.User, perm access_model.Permission, pr *issues_model.PullRequest) error {
	entry, err := pull_model.GetMergeQueueEntryByPullID(ctx, pr.ID)
	if err != nil {
		return err
	} else if entry == nil {
		return ErrNotQueued
	}
	if ok, err := MayRemove(ctx, doer, perm, pr, entry); err != nil {
		return err
	} else if !ok {
		return util.ErrPermissionDenied
	}
	if err := removeEntry(ctx, pr, entry, doer, ReasonRemoved, ""); err != nil {
		return err
	}
	shared.Trigger(entry.RepoID, entry.BaseBranch)
	return nil
}

// removeEntry deletes entry with a timeline comment by doer, unless it was deleted meanwhile
func removeEntry(ctx context.Context, pr *issues_model.PullRequest, entry *pull_model.MergeQueueEntry, doer *user_model.User, reason, sha string) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		if deleted, err := pull_model.DeleteMergeQueueEntry(ctx, entry.ID); err != nil || !deleted {
			return err
		}
		_, err := issues_model.CreateAutoMergeComment(ctx, issues_model.CommentTypePRRemovedFromMergeQueue, pr, doer, issues_model.AutoMergeInfo{Reason: reason, SHA: sha})
		return err
	})
}

// item is an entry of the queue during a pass
type item struct {
	*pull_model.MergeQueueEntry
	pr     *issues_model.PullRequest
	doer   *user_model.User
	passed bool
}

type pass struct {
	ctx     context.Context
	repo    *repo_model.Repository
	gitRepo *git.Repository
	branch  string
	rules   git_model.ProtectedBranchRules
	cfg     git_model.MergeQueueConfig
	again   bool // start another pass when this one ends
}

// Process looks at the merge queue of the branch: it removes the entries that may no longer be merged, merges the
// ones whose builds passed, and builds the next ones.
func Process(ctx context.Context, repoID int64, branch string) error {
	key := shared.Key(repoID, branch)
	if !claim(key) {
		return nil
	}
	for round := 1; ; round++ {
		again, err := processOnce(ctx, repoID, branch)
		if err != nil {
			release(key, false)
			return err
		}
		if !release(key, again) {
			return nil
		}
		if round == maxRounds {
			release(key, false)
			shared.Trigger(repoID, branch)
			return nil
		}
	}
}

func processOnce(ctx context.Context, repoID int64, branch string) (again bool, err error) {
	entries, err := pull_model.FindMergeQueue(ctx, repoID, branch)
	if err != nil {
		return false, err
	}
	repo, err := repo_model.GetRepositoryByID(ctx, repoID)
	if repo_model.IsErrRepoNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	gitRepo, err := gitrepo.OpenRepository(ctx, repo)
	if err != nil {
		return false, err
	}
	defer gitRepo.Close()

	p := &pass{ctx: ctx, repo: repo, gitRepo: gitRepo, branch: branch}
	defer func() {
		p.deleteStaleBranches()
		again = p.again
	}()
	if len(entries) == 0 || repo.IsArchived {
		return false, nil
	}
	// the builds and merges go to this repository, which the size limit of its owner's personal repositories locks
	if l, err := quota_model.GetPersonalRepoLimit(ctx, repo.OwnerID); err != nil {
		return false, err
	} else if l.Reached() {
		return false, nil
	}

	if p.rules, err = git_model.GetMatchedProtectedBranchRules(ctx, repoID, branch); err != nil {
		return false, err
	}
	rule := git_model.MergeQueueRule(p.rules)
	tip, tipErr := gitRepo.GetBranchCommitID(branch)
	if rule == nil || tipErr != nil {
		reason := ReasonDisabled
		if tipErr != nil {
			reason = ReasonBranchMissing
		}
		for _, e := range entries {
			if err := p.removeByID(e, reason); err != nil {
				return false, err
			}
		}
		return false, nil
	}
	p.cfg = rule.MergeQueueConfig()

	live, err := p.checkEntries(entries)
	if err != nil {
		return false, err
	}
	if err := p.checkChain(live, tip); err != nil {
		return false, err
	}
	if live, err = p.checkResults(live); err != nil {
		return false, err
	}
	merged, ok := p.merge(live)
	if !ok {
		return false, nil
	}
	if merged > 0 {
		tip = live[merged-1].TempCommitID
	}
	return false, p.build(live[merged:], tip)
}

// removeByID removes an entry whose pull request is not loaded yet
func (p *pass) removeByID(e *pull_model.MergeQueueEntry, reason string) error {
	pr, err := issues_model.GetPullRequestByID(p.ctx, e.PullID)
	if issues_model.IsErrPullRequestNotExist(err) {
		_, err := pull_model.DeleteMergeQueueEntry(p.ctx, e.ID)
		return err
	} else if err != nil {
		return err
	}
	return removeEntry(p.ctx, pr, e, p.poster(e), reason, "")
}

// poster is who the timeline comments of the queue about e are posted as: whoever added it, or a ghost
func (p *pass) poster(e *pull_model.MergeQueueEntry) *user_model.User {
	u, err := user_model.GetPossibleUserByID(p.ctx, e.EnqueuedByID)
	if err != nil {
		return user_model.NewGhostUser()
	}
	return u
}

func (p *pass) remove(it *item, reason, sha string) error {
	log.Info("Merge queue of %s in %-v: removing %-v (%s)", p.branch, p.repo, it.pr, reason)
	p.again = true
	return removeEntry(p.ctx, it.pr, it.MergeQueueEntry, it.doer, reason, sha)
}

// checkEntries removes the entries whose pull requests may no longer be merged and returns the others in order
func (p *pass) checkEntries(entries []*pull_model.MergeQueueEntry) ([]*item, error) {
	live := make([]*item, 0, len(entries))
	for _, e := range entries {
		pr, err := issues_model.GetPullRequestByID(p.ctx, e.PullID)
		if issues_model.IsErrPullRequestNotExist(err) {
			if _, err := pull_model.DeleteMergeQueueEntry(p.ctx, e.ID); err != nil {
				return nil, err
			}
			continue
		} else if err != nil {
			return nil, err
		}
		pr.BaseRepo = p.repo
		if pr.HasMerged {
			if _, err := pull_model.DeleteMergeQueueEntry(p.ctx, e.ID); err != nil {
				return nil, err
			}
			continue
		}
		it := &item{MergeQueueEntry: e, pr: pr, doer: p.poster(e)}
		reason, err := p.ineligible(it)
		if err != nil {
			return nil, err
		}
		if reason != "" {
			if err := p.remove(it, reason, ""); err != nil {
				return nil, err
			}
			continue
		}
		live = append(live, it)
	}
	return live, nil
}

// ineligible returns why it may no longer be merged, "" if it may. The branch protection is checked as the
// pre-receive hook will check it when the queue merges: site admins skip it, rules exempt for admins are skipped
// for repository and organization admins.
func (p *pass) ineligible(it *item) (string, error) {
	pr := it.pr
	if err := pr.LoadIssue(p.ctx); err != nil {
		return "", err
	}
	if pr.Issue.IsClosed {
		return ReasonClosed, nil
	}
	if pr.BaseBranch != it.BaseBranch {
		return ReasonRetargeted, nil
	}
	if head, err := p.gitRepo.GetRefCommitID(pr.GetGitRefName()); err != nil || head != it.HeadCommitID {
		return ReasonHeadMoved, nil
	}

	doer := it.doer
	if doer.ID <= 0 || !doer.IsActive || doer.ProhibitLogin {
		return ReasonPermission, nil
	}
	perm, err := access_model.GetUserRepoPermission(p.ctx, p.repo, doer)
	if err != nil {
		return "", err
	}
	if allowed, err := pull_service.IsUserAllowedToMerge(p.ctx, pr, perm, doer); err != nil {
		return "", err
	} else if !allowed {
		return ReasonPermission, nil
	}

	if pr.IsWorkInProgress(p.ctx) {
		return ReasonWIP, nil
	}
	if err := issue_service.CheckPRTitleRule(p.ctx, p.repo, p.branch, pr.Issue.Title, true); err != nil {
		if git_model.IsErrPRTitleRule(err) {
			return ReasonBlocked, nil
		}
		return "", err
	}
	if noDeps, err := issues_model.IssueNoDependenciesLeft(p.ctx, pr.Issue); err != nil {
		return "", err
	} else if !noDeps {
		return ReasonBlocked, nil
	}
	if doer.IsAdmin {
		return "", nil
	}
	exempt := func(pb *git_model.ProtectedBranch) bool {
		ok, err := pb.IsAdminExempt(p.ctx, doer, perm.IsAdmin())
		if err != nil {
			log.Error("IsAdminExempt: %v", err)
		}
		return ok
	}
	if _, err := pull_service.CheckPullBranchProtections(p.ctx, pr, false, exempt); err != nil {
		if models.IsErrDisallowedToMerge(err) {
			return ReasonBlocked, nil
		}
		return "", err
	}
	return "", nil
}

// checkChain makes the builds that are not on top of the branch tip and the builds ahead of them wait to be built
// again: after a push to the branch, or when an entry ahead left the queue
func (p *pass) checkChain(live []*item, tip string) error {
	prev, valid := tip, true
	var reset []int64
	for _, it := range live {
		if it.Status != pull_model.MergeQueueBuilding {
			valid = false
			continue
		}
		if valid && it.BaseCommitID == prev {
			prev = it.TempCommitID
			continue
		}
		valid = false
		reset = append(reset, it.ID)
		it.Status, it.BaseCommitID, it.TempCommitID, it.StartedUnix = pull_model.MergeQueueWaiting, "", "", 0
	}
	return pull_model.ResetMergeQueueBuilds(p.ctx, reset)
}

// checkResults reads the required status checks of the builds. The first failed build leaves the queue and the builds
// behind it wait to be built again without it.
func (p *pass) checkResults(live []*item) ([]*item, error) {
	for i, it := range live {
		if it.Status != pull_model.MergeQueueBuilding {
			break
		}
		state, err := p.checksState(it.TempCommitID)
		if err != nil {
			return nil, err
		}
		var reason string
		switch {
		case state.IsSuccess():
			it.passed = true
			continue
		case state.IsFailure() || state.IsError():
			reason = ReasonChecksFailed
		case since(it.StartedUnix) > p.cfg.CheckTimeout:
			reason = ReasonTimeout
		default:
			continue
		}
		if err := p.remove(it, reason, it.TempCommitID); err != nil {
			return nil, err
		}
		behind := live[i+1:]
		var reset []int64
		for _, b := range behind {
			if b.Status == pull_model.MergeQueueBuilding {
				reset = append(reset, b.ID)
				b.Status, b.BaseCommitID, b.TempCommitID, b.StartedUnix = pull_model.MergeQueueWaiting, "", "", 0
			}
		}
		if err := pull_model.ResetMergeQueueBuilds(p.ctx, reset); err != nil {
			return nil, err
		}
		return append(live[:i:i], behind...), nil
	}
	return live, nil
}

// checksState is the state of the required status checks of every matching rule on sha; success without any
func (p *pass) checksState(sha string) (stateResult, error) {
	statuses, _, err := git_model.GetLatestCommitStatus(p.ctx, p.repo.ID, sha, db.ListOptionsAll)
	if err != nil {
		return stateResult{}, err
	}
	result := stateResult{success: true}
	for _, pb := range p.rules {
		if !pb.EnableStatusCheck {
			continue
		}
		state := pull_service.MergeRequiredContextsCommitStatus(statuses, pb.StatusCheckContexts)
		if state.IsFailure() || state.IsError() {
			return stateResult{failure: true}, nil
		}
		if !state.IsSuccess() {
			result.success = false
		}
	}
	return result, nil
}

type stateResult struct {
	success, failure bool
}

func (s stateResult) IsSuccess() bool { return s.success }
func (s stateResult) IsFailure() bool { return s.failure }
func (s stateResult) IsError() bool   { return false }

// merge fast-forwards the branch to the passed builds: the first one, or up to the last passed one within the group
// size, whose build has all the ones ahead of it. It returns how many entries it merged, and false when the pass
// must stop.
func (p *pass) merge(live []*item) (int, bool) {
	maxGroup := min(p.cfg.MaxGroupSize, p.cfg.BuildConcurrency)
	last := -1
	for i := 0; i < len(live) && i < maxGroup && live[i].Status == pull_model.MergeQueueBuilding; i++ {
		if live[i].passed {
			last = i
		}
	}
	if last < 0 {
		return 0, true
	}
	if last+1 < p.cfg.MinGroupSize && since(live[0].EnqueuedUnix) < p.cfg.WaitTime {
		return 0, true // wait for more pull requests
	}
	for i := 0; i <= last; i++ {
		it := live[i]
		if err := pull_service.MergeQueued(p.ctx, it.pr, it.doer, it.TempCommitID); err != nil {
			p.again = true
			if git.IsErrPushOutOfDate(err) {
				// the branch moved: the next pass builds again on top of it
				log.Info("Merge queue of %s in %-v: the branch moved before %-v was merged", p.branch, p.repo, it.pr)
				return i, false
			}
			if models.IsErrPullRequestHasMerged(err) {
				_, err = pull_model.DeleteMergeQueueEntry(p.ctx, it.ID)
			} else {
				reason := ReasonMergeFailed
				if git.IsErrPushRejected(err) {
					reason = ReasonBlocked
				}
				log.Warn("Merge queue of %s in %-v: merging %-v failed: %v", p.branch, p.repo, it.pr, err)
				err = p.remove(it, reason, "")
			}
			if err != nil {
				log.Error("Merge queue of %s in %-v: %v", p.branch, p.repo, err)
			}
			return i, false
		}
		if _, err := pull_model.DeleteMergeQueueEntry(p.ctx, it.ID); err != nil {
			log.Error("DeleteMergeQueueEntry[%d]: %v", it.ID, err)
		}
		p.deleteHeadBranch(it)
	}
	p.again = true
	return last + 1, true
}

// deleteHeadBranch deletes the head branch of a merged pull request if that was asked for, or the repository always
// does it
func (p *pass) deleteHeadBranch(it *item) {
	alwaysDelete, err := branchworkflow.IsAlways(p.ctx, p.repo)
	if err != nil {
		log.Error("branchworkflow.IsAlways: %v", err)
	}
	if !it.DeleteBranch && !alwaysDelete {
		return
	}
	if err := it.pr.LoadHeadRepo(p.ctx); err != nil || it.pr.HeadRepo == nil {
		return
	}
	headRepo, closer, err := gitrepo.RepositoryFromContextOrOpen(p.ctx, it.pr.HeadRepo)
	if err != nil {
		log.Error("OpenRepository: %v", err)
		return
	}
	defer closer.Close()
	if err := repo_service.DeleteBranchAfterMerge(p.ctx, it.doer, it.pr, headRepo); err != nil {
		log.Info("Merge queue: the head branch of %-v was kept: %v", it.pr, err)
	}
}

// build builds the waiting entries within the build concurrency, each on top of the one ahead of it
func (p *pass) build(live []*item, tip string) error {
	prev := tip
	for i, it := range live {
		if i >= p.cfg.BuildConcurrency {
			break
		}
		if it.Status == pull_model.MergeQueueBuilding {
			prev = it.TempCommitID
			continue
		}
		rule := git_model.MergeQueueRule(p.rules)
		sha, err := pull_service.BuildMergeQueueCommit(p.ctx, it.pr, it.doer, queueStyle(rule, it.MergeStyle), it.HeadCommitID, prev, it.TempBranch())
		if err != nil {
			switch {
			case models.IsErrMergeConflicts(err), models.IsErrRebaseConflicts(err), models.IsErrMergeUnrelatedHistories(err):
				if err := p.remove(it, ReasonConflict, ""); err != nil {
					return err
				}
				continue
			case models.IsErrSHADoesNotMatch(err):
				if err := p.remove(it, ReasonHeadMoved, ""); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("build of %v: %w", it.pr, err)
		}
		started, err := pull_model.StartMergeQueueBuild(p.ctx, it.ID, prev, sha)
		if err != nil || !started {
			return err
		}
		it.Status, it.BaseCommitID, it.TempCommitID, it.StartedUnix = pull_model.MergeQueueBuilding, prev, sha, timeutil.TimeStampNow()
		log.Trace("Merge queue of %s in %-v: built %-v at %s", p.branch, p.repo, it.pr, sha)
		notify_service.MergeGroupChecksRequested(p.ctx, it.doer, p.repo, it.MergeQueueEntry)
		p.again = true // a build without required checks passes at once
		prev = sha
	}
	return nil
}

// deleteStaleBranches deletes the build branches of the queue that belong to no entry
func (p *pass) deleteStaleBranches() {
	entries, err := pull_model.FindMergeQueue(p.ctx, p.repo.ID, p.branch)
	if err != nil {
		log.Error("FindMergeQueue: %v", err)
		return
	}
	live := make(map[int64]bool, len(entries))
	for _, e := range entries {
		live[e.ID] = true
	}
	prefix := git.BranchPrefix + pull_model.MergeQueueBranchPrefix + p.branch + "/"
	stdout, _, err := git.NewCommand(p.ctx, "for-each-ref", "--format=%(refname)").AddDynamicArguments(prefix).RunStdString(&git.RunOpts{Dir: p.repo.RepoPath()})
	if err != nil {
		log.Error("for-each-ref %s in %-v: %v", prefix, p.repo, err)
		return
	}
	for ref := range strings.FieldsSeq(stdout) {
		id, err := strconv.ParseInt(strings.TrimPrefix(ref, prefix), 10, 64)
		if err != nil || live[id] {
			continue // another branch's build ("main" and "main/x"), or a live one
		}
		if err := git.NewCommand(p.ctx, "update-ref", "-d").AddDynamicArguments(ref).Run(&git.RunOpts{Dir: p.repo.RepoPath()}); err != nil {
			log.Error("Unable to delete %s in %-v: %v", ref, p.repo, err)
		}
	}
}

// since is the time from t to now, on the clock the tests can set
func since(t timeutil.TimeStamp) time.Duration {
	return timeutil.TimeStampNow().AsTime().Sub(t.AsTime())
}
