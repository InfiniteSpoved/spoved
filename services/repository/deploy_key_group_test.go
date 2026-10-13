// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repository

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	asymkey_model "forgejo.org/models/asymkey"
	git_model "forgejo.org/models/git"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func newDeployKeyContent(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	return string(ssh.MarshalAuthorizedKey(sshPub))
}

// Deleting a project deletes its deploy keys, and the public keys no other deploy key uses.
func TestDeleteRepoGroupDeletesDeployKeys(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	org3 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 3})
	group, err := repo_model.CreateRepoGroup(ctx, org3, "pay", "")
	require.NoError(t, err)
	only, err := asymkey_model.AddOrgDeployKey(ctx, 3, group.ID, "only", newDeployKeyContent(t), true, false, nil, 0)
	require.NoError(t, err)
	shared := newDeployKeyContent(t)
	sharedKey, err := asymkey_model.AddOrgDeployKey(ctx, 3, group.ID, "shared", shared, true, false, nil, 0)
	require.NoError(t, err)
	orgKey, err := asymkey_model.AddOrgDeployKey(ctx, 3, 0, "shared", shared, true, true, nil, 0)
	require.NoError(t, err)

	// a project with a repository is refused, and keeps its keys
	repo, err := CreateRepositoryDirectly(ctx, doer, org3, CreateRepoOptions{Name: "api", GroupID: group.ID})
	require.NoError(t, err)
	require.ErrorIs(t, DeleteRepoGroup(ctx, org3, group), repo_model.ErrRepoGroupNotEmpty)
	unittest.AssertExistsAndLoadBean(t, &asymkey_model.OrgDeployKey{ID: only.ID})
	unittest.AssertExistsAndLoadBean(t, &asymkey_model.PublicKey{ID: only.KeyID})

	require.NoError(t, DeleteRepositoryDirectly(ctx, repo.ID, DeleteRepositoryOpts{}))
	require.NoError(t, DeleteRepoGroup(ctx, org3, group))
	unittest.AssertNotExistsBean(t, &asymkey_model.OrgDeployKey{ID: only.ID})
	unittest.AssertNotExistsBean(t, &asymkey_model.OrgDeployKey{ID: sharedKey.ID})
	unittest.AssertNotExistsBean(t, &asymkey_model.PublicKey{ID: only.KeyID})
	unittest.AssertExistsAndLoadBean(t, &asymkey_model.OrgDeployKey{ID: orgKey.ID})
	unittest.AssertExistsAndLoadBean(t, &asymkey_model.PublicKey{ID: orgKey.KeyID})
}

// A repository moved out of a project loses the project's keys in its protection rules, unless another key of the
// same public key still writes to it.
func TestMoveRepositoryDropsProjectDeployKeys(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	org3 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 3})
	pay, err := repo_model.CreateRepoGroup(ctx, org3, "pay", "")
	require.NoError(t, err)
	repo, err := CreateRepositoryDirectly(ctx, doer, org3, CreateRepoOptions{Name: "api", GroupID: pay.ID})
	require.NoError(t, err)

	projectOnly, err := asymkey_model.AddOrgDeployKey(ctx, 3, pay.ID, "project only", newDeployKeyContent(t), false, false, nil, 0)
	require.NoError(t, err)
	shared := newDeployKeyContent(t)
	alsoOrg, err := asymkey_model.AddOrgDeployKey(ctx, 3, pay.ID, "also org", shared, false, false, nil, 0)
	require.NoError(t, err)
	_, err = asymkey_model.AddOrgDeployKey(ctx, 3, 0, "org", shared, false, true, nil, 0)
	require.NoError(t, err)

	keyIDs := []int64{projectOnly.KeyID, alsoOrg.KeyID}
	require.NoError(t, git_model.UpdateProtectBranch(ctx, repo, &git_model.ProtectedBranch{RepoID: repo.ID, RuleName: "main"},
		git_model.WhitelistOptions{DeployKeyIDs: keyIDs, DeleteDeployKeyIDs: keyIDs, ForcePushDeployKeyIDs: keyIDs}))
	rule := unittest.AssertExistsAndLoadBean(t, &git_model.ProtectedBranch{RepoID: repo.ID, RuleName: "main"})
	assert.ElementsMatch(t, keyIDs, rule.WhitelistDeployKeyIDs, "the picker takes the project's write keys")

	require.NoError(t, MoveRepository(ctx, doer, reloadRepo(t, repo.ID), 0))
	rule = unittest.AssertExistsAndLoadBean(t, &git_model.ProtectedBranch{RepoID: repo.ID, RuleName: "main"})
	want := []int64{alsoOrg.KeyID}
	assert.Equal(t, want, rule.WhitelistDeployKeyIDs)
	assert.Equal(t, want, rule.DeleteAllowlistDeployKeyIDs)
	assert.Equal(t, want, rule.ForcePushAllowlistDeployKeyIDs)

	_, err = asymkey_model.GetEffectiveDeployKey(ctx, projectOnly.KeyID, reloadRepo(t, repo.ID))
	assert.True(t, asymkey_model.IsErrDeployKeyNotExist(err), "%v", err)
}
