// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretscan

import (
	"context"
	"errors"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	secretscan_model "forgejo.org/models/secretscan"
)

var (
	// ErrSwitchLocked is a change of a switch that a higher level decides.
	ErrSwitchLocked = errors.New("the secret scanning switch is set by a higher level")
	// ErrNotEnabled is a rescan of a repository secret scanning is off for.
	ErrNotEnabled = errors.New("secret scanning is off for the repository")
	// ErrRescanBusy is a rescan while the one before is queued or running: a rescan deletes the scan state, so a
	// repository without state has no finished scan.
	ErrRescanBusy = errors.New("a scan of the repository is queued or running")
)

// RepoSwitch is the state of the switches for a repository.
type RepoSwitch struct {
	Site    bool
	Org     *OrgSwitch // nil for a personal repository
	Own     bool       // the repository's own setting
	Enabled bool
}

// CanChange reports whether the repository's own setting decides: the site and the organization are on and the
// organization doesn't lock it.
func (s RepoSwitch) CanChange() bool {
	return s.Site && (s.Org == nil || s.Org.On && !s.Org.Locked)
}

// GetRepoSwitch reads every level for a repository.
func GetRepoSwitch(ctx context.Context, repo *repo_model.Repository) (RepoSwitch, error) {
	s := RepoSwitch{Site: SiteEnabled(ctx), Own: !repo.SecretScanningDisabled}
	if err := repo.LoadOwner(ctx); err != nil {
		return s, err
	}
	org := OrgSwitch{On: true}
	if repo.Owner.IsOrganization() {
		var err error
		if org, err = GetOrgSwitch(ctx, repo.OwnerID); err != nil {
			return s, err
		}
		s.Org = &org
	}
	s.Enabled = Resolve(s.Site, org.On, org.Locked, s.Own)
	return s, nil
}

// SetRepoSwitch stores the repository's own switch and audits the change. Turning it off keeps the alerts; turning
// it on queues a scan. While the site or the organization decides, it returns ErrSwitchLocked.
func SetRepoSwitch(ctx context.Context, repo *repo_model.Repository, on bool) error {
	s, err := GetRepoSwitch(ctx, repo)
	if err != nil {
		return err
	}
	if !s.CanChange() {
		return ErrSwitchLocked
	}
	if s.Own == on {
		return nil
	}
	err = db.WithTx(ctx, func(ctx context.Context) error {
		repo.SecretScanningDisabled = !on
		if err := repo_model.RecordRepoChange(ctx, repo); err != nil {
			return err
		}
		return repo_model.UpdateRepositoryCols(ctx, repo, "secret_scanning_disabled")
	})
	if err != nil {
		repo.SecretScanningDisabled = !s.Own
		return err
	}
	if on {
		Enqueue(repo.ID)
	}
	return nil
}

// Rescan forgets what was scanned (and so the holes) and scans the repository from the beginning; the alerts stay.
// The scan runs only while secret scanning is on.
func Rescan(ctx context.Context, repo *repo_model.Repository) error {
	if !Enabled(ctx, repo) {
		return ErrNotEnabled
	}
	err := db.WithTx(ctx, func(ctx context.Context) error {
		// the delete is the claim: of two rescans at once only one deletes a row; a scan already running finds its
		// state row gone and its write fails (secretscan_model.Save: ErrScanMoved), so it is queued again, never saved
		if n, err := secretscan_model.DeleteScans(ctx, repo.ID); err != nil {
			return err
		} else if n == 0 {
			return ErrRescanBusy
		}
		// the action key is a literal: TestActionCatalog finds it in the code
		return repo_model.RecordRepo(ctx, "repo.secret_scanning.rescanned", repo, map[string]any{})
	})
	if err != nil {
		return err
	}
	Enqueue(repo.ID)
	return nil
}
