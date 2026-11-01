// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretscan

import (
	"context"
	"slices"

	"forgejo.org/models/db"
	"forgejo.org/modules/timeutil"

	"xorm.io/builder"
)

// TextSource names the text a place is in: an issue's title or body (kind issue, source the issue ID, field "title" or
// "body") or a comment (kind comment, source the comment ID, field "").
type TextSource struct {
	Kind, Field string
	ID          int64
}

// SyncTextPlaces brings the stored places of the texts up to date with what a scan of them found now: found has, for
// each text scanned, the places (path hash and line) that still hold a secret. A stored place that is not among them is
// marked removed (the alert stays: the secret was exposed), one that was marked removed and is found again is
// restored. Only the texts named in found are looked at, in a few queries.
func SyncTextPlaces(ctx context.Context, repoID int64, found map[TextSource]map[PlaceKey]bool) error {
	byKind := map[string][]int64{}
	for src := range found {
		byKind[src.Kind] = append(byKind[src.Kind], src.ID)
	}
	var gone, back []int64
	for kind, ids := range byKind {
		for chunk := range slices.Chunk(ids, 500) {
			var rows []*Location
			if err := db.GetEngine(ctx).Where(builder.Eq{"repo_id": repoID, "kind": kind}.And(builder.In("source_id", chunk))).Find(&rows); err != nil {
				return err
			}
			for _, r := range rows {
				places, scanned := found[TextSource{Kind: kind, Field: r.Field, ID: r.SourceID}]
				if !scanned {
					continue
				}
				has := places[PlaceKey{Kind: kind, PathHash: r.PathHash, StartLine: r.StartLine}]
				switch {
				case !has && r.RemovedUnix == 0:
					gone = append(gone, r.ID)
				case has && r.RemovedUnix != 0:
					back = append(back, r.ID)
				}
			}
		}
	}
	return setRemoved(ctx, gone, int64(timeutil.TimeStampNow()), back)
}

func setRemoved(ctx context.Context, gone []int64, now int64, back []int64) error {
	for chunk := range slices.Chunk(gone, 500) {
		if _, err := db.GetEngine(ctx).In("id", chunk).Cols("removed_unix").Update(&Location{RemovedUnix: now}); err != nil {
			return err
		}
	}
	for chunk := range slices.Chunk(back, 500) {
		if _, err := db.GetEngine(ctx).In("id", chunk).Cols("removed_unix").Update(&Location{}); err != nil {
			return err
		}
	}
	return nil
}

// MarkTextGone marks the places of a deleted comment (kind comment, id of the comment) or of a deleted issue and its
// comments (kind issue, id of the issue) removed. The alerts stay open.
func MarkTextGone(ctx context.Context, repoID int64, kind string, id int64) error {
	cond := builder.Cond(builder.Eq{"repo_id": repoID, "kind": kind, "source_id": id})
	if kind == KindIssue {
		cond = builder.Eq{"repo_id": repoID, "issue_id": id}.And(builder.In("kind", TextKinds))
	}
	_, err := db.GetEngine(ctx).Where(cond.And(builder.Eq{"removed_unix": 0})).Cols("removed_unix").Update(&Location{RemovedUnix: int64(timeutil.TimeStampNow())})
	return err
}

// AddTextHoles adds to the holes of the repository's text scan row (the text over the size limit that was cut, or that
// the detector could not scan whole). A repository whose text was not scanned yet has no row: its backfill counts them.
func AddTextHoles(ctx context.Context, repoID, n int64) error {
	if n <= 0 {
		return nil
	}
	_, err := db.GetEngine(ctx).Exec("UPDATE secret_scan SET holes = holes + ? WHERE repo_id = ? AND kind = ?", n, repoID, KindIssue)
	return err
}

// readableKinds is the condition on a place's kind for the readers that show places: never the places in text (see
// TextKinds), and the wiki's only when noWiki is not set.
func readableKinds(noWiki bool) builder.Cond {
	hidden := slices.Clone(TextKinds)
	if noWiki {
		hidden = append(hidden, KindWiki)
	}
	return builder.NotIn("kind", hidden)
}
