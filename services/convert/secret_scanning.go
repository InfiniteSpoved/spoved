// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package convert

import (
	"context"
	"strconv"
	"time"

	repo_model "forgejo.org/models/repo"
	secretscan_model "forgejo.org/models/secretscan"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/secretdetection"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/util"
)

// ToSecretScanningAlerts converts alerts of one repository (one query for the users who closed them). locs, by
// alert ID, are the places to list; wikiURL turns the path of a wiki place into its page URL.
func ToSecretScanningAlerts(ctx context.Context, doer *user_model.User, repo *repo_model.Repository, alerts []*secretscan_model.Alert, locs map[int64][]*secretscan_model.Location, wikiURL func(path string) string) ([]*api.SecretScanningAlert, error) {
	var userIDs []int64
	for _, a := range alerts {
		if a.ClosedByID != 0 {
			userIDs = append(userIDs, a.ClosedByID)
		}
	}
	users, err := user_model.GetUsersByIDsWithBots(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	out := make([]*api.SecretScanningAlert, 0, len(alerts))
	for _, a := range alerts {
		res := &api.SecretScanningAlert{
			Number: a.Index, State: a.State, Resolution: a.Reason, ResolutionComment: a.Comment, RuleID: a.RuleID,
			SecretType: a.SecretType, SecretTypeDisplayName: a.RuleID, Fingerprint: a.Fingerprint,
			CreatedAt: time.Unix(int64(a.CreatedUnix), 0).UTC(), UpdatedAt: time.Unix(int64(a.UpdatedUnix), 0).UTC(),
			HTMLURL: repo.HTMLURL() + "/security/secrets/" + strconv.FormatInt(a.Index, 10), LocationsCount: a.LocationCount,
		}
		if rule, ok := secretdetection.RuleByID(a.RuleID); ok {
			res.SecretTypeDisplayName = rule.Name
		}
		if a.ClosedByID != 0 {
			_, u := user_model.GetUserFromMap(a.ClosedByID, users)
			res.ResolvedBy = ToUser(ctx, u, doer)
		}
		if a.ClosedUnix != 0 {
			t := time.Unix(int64(a.ClosedUnix), 0).UTC()
			res.ResolvedAt = &t
		}
		for _, l := range locs[a.ID] {
			res.Locations = append(res.Locations, toSecretScanningLocation(repo, l, wikiURL))
		}
		out = append(out, res)
	}
	return out, nil
}

func toSecretScanningLocation(repo *repo_model.Repository, l *secretscan_model.Location, wikiURL func(string) string) *api.SecretScanningLocation {
	res := &api.SecretScanningLocation{
		Kind: l.Kind, Path: l.Path, StartLine: l.StartLine, EndLine: l.EndLine, CommitSHA: l.CommitID, BlobSHA: l.BlobID,
		CommitAuthorName: l.AuthorName, CommitAuthorEmail: l.AuthorEmail,
	}
	if l.CommitUnix != 0 {
		t := time.Unix(l.CommitUnix, 0).UTC()
		res.CommitDate = &t
	}
	switch {
	case l.Kind == secretscan_model.KindWiki:
		res.HTMLURL = wikiURL(l.Path)
	case l.CommitID != "":
		res.HTMLURL = repo.HTMLURL() + "/src/commit/" + l.CommitID + "/" + util.PathEscapeSegments(l.Path) + "#L" + strconv.Itoa(l.StartLine)
	}
	return res
}
