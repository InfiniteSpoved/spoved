// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// Package secretscan looks for secrets in the git objects of every repository and its wiki and stores alerts
// (models/secretscan). A scan reads the objects reachable from all refs that the stored scan state doesn't cover, so the
// first scan reads the whole history and every later one only what is new. Scans run from a unique queue of
// repository IDs, started by pushes, pull requests, migrations, forks, transfers and wiki changes, and by a cron task
// for repositories that were never scanned.
package secretscan

import (
	"context"
	"errors"
	"sync"
	"time"

	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	secretscan_model "forgejo.org/models/secretscan"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/graceful"
	"forgejo.org/modules/log"
	"forgejo.org/modules/queue"
	"forgejo.org/modules/repository"
	"forgejo.org/modules/timeutil"
	notify_service "forgejo.org/services/notify"
)

const (
	// failedRetry is how long the cron task leaves a repository alone after a failed scan
	failedRetry = 24 * time.Hour
	cronBatch   = 500
)

var (
	scanQueue *queue.WorkerPoolQueue[int64]
	// scanSlots counts the scans running in this process (acquireSlot); secret scans have their own, so a long
	// backfill never holds up the dependency graph scans
	scanSlots struct {
		sync.Mutex
		running int
		freed   chan struct{} // closed and replaced when a slot is given back
	}

	// repoScans holds the repositories being scanned in this process: the unique queue forgets an ID once it is
	// popped, so a push during a scan queues the repository again, and the second scan must wait for the first.
	repoScans = struct {
		sync.Mutex
		running map[int64]*repoScan
	}{running: map[int64]*repoScan{}}
)

type repoScan struct {
	mu   sync.Mutex
	refs int
}

// lockRepoScan waits until no other scan of the repository runs in this process. Take it before a slot. The caller
// holds no database connection or transaction while it waits.
func lockRepoScan(repoID int64) (unlock func()) {
	repoScans.Lock()
	rs := repoScans.running[repoID]
	if rs == nil {
		rs = &repoScan{}
		repoScans.running[repoID] = rs
	}
	rs.refs++
	repoScans.Unlock()
	rs.mu.Lock()
	return func() {
		rs.mu.Unlock()
		repoScans.Lock()
		if rs.refs--; rs.refs == 0 {
			delete(repoScans.running, repoID)
		}
		repoScans.Unlock()
	}
}

// Init starts the queue and listens for changes. Both are always set up, whatever the switches say: they are read
// when something happens and again when a scan runs, so turning secret scanning on or off needs no restart.
func Init() error {
	notify_service.RegisterNotifier(&notifier{})
	scanQueue = queue.CreateUniqueQueue(graceful.GetManager().ShutdownContext(), "secret_scanning", handler)
	if scanQueue == nil {
		return errors.New("unable to create secret_scanning queue")
	}
	go graceful.GetManager().RunWithCancel(scanQueue)
	return nil
}

type notifier struct {
	notify_service.NullNotifier
}

func (*notifier) PushCommits(ctx context.Context, _ *user_model.User, repo *repo_model.Repository, _ *repository.PushUpdateOptions, _ *repository.PushCommits) {
	EnqueueIfEnabled(ctx, repo)
}

func (*notifier) SyncPushCommits(ctx context.Context, _ *user_model.User, repo *repo_model.Repository, _ *repository.PushUpdateOptions, _ *repository.PushCommits) {
	EnqueueIfEnabled(ctx, repo)
}

// MigrateRepository: a migrated or mirrored repository arrives without a push.
func (*notifier) MigrateRepository(ctx context.Context, _, _ *user_model.User, repo *repo_model.Repository) {
	EnqueueIfEnabled(ctx, repo)
}

// TransferRepository: the new owner's switch decides now; a scan that started before the transfer is discarded.
func (*notifier) TransferRepository(ctx context.Context, _ *user_model.User, repo *repo_model.Repository, _ string) {
	EnqueueIfEnabled(ctx, repo)
}

// ForkRepository: a fork shares every object of its parent, so when the parent's scan is complete the fork starts
// from the parent's scanned tips instead of reading the whole history again. The fork gets no alerts of its own for
// what the parent already holds.
func (*notifier) ForkRepository(ctx context.Context, _ *user_model.User, parent, fork *repo_model.Repository) {
	if !Enabled(ctx, fork) {
		return
	}
	if p, err := secretscan_model.GetScan(ctx, parent.ID, secretscan_model.KindRepo); err != nil {
		log.Error("secret scanning: read the scan state of repository %d: %v", parent.ID, err)
	} else if p != nil && p.Status == secretscan_model.StatusOK {
		s := &secretscan_model.Scan{RepoID: fork.ID, Kind: secretscan_model.KindRepo, ScannedTips: p.ScannedTips, Status: secretscan_model.StatusOK}
		if err := secretscan_model.Save(ctx, nil, s); err != nil && !errors.Is(err, secretscan_model.ErrScanMoved) {
			log.Error("secret scanning: store the inherited scan state of repository %d: %v", fork.ID, err)
		}
	}
	Enqueue(fork.ID)
}

// NewPullRequest and PullRequestSynchronized: the head of a pull request from a fork, and of an AGit push, is a ref
// of the base repository that no push event names.
func (*notifier) NewPullRequest(ctx context.Context, pr *issues_model.PullRequest, _ []*user_model.User) {
	enqueuePullRequest(ctx, pr)
}

func (*notifier) PullRequestSynchronized(ctx context.Context, _ *user_model.User, pr *issues_model.PullRequest) {
	enqueuePullRequest(ctx, pr)
}

func enqueuePullRequest(ctx context.Context, pr *issues_model.PullRequest) {
	if err := pr.LoadBaseRepo(ctx); err != nil {
		log.Error("secret scanning: load the base repository of pull request %d: %v", pr.ID, err)
		return
	}
	EnqueueIfEnabled(ctx, pr.BaseRepo)
}

// NewWikiPage and EditWikiPage: a web edit of the wiki. A git push to the wiki calls EnqueueIfEnabled from the hook.
func (*notifier) NewWikiPage(ctx context.Context, _ *user_model.User, repo *repo_model.Repository, _, _ string) {
	EnqueueIfEnabled(ctx, repo)
}

func (*notifier) EditWikiPage(ctx context.Context, _ *user_model.User, repo *repo_model.Repository, _, _ string) {
	EnqueueIfEnabled(ctx, repo)
}

// EnqueueIfEnabled queues a scan of the repository when secret scanning is on for it.
func EnqueueIfEnabled(ctx context.Context, repo *repo_model.Repository) {
	if Enabled(ctx, repo) {
		Enqueue(repo.ID)
	}
}

// Enqueue queues a scan of the repository. A repository that is already queued isn't queued twice. It does nothing
// while the site switch is off; the scan checks every switch (Enabled) again before any work.
func Enqueue(repoID int64) {
	if scanQueue == nil || !SiteEnabled(graceful.GetManager().ShutdownContext()) {
		return
	}
	if err := scanQueue.Push(repoID); err != nil && !errors.Is(err, queue.ErrAlreadyInQueue) {
		log.Error("secret scanning: queue repository %d: %v", repoID, err)
	}
}

func handler(items ...int64) (unhandled []int64) {
	ctx := graceful.GetManager().ShutdownContext()
	for _, id := range items {
		if err := Scan(ctx, id); err != nil {
			if ctx.Err() != nil {
				unhandled = append(unhandled, id) // shutting down: kept for the next start
				continue
			}
			log.Error("secret scanning: scan of repository %d: %v", id, err)
		}
	}
	return unhandled
}

// EnqueueUnscanned queues the repositories that have no scan state or whose last scan failed more than a day ago, in
// batches of 500 per call (the next call takes the next ones once these are scanned). An incomplete scan is not
// retried: the budget that ended it would end it again. Repositories secret scanning is off for are left out.
func EnqueueUnscanned(ctx context.Context) error {
	if !SiteEnabled(ctx) {
		return nil
	}
	ids, err := secretscan_model.UnscannedRepoIDs(ctx, timeutil.TimeStampNow().Add(-int64(failedRetry/time.Second)), nil, cronBatch)
	if err != nil {
		return err
	}
	for _, id := range ids {
		Enqueue(id)
	}
	return nil
}
