// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretscan

import (
	"context"
	"time"

	repo_model "forgejo.org/models/repo"
	secretscan_model "forgejo.org/models/secretscan"
	"forgejo.org/modules/log"
	"forgejo.org/services/mailer"
)

// digestMaxAge is how old a mark may be and still send its mail: a repository that was switched off for days
// would otherwise mail the alerts of whatever scan comes first after it is switched on again.
const digestMaxAge = 24 * time.Hour

// sendDigest is a variable so that tests can see it.
var sendDigest = mailer.SendSecretDigest

// MarkPush marks the repository for a digest mail after a push (the post-receive hook calls it before the push is
// handed to the notifiers, which queue the scan): the next scan that finishes mails the alerts it opened. ref and
// commit are what the mail names ("" for a wiki push). pusherID is the person who pushed, 0 for a deploy key or an
// Actions task. Nothing is marked while the repository isn't scanned or its scan isn't complete (the first scan, a
// backfill), so those never mail.
func MarkPush(ctx context.Context, repo *repo_model.Repository, pusherID int64, ref, commit string) {
	if !Enabled(ctx, repo) {
		return
	}
	if _, err := secretscan_model.MarkNotify(ctx, repo.ID, pusherID, ref, commit); err != nil {
		log.Error("secret scanning: mark repository %d for a digest: %v", repo.ID, err)
	}
}

// notifyMark is the digest mark as the scan read it before it started.
type notifyMark struct {
	seq, alertID, pusherID int64
	after                  time.Time
	ref, commit            string
}

// readMark returns the repository's digest mark, nil without one.
func readMark(ctx context.Context, repoID int64) *notifyMark {
	s, err := secretscan_model.GetScan(ctx, repoID, secretscan_model.KindRepo)
	if err != nil {
		log.Error("secret scanning: read the digest mark of repository %d: %v", repoID, err)
		return nil
	}
	if s == nil || s.NotifyAfterUnix == 0 {
		return nil
	}
	return &notifyMark{seq: s.NotifySeq, alertID: s.NotifyAlertID, pusherID: s.NotifyPusherID, after: s.NotifyAfterUnix.AsTime(), ref: s.NotifyRef, commit: s.NotifyCommit}
}

// finishDigest runs after a pass over a repository: when every scan is complete it takes the mark (once, compared
// with the sequence read before the scan started, so a push that came meanwhile keeps its mark for the next scan) and,
// only after that succeeded and nothing is in a transaction, mails the new alerts. A mail that fails to go out is not
// tried again: a digest is sent at most once.
func finishDigest(ctx context.Context, repo *repo_model.Repository, m *notifyMark) {
	if m == nil {
		return
	}
	under, err := secretscan_model.ScanUnderWay(ctx, repo.ID)
	if err != nil {
		log.Error("secret scanning: digest of repository %d: %v", repo.ID, err)
		return
	}
	if under {
		return // a slice or a retry is to come: the last one mails
	}
	took, err := secretscan_model.ClearNotify(ctx, repo.ID, m.seq)
	if err != nil {
		log.Error("secret scanning: clear the digest mark of repository %d: %v", repo.ID, err)
		return
	}
	if !took || time.Since(m.after) > digestMaxAge || !Enabled(ctx, repo) {
		return
	}
	if has, err := secretscan_model.HasAlertsAfter(ctx, repo.ID, m.alertID); err != nil || !has {
		if err != nil {
			log.Error("secret scanning: digest of repository %d: %v", repo.ID, err)
		}
		return // the push opened no alert
	}
	if err := sendDigest(ctx, repo, mailer.SecretDigestInput{Ref: m.ref, Commit: m.commit, PusherID: m.pusherID, AfterAlertID: m.alertID}); err != nil {
		log.Error("secret scanning: digest of repository %d: %v", repo.ID, err)
	}
}
