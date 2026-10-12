// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package asymkey

import (
	"context"
	"time"

	"forgejo.org/models/db"
	"forgejo.org/models/perm"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/container"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"

	"xorm.io/builder"
)

// OrgDeployKey is a deploy key of an organization: it covers all its repositories or the selected ones
// (OrgDeployKeyRepo). It shares the PublicKey (KeyTypeDeploy) with repository deploy keys of the same key.
type OrgDeployKey struct {
	ID               int64 `xorm:"pk autoincr"`
	KeyID            int64 `xorm:"UNIQUE(s) INDEX"`
	OrgID            int64 `xorm:"UNIQUE(s) INDEX"`
	Name             string
	Fingerprint      string
	Mode             perm.AccessMode    `xorm:"NOT NULL DEFAULT 1"`
	IncludesAllRepos bool               `xorm:"NOT NULL DEFAULT false"`
	ExpiresUnix      timeutil.TimeStamp `xorm:"NOT NULL DEFAULT 0"` // 0 = never

	CreatedUnix       timeutil.TimeStamp `xorm:"created"`
	UpdatedUnix       timeutil.TimeStamp `xorm:"updated"`
	HasRecentActivity bool               `xorm:"-"`
	HasUsed           bool               `xorm:"-"`
}

// OrgDeployKeyRepo is a repository selected for an organization deploy key.
type OrgDeployKeyRepo struct {
	ID             int64 `xorm:"pk autoincr"`
	OrgDeployKeyID int64 `xorm:"UNIQUE(s) INDEX"`
	RepoID         int64 `xorm:"UNIQUE(s) INDEX"`
}

func init() {
	db.RegisterModel(new(OrgDeployKey))
	db.RegisterModel(new(OrgDeployKeyRepo))
}

// AfterLoad is invoked from XORM after setting the values of all fields of this object.
func (key *OrgDeployKey) AfterLoad() {
	key.HasUsed = key.UpdatedUnix > key.CreatedUnix
	key.HasRecentActivity = key.UpdatedUnix.AddDuration(7*24*time.Hour) > timeutil.TimeStampNow()
}

// IsExpired reports whether the key has an expiry date that has passed.
func (key *OrgDeployKey) IsExpired() bool {
	return key.ExpiresUnix != 0 && timeutil.TimeStampNow() >= key.ExpiresUnix
}

// AddOrgDeployKey adds a deploy key to the organization, for all its repositories or the selected ones.
func AddOrgDeployKey(ctx context.Context, orgID int64, name, content string, readOnly, includesAllRepos bool, repoIDs []int64, expires timeutil.TimeStamp) (*OrgDeployKey, error) {
	fingerprint, err := CalcFingerprint(content)
	if err != nil {
		return nil, err
	}
	mode := perm.AccessModeRead
	if !readOnly {
		mode = perm.AccessModeWrite
	}

	var key *OrgDeployKey
	err = db.WithTx(ctx, func(ctx context.Context) error {
		if isOrg, err := db.GetEngine(ctx).Table("user").
			Where("id = ? AND type = ? AND trashed_unix = 0", orgID, user_model.UserTypeOrganization).Exist(); err != nil {
			return err
		} else if !isOrg {
			return util.NewInvalidArgumentErrorf("%d is not an organization", orgID)
		}
		repoIDs = container.SetOf(repoIDs...).Values()
		if err := checkOrgDeployKeyRepos(ctx, orgID, repoIDs); err != nil {
			return err
		}
		if has, err := db.GetEngine(ctx).Where("org_id = ? AND name = ?", orgID, name).Exist(new(OrgDeployKey)); err != nil {
			return err
		} else if has {
			return util.NewAlreadyExistErrorf("the organization already has a deploy key named %q", name)
		}

		pkey, err := getOrAddDeployPublicKey(ctx, name, content, fingerprint, mode)
		if err != nil {
			return err
		}
		if has, err := db.GetEngine(ctx).Where("org_id = ? AND key_id = ?", orgID, pkey.ID).Exist(new(OrgDeployKey)); err != nil {
			return err
		} else if has {
			return util.NewAlreadyExistErrorf("the organization already has this deploy key")
		}

		key = &OrgDeployKey{
			KeyID:            pkey.ID,
			OrgID:            orgID,
			Name:             name,
			Fingerprint:      fingerprint,
			Mode:             mode,
			IncludesAllRepos: includesAllRepos,
			ExpiresUnix:      expires,
		}
		if err := db.Insert(ctx, key); err != nil {
			return err
		}
		for _, repoID := range repoIDs {
			if err := db.Insert(ctx, &OrgDeployKeyRepo{OrgDeployKeyID: key.ID, RepoID: repoID}); err != nil {
				return err
			}
		}
		return nil
	})
	return key, err
}

// checkOrgDeployKeyRepos refuses repositories that aren't live repositories of the organization.
func checkOrgDeployKeyRepos(ctx context.Context, orgID int64, repoIDs []int64) error {
	if len(repoIDs) == 0 {
		return nil
	}
	n, err := db.GetEngine(ctx).Table("repository").In("id", repoIDs).
		And("owner_id = ? AND trashed_unix = 0", orgID).Count()
	if err != nil {
		return err
	} else if n != int64(len(repoIDs)) {
		return util.NewInvalidArgumentErrorf("a selected repository is not a repository of the organization")
	}
	return nil
}

// EffectiveDeployKey is the deploy key that applies when a public key is used on a repository:
// exactly one of RepoKey and OrgKey is set.
type EffectiveDeployKey struct {
	KeyID       int64 // public key ID
	Name        string
	Mode        perm.AccessMode
	ExpiresUnix timeutil.TimeStamp
	RepoKey     *DeployKey
	OrgKey      *OrgDeployKey
}

// IsExpired reports whether the key has an expiry date that has passed.
func (key *EffectiveDeployKey) IsExpired() bool {
	return key.ExpiresUnix != 0 && timeutil.TimeStampNow() >= key.ExpiresUnix
}

// GetEffectiveDeployKey returns the deploy key the public key has on the repository: its deploy key of the
// repository or a deploy key of the repository owner that covers it. Unexpired keys come first, then the higher
// mode, then the repository key. An expired key is only returned when no other applies.
func GetEffectiveDeployKey(ctx context.Context, keyID int64, repo *repo_model.Repository) (*EffectiveDeployKey, error) {
	var candidates []*EffectiveDeployKey
	if key, has, err := db.Get[DeployKey](ctx, builder.Eq{"key_id": keyID, "repo_id": repo.ID}); err != nil {
		return nil, err
	} else if has {
		candidates = append(candidates, &EffectiveDeployKey{KeyID: keyID, Name: key.Name, Mode: key.Mode, ExpiresUnix: key.ExpiresUnix, RepoKey: key})
	}
	if key, has, err := db.Get[OrgDeployKey](ctx, builder.Eq{"key_id": keyID}.And(orgDeployKeyCoversCond(repo))); err != nil {
		return nil, err
	} else if has {
		candidates = append(candidates, &EffectiveDeployKey{KeyID: keyID, Name: key.Name, Mode: key.Mode, ExpiresUnix: key.ExpiresUnix, OrgKey: key})
	}

	var best *EffectiveDeployKey
	for _, c := range candidates {
		if best == nil || best.IsExpired() && !c.IsExpired() || best.IsExpired() == c.IsExpired() && c.Mode > best.Mode {
			best = c
		}
	}
	if best == nil {
		return nil, ErrDeployKeyNotExist{0, keyID, repo.ID}
	}
	return best, nil
}

// FindOrgDeployKeysCoveringRepo returns the deploy keys of the repository owner that cover the repository.
func FindOrgDeployKeysCoveringRepo(ctx context.Context, repo *repo_model.Repository) ([]*OrgDeployKey, error) {
	keys := make([]*OrgDeployKey, 0, 2)
	return keys, db.GetEngine(ctx).Where(orgDeployKeyCoversCond(repo)).Find(&keys)
}

// orgDeployKeyCoversCond matches the deploy keys of the repository owner that cover the repository.
// org_id = the current owner: a key never covers a repository of another owner, whatever is selected.
func orgDeployKeyCoversCond(repo *repo_model.Repository) builder.Cond {
	return builder.Eq{"org_id": repo.OwnerID}.And(builder.Or(
		builder.Eq{"includes_all_repos": true},
		builder.In("id", builder.Select("org_deploy_key_id").From("org_deploy_key_repo").Where(builder.Eq{"repo_id": repo.ID})),
	))
}

// DeleteOrgDeployKeys deletes the deploy keys of the organization and the public keys no other deploy key uses.
// It reports whether a public key was deleted, so the caller rewrites the authorized_keys file.
func DeleteOrgDeployKeys(ctx context.Context, orgID int64) (deletedPublicKey bool, err error) {
	keys := make([]*OrgDeployKey, 0, 2)
	if err := db.GetEngine(ctx).Where("org_id = ?", orgID).Find(&keys); err != nil {
		return false, err
	}
	for _, key := range keys {
		if _, err := db.GetEngine(ctx).Where("org_deploy_key_id = ?", key.ID).Delete(new(OrgDeployKeyRepo)); err != nil {
			return false, err
		}
		if _, err := db.DeleteByID[OrgDeployKey](ctx, key.ID); err != nil {
			return false, err
		}
		if has, err := IsDeployKeyExistByKeyID(ctx, key.KeyID); err != nil {
			return false, err
		} else if !has {
			if _, err := db.DeleteByID[PublicKey](ctx, key.KeyID); err != nil {
				return false, err
			}
			deletedPublicKey = true
		}
	}
	return deletedPublicKey, nil
}
