// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package org

import (
	"context"
	"fmt"

	"forgejo.org/models"
	asymkey_model "forgejo.org/models/asymkey"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	org_model "forgejo.org/models/organization"
	packages_model "forgejo.org/models/packages"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/storage"
	"forgejo.org/modules/util"
	repo_service "forgejo.org/services/repository"

	"xorm.io/builder"
)

// DeleteOrganization completely and permanently deletes everything of organization.
func DeleteOrganization(ctx context.Context, org *org_model.Organization, purge bool) error {
	if purge {
		err := repo_service.DeleteOwnerRepositoriesDirectly(ctx, org.AsUser())
		if err != nil {
			return err
		}
	}

	ctx, committer, err := db.TxContext(ctx)
	if err != nil {
		return err
	}
	defer committer.Close()

	// Check ownership of repository.
	// repositories in the trash count on purpose: the owner can only be deleted once they are purged
	count, err := repo_model.CountRepositories(ctx, repo_model.CountRepositoryOptions{OwnerID: org.ID})
	if err != nil {
		return fmt.Errorf("GetRepositoryCount: %w", err)
	} else if count > 0 {
		return models.ErrUserOwnRepos{UID: org.ID}
	}

	// Check ownership of packages.
	if ownsPackages, err := packages_model.HasOwnerPackages(ctx, org.ID); err != nil {
		return fmt.Errorf("HasOwnerPackages: %w", err)
	} else if ownsPackages {
		return models.ErrUserOwnPackages{UID: org.ID}
	}

	counts, err := deleteCounts(ctx, org.ID)
	if err != nil {
		return err
	}
	if err := user_model.RecordOrg(ctx, "org.organization.deleted", org.AsUser(), map[string]any{
		"counts": counts, "purge": purge || org.TrashedUnix != 0, // deleting it from the trash purges it
	}); err != nil {
		return err
	}

	if err := git_model.DeleteOrgProtectedBranches(ctx, org.ID); err != nil {
		return fmt.Errorf("DeleteOrgProtectedBranches: %w", err)
	}

	deletedPublicKey, err := asymkey_model.DeleteOrgDeployKeys(ctx, org.ID)
	if err != nil {
		return fmt.Errorf("DeleteOrgDeployKeys: %w", err)
	}

	if err := org_model.DeleteOrganization(ctx, org); err != nil {
		return fmt.Errorf("DeleteOrganization: %w", err)
	}

	if err := committer.Commit(); err != nil {
		return err
	}

	if deletedPublicKey {
		if err := asymkey_model.RewriteAllPublicKeys(ctx); err != nil {
			return fmt.Errorf("RewriteAllPublicKeys: %w", err)
		}
	}

	// FIXME: system notice
	// Note: There are something just cannot be roll back,
	//	so just keep error logs of those operations.
	path := user_model.UserPath(org.Name)

	if err := util.RemoveAll(path); err != nil {
		return fmt.Errorf("failed to RemoveAll %s: %w", path, err)
	}

	if len(org.Avatar) > 0 {
		avatarPath := org.CustomAvatarRelativePath()
		if err := storage.Avatars.Delete(avatarPath); err != nil {
			return fmt.Errorf("failed to remove %s: %w", avatarPath, err)
		}
	}

	return nil
}

// deleteCounts counts the rows deleting the organization orgID removes with it, for its audit entry.
func deleteCounts(ctx context.Context, orgID int64) (map[string]int64, error) {
	keys := builder.Select("id").From("org_deploy_key").Where(builder.Eq{"org_id": orgID})
	counts := map[string]int64{}
	for _, c := range []struct {
		name, table string
		cond        builder.Cond
	}{
		{"teams", "team", builder.Eq{"org_id": orgID}},
		{"members", "org_user", builder.Eq{"org_id": orgID}},
		{"runners", "action_runner", builder.Eq{"owner_id": orgID}.And(builder.IsNull{"deleted"}.Or(builder.Eq{"deleted": 0}))},
		{"runner_tokens", "action_runner_token", builder.Eq{"owner_id": orgID}},
		{"deploy_keys", "org_deploy_key", builder.Eq{"org_id": orgID}},
		{"deploy_key_repos", "org_deploy_key_repo", builder.In("org_deploy_key_id", keys)},
		{"secrets", "secret", builder.Eq{"owner_id": orgID}},
		{"variables", "action_variable", builder.Eq{"owner_id": orgID}},
		{"verified_domains", "org_verified_domain", builder.Eq{"org_id": orgID}},
		{"branch_rules", "protected_branch", builder.Eq{"repo_id": 0, "org_id": orgID}},
		{"access_tokens", "access_token", builder.In("owner_type", auth_model.AccessTokenOwnerOrg, auth_model.AccessTokenOwnerProject).And(builder.Eq{"owner_id": orgID})},
	} {
		n, err := db.GetEngine(ctx).Table(c.table).Where(c.cond).Count()
		if err != nil {
			return nil, err
		}
		counts[c.name] = n
	}
	return counts, nil
}
