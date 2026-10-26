// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import (
	"context"
	"errors"
	"time"

	audit_model "forgejo.org/models/audit"
	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	system_model "forgejo.org/models/system"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/cache"
	"forgejo.org/modules/json"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/setting/config"
)

// The switches of the dependency graph, at three levels:
//   - site: the dynamic setting dependency_graph.enabled (Site administration), whose default is app.ini
//     [dependency_graph] ENABLED;
//   - organization: owners turn it off for every repository of the organization, or lock it, so repositories can't
//     change it (user settings repo_model.DependencyGraphOrgKey and DependencyGraphOrgLockedKey; no row = on);
//   - repository: admins turn it off (repository.dependency_graph_disabled), unless the organization locked it.
//
// Enabled is the one rule; repo_model.DependencyGraphOffCond is its SQL form. Off means: no scan, the pages and the
// SBOM answer 404, and the stored rows are deleted.

var (
	// ErrSwitchLocked is a change of a switch that a higher level decides.
	ErrSwitchLocked = errors.New("the dependency graph switch is set by a higher level")

	// purgeBatch is the number of repositories whose rows one purge transaction deletes, and purgeBatchesPerCall how
	// many a switch change deletes before it answers; the cron task takes the rest
	purgeBatch          = 100
	purgeBatchesPerCall = 50
)

// Resolve is the rule: the site switch, then the organization's, then the repository's own unless the organization
// locked it.
func Resolve(site, orgOn, orgLocked, repoOn bool) bool {
	return site && orgOn && (orgLocked || repoOn)
}

// SiteEnabled is the site switch in force.
func SiteEnabled(ctx context.Context) bool {
	return setting.Config().DependencyGraph.Value(ctx).Enabled()
}

// OrgSwitch is an organization's switch.
type OrgSwitch struct {
	On, Locked bool
}

// GetOrgSwitch reads the switch of an organization, once per request.
func GetOrgSwitch(ctx context.Context, orgID int64) (OrgSwitch, error) {
	return cache.GetWithContextCache(ctx, "dependency_graph_org", orgID, func() (OrgSwitch, error) {
		stored, err := user_model.GetSettings(ctx, orgID, []string{repo_model.DependencyGraphOrgKey, repo_model.DependencyGraphOrgLockedKey})
		if err != nil {
			return OrgSwitch{}, err
		}
		s := OrgSwitch{On: true}
		if v := stored[repo_model.DependencyGraphOrgKey]; v != nil && v.SettingValue == "false" {
			s.On = false
		}
		if v := stored[repo_model.DependencyGraphOrgLockedKey]; v != nil && v.SettingValue == "true" {
			s.Locked = true
		}
		return s, nil
	})
}

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
	s := RepoSwitch{Site: SiteEnabled(ctx), Own: !repo.DependencyGraphDisabled}
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

// Enabled tells whether the dependency graph is on for the repository. An error reads as off.
func Enabled(ctx context.Context, repo *repo_model.Repository) bool {
	if !SiteEnabled(ctx) {
		return false // no query while the site is off
	}
	s, err := GetRepoSwitch(ctx, repo)
	if err != nil {
		log.Error("dependency graph: read the switches of repository %d: %v", repo.ID, err)
		return false
	}
	return s.Enabled
}

// SetSiteEnabled stores the site switch (recorded as admin.setting.changed by SetSettings). Turning it off deletes
// the stored rows of every repository; turning it on queues the repositories that need a scan.
func SetSiteEnabled(ctx context.Context, doer *user_model.User, on bool) error {
	mode := "off"
	if on {
		mode = "on"
	}
	was := SiteEnabled(ctx)
	v, err := json.Marshal(setting.DependencyGraphState{Mode: mode, UpdatedBy: doer.ID, UpdatedUnix: time.Now().Unix()})
	if err != nil {
		return err
	}
	if err := system_model.SetSettings(ctx, map[string]string{setting.Config().DependencyGraph.DynKey(): string(v)}); err != nil {
		return err
	}
	if g := config.GetDynGetter(); g != nil {
		g.InvalidateCache()
	}
	if was != on {
		log.Info("Dependency graph turned %s by %s", mode, doer.Name)
	}
	return afterSwitch(ctx, on)
}

// SetOrgSwitch stores an organization's switch; the site must be on.
func SetOrgSwitch(ctx context.Context, org *user_model.User, on, locked bool) error {
	if !SiteEnabled(ctx) {
		return ErrSwitchLocked
	}
	err := db.WithTx(ctx, func(ctx context.Context) error {
		stored, err := user_model.GetSettings(ctx, org.ID, []string{repo_model.DependencyGraphOrgKey, repo_model.DependencyGraphOrgLockedKey})
		if err != nil {
			return err
		}
		beforeOn := stored[repo_model.DependencyGraphOrgKey] == nil || stored[repo_model.DependencyGraphOrgKey].SettingValue != "false"
		beforeLocked := stored[repo_model.DependencyGraphOrgLockedKey] != nil && stored[repo_model.DependencyGraphOrgLockedKey].SettingValue == "true"
		if on {
			err = user_model.DeleteUserSetting(ctx, org.ID, repo_model.DependencyGraphOrgKey)
		} else {
			err = user_model.SetUserSetting(ctx, org.ID, repo_model.DependencyGraphOrgKey, "false")
		}
		if err != nil {
			return err
		}
		if locked {
			err = user_model.SetUserSetting(ctx, org.ID, repo_model.DependencyGraphOrgLockedKey, "true")
		} else {
			err = user_model.DeleteUserSetting(ctx, org.ID, repo_model.DependencyGraphOrgLockedKey)
		}
		if err != nil {
			return err
		}
		if changes := audit_model.Changes(
			map[string]any{"dependency_graph": beforeOn, "dependency_graph_locked": beforeLocked},
			map[string]any{"dependency_graph": on, "dependency_graph_locked": locked},
		); changes != nil {
			return user_model.RecordOrg(ctx, "org.dependency_graph.changed", org, changes)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return afterSwitch(ctx, on)
}

// SetRepoSwitch stores the repository's own switch, unless a higher level decides. Turning it off deletes the rows
// in the same transaction.
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
		repo.DependencyGraphDisabled = !on
		if err := repo_model.RecordRepoChange(ctx, repo); err != nil {
			return err
		}
		if err := repo_model.UpdateRepositoryCols(ctx, repo, "dependency_graph_disabled"); err != nil {
			return err
		}
		if on {
			return nil
		}
		return repo_model.DeleteRepoDependencies(ctx, repo.ID)
	})
	if err != nil {
		repo.DependencyGraphDisabled = !s.Own
		return err
	}
	if on {
		Enqueue(repo.ID)
	}
	return nil
}

// afterSwitch runs after a site or organization switch was stored: off deletes the rows of the repositories that
// are now off (a bounded number of batches; the cron task deletes the rest), on queues a batch of the repositories
// that need a scan (the scans run MAX_CONCURRENT_SCANS at a time).
func afterSwitch(ctx context.Context, on bool) error {
	if on {
		return EnqueueUnscanned(ctx)
	}
	_, err := PurgeDisabled(ctx, purgeBatchesPerCall)
	return err
}

// PurgeDisabled deletes the stored rows of the repositories the dependency graph is off for (all of them while the
// site is off), in at most batches transactions; more is true when rows are left.
func PurgeDisabled(ctx context.Context, batches int) (more bool, err error) {
	cond := repo_model.DependencyGraphOffCond()
	if !SiteEnabled(ctx) {
		cond = nil // every scan row, also one whose repository is gone
	}
	for range batches {
		ids, err := repo_model.ScannedDependencyRepoIDs(ctx, cond, purgeBatch)
		if err != nil || len(ids) == 0 {
			return false, err
		}
		if err := db.WithTx(ctx, func(ctx context.Context) error {
			for _, id := range ids {
				if err := repo_model.DeleteRepoDependencies(ctx, id); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return false, err
		}
	}
	return true, nil
}
