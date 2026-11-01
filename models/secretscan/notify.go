// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretscan

import (
	"context"

	"forgejo.org/models/db"
	"forgejo.org/modules/timeutil"

	"xorm.io/builder"
)

// MaxDigestLocations is the most places a digest mail lists.
const MaxDigestLocations = 10

// MarkNotify marks the repository for a digest mail: a push to it, by pusherID (0 when no person pushed: a deploy
// key, an Actions task), ref and commit as shown in the mail. Only a repository whose scans are all complete is
// marked, so the first scan, a backfill and a rescan (which removes the rows) never mail. A second push before the
// digest keeps the first mark's alert boundary and time, takes the newer ref and commit, and keeps the pusher only
// when it is the same one (else 0: nobody is told "your push"). Every call bumps notify_seq, so the clear of a scan
// that started before the push is refused. It reports whether a row was marked.
func MarkNotify(ctx context.Context, repoID, pusherID int64, ref, commit string) (bool, error) {
	if under, err := ScanUnderWay(ctx, repoID); err != nil || under {
		return false, err
	}
	// the assignments run in this order: MySQL reads the columns as already assigned, so notify_after_unix is last
	res, err := db.GetEngine(ctx).Exec(
		"UPDATE secret_scan SET notify_seq = notify_seq + 1,"+
			" notify_alert_id = CASE WHEN notify_after_unix = 0 THEN (SELECT COALESCE(MAX(id), 0) FROM secret_scanning_alert WHERE repo_id = ?) ELSE notify_alert_id END,"+
			" notify_pusher_id = CASE WHEN notify_after_unix = 0 OR notify_pusher_id = ? THEN ? ELSE 0 END,"+
			" notify_ref = ?, notify_commit = ?,"+
			" notify_after_unix = CASE WHEN notify_after_unix = 0 THEN ? ELSE notify_after_unix END"+
			" WHERE repo_id = ? AND kind = ? AND retry_unix = 0",
		repoID, pusherID, pusherID, ref, commit, int64(timeutil.TimeStampNow()), repoID, KindRepo,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ScanUnderWay tells whether a scan of the repository (its code, wiki or pull refs) is not complete: a slice stopped
// at a limit or a failure waiting out its backoff.
func ScanUnderWay(ctx context.Context, repoID int64) (bool, error) {
	return db.GetEngine(ctx).Where("repo_id = ? AND retry_unix > 0", repoID).Exist(new(Scan))
}

// ClearNotify removes the mark if no push marked the repository since seq was read, and reports whether it did:
// only then the caller sends the mail, so a digest is sent at most once and a push during the scan keeps its mark
// for the next scan.
func ClearNotify(ctx context.Context, repoID, seq int64) (bool, error) {
	res, err := db.GetEngine(ctx).Exec(
		"UPDATE secret_scan SET notify_after_unix = 0, notify_pusher_id = 0, notify_ref = '', notify_commit = '' WHERE repo_id = ? AND kind = ? AND notify_seq = ? AND notify_after_unix <> 0",
		repoID, KindRepo, seq,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// HasAlertsAfter tells whether the repository has an alert above the boundary a mark recorded: no digest is built
// for a push that opened none.
func HasAlertsAfter(ctx context.Context, repoID, afterAlertID int64) (bool, error) {
	return db.GetEngine(ctx).Where(builder.Eq{"repo_id": repoID}.And(builder.Gt{"id": afterAlertID})).Exist(new(Alert))
}

// DigestType is the number of new alerts of one secret type.
type DigestType struct {
	RuleID string
	Count  int
}

// Digest is what a digest mail says: the new alerts (above afterAlertID) a viewer can see, by type, and their first
// places. No fingerprint is read.
type Digest struct {
	Alerts         int
	Types          []DigestType
	Locations      []*Location // at most MaxDigestLocations, in the order found
	TotalLocations int         // every place of the alerts, also those over the cap that have no row
}

// BuildDigest reads the digest of the alerts created after afterAlertID. With noWiki (the viewer can't read the
// wiki) an alert that is only in the wiki is not counted and the wiki's places are left out.
func BuildDigest(ctx context.Context, repoID, afterAlertID int64, noWiki bool) (*Digest, error) {
	opts := &AlertSearchOptions{RepoID: repoID, CodeOnly: noWiki}
	alerts := opts.ToConds().And(builder.Gt{"id": afterAlertID})
	var rows []struct {
		RuleID string
		N      int
		Places int
	}
	if err := db.GetEngine(ctx).Table("secret_scanning_alert").Select("rule_id, COUNT(*) AS n, SUM(location_count) AS places").
		Where(alerts).GroupBy("rule_id").OrderBy("n DESC, rule_id").Find(&rows); err != nil {
		return nil, err
	}
	d := &Digest{}
	for _, r := range rows {
		d.Types = append(d.Types, DigestType{RuleID: r.RuleID, Count: r.N})
		d.Alerts += r.N
		d.TotalLocations += r.Places
	}
	if d.Alerts == 0 {
		return d, nil
	}
	in := builder.In("alert_id", builder.Select("id").From("secret_scanning_alert").Where(alerts))
	visible := builder.NewCond().And(in)
	if noWiki {
		visible = visible.And(builder.Neq{"kind": KindWiki})
		wiki, err := db.GetEngine(ctx).Where(builder.And(in, builder.Eq{"kind": KindWiki})).Count(new(Location))
		if err != nil {
			return nil, err
		}
		d.TotalLocations -= int(wiki)
	}
	return d, db.GetEngine(ctx).Where(visible).OrderBy("id").Limit(MaxDigestLocations).Find(&d.Locations)
}
