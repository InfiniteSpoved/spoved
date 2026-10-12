// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package asymkey

import (
	"context"

	asymkey_model "forgejo.org/models/asymkey"
	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
)

// SetOrgDeployKeyRepos changes what the deploy key of the organization covers. A repository it no longer writes
// to loses the key in its protection rules.
func SetOrgDeployKeyRepos(ctx context.Context, orgID, id int64, includesAllRepos bool, repoIDs []int64) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		key, err := asymkey_model.SetOrgDeployKeyRepos(ctx, orgID, id, includesAllRepos, repoIDs)
		if err != nil {
			return err
		}
		return git_model.DropDeployKeyIDFromOwnerRepos(ctx, orgID, key.KeyID)
	})
}

// DeleteOrgDeployKey deletes the deploy key of the organization; the repositories it no longer writes to lose it
// in their protection rules.
func DeleteOrgDeployKey(ctx context.Context, orgID, id int64) error {
	var deletedPublicKey bool
	if err := db.WithTx(ctx, func(ctx context.Context) error {
		key, err := asymkey_model.GetOrgDeployKey(ctx, orgID, id)
		if err != nil {
			return err
		}
		if deletedPublicKey, err = asymkey_model.DeleteOrgDeployKey(ctx, key); err != nil {
			return err
		}
		return git_model.DropDeployKeyIDFromOwnerRepos(ctx, orgID, key.KeyID)
	}); err != nil {
		return err
	}
	if !deletedPublicKey {
		return nil
	}
	return asymkey_model.RewriteAllPublicKeys(ctx)
}
