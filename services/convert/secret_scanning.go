// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package convert

import (
	"cmp"
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

// ToSecretScanningAlerts converts alerts of one repository (one query for the users who closed them, one for the
// places). The count is the one the locations endpoint gives the viewer: without canReadWiki, places in the wiki
// are not in it.
func ToSecretScanningAlerts(ctx context.Context, doer *user_model.User, repo *repo_model.Repository, alerts []*secretscan_model.Alert, canReadWiki bool) ([]*api.SecretScanningAlert, error) {
	var userIDs, alertIDs []int64
	for _, a := range alerts {
		alertIDs = append(alertIDs, a.ID)
		if a.ClosedByID != 0 {
			userIDs = append(userIDs, a.ClosedByID)
		}
	}
	users, err := user_model.GetUsersByIDsWithBots(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	counts, err := secretscan_model.CountLocations(ctx, repo.ID, alertIDs, !canReadWiki)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, r := range secretdetection.BuiltinRules() {
		names[r.ID] = r.Name
	}
	out := make([]*api.SecretScanningAlert, 0, len(alerts))
	for _, a := range alerts {
		index := strconv.FormatInt(a.Index, 10)
		res := &api.SecretScanningAlert{
			Number: a.Index, State: secretscan_model.StateOpen, SecretType: a.RuleID, SecretTypeDisplayName: cmp.Or(names[a.RuleID], a.RuleID),
			Fingerprint: a.Fingerprint, ViaPull: a.ViaPull, LocationsCount: counts[a.ID],
			URL:       repo.APIURL() + "/secret-scanning/alerts/" + index,
			CreatedAt: time.Unix(int64(a.CreatedUnix), 0).UTC(), UpdatedAt: time.Unix(int64(a.UpdatedUnix), 0).UTC(),
			HTMLURL: repo.HTMLURL() + "/security/secrets/" + index, LocationsURL: repo.APIURL() + "/secret-scanning/alerts/" + index + "/locations",
		}
		if a.State != secretscan_model.StateOpen {
			res.State, res.Resolution, res.ResolutionComment = "resolved", a.Reason, a.Comment
		}
		if a.ClosedByID != 0 {
			_, u := user_model.GetUserFromMap(a.ClosedByID, users)
			res.ResolvedBy = ToUser(ctx, u, doer)
		}
		if a.ClosedUnix != 0 {
			t := time.Unix(int64(a.ClosedUnix), 0).UTC()
			res.ResolvedAt = &t
		}
		out = append(out, res)
	}
	return out, nil
}

// ToSecretScanningLocations converts places of an alert of the repository, in GitHub's shape; wikiURL turns the
// path of a wiki place into its page URL.
func ToSecretScanningLocations(repo *repo_model.Repository, locs []*secretscan_model.Location, wikiURL func(path string) string) []*api.SecretScanningLocation {
	out := make([]*api.SecretScanningLocation, 0, len(locs))
	for _, l := range locs {
		res := &api.SecretScanningLocation{Type: "commit", Details: api.SecretScanningLocationDetails{
			Path: l.Path, StartLine: l.StartLine, EndLine: l.EndLine, BlobSHA: l.BlobID, CommitSHA: l.CommitID,
			CommitAuthorName: l.AuthorName, CommitAuthorEmail: l.AuthorEmail, PullRef: l.Kind == secretscan_model.KindPull,
		}}
		if l.CommitUnix != 0 {
			t := time.Unix(l.CommitUnix, 0).UTC()
			res.Details.CommitDate = &t
		}
		switch {
		case l.Kind == secretscan_model.KindWiki:
			res.Type, res.Details.PageURL = "wiki_commit", wikiURL(l.Path)
		case l.CommitID != "":
			res.Details.BlobURL = repo.APIURL() + "/git/blobs/" + l.BlobID
			res.Details.CommitURL = repo.APIURL() + "/git/commits/" + l.CommitID
			res.Details.HTMLURL = repo.HTMLURL() + "/src/commit/" + l.CommitID + "/" + util.PathEscapeSegments(l.Path) + "#L" + strconv.Itoa(l.StartLine)
			if l.EndLine > l.StartLine {
				res.Details.HTMLURL += "-L" + strconv.Itoa(l.EndLine)
			}
		}
		out = append(out, res)
	}
	return out
}

// ToSecretScanningStatus sums up the state rows of a repository's code, wiki and pull refs: failed beats incomplete
// beats ok (the reason is the code of the row that decides), holes and skipped files add up, and no row is pending.
// The scanned tips never leave the model.
func ToSecretScanningStatus(scans []*secretscan_model.Scan) *api.SecretScanningStatus {
	res := &api.SecretScanningStatus{Status: "pending"}
	rank := map[string]int{secretscan_model.StatusOK: 1, secretscan_model.StatusIncomplete: 2, secretscan_model.StatusFailed: 3}
	best := 0
	for _, s := range scans {
		res.Holes += s.Holes
		res.SkippedLarge += s.SkippedLarge
		if t := s.UpdatedUnix.AsTimePtr(); s.Status != secretscan_model.StatusFailed && t != nil && (res.LastScanned == nil || t.After(*res.LastScanned)) {
			res.LastScanned = t
		}
		if r := rank[s.Status]; r > best {
			best, res.Status, res.Reason = r, s.Status, s.Error
		}
	}
	return res
}
