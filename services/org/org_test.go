// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package org

import (
	"testing"

	"forgejo.org/models"
	"forgejo.org/models/actions"
	"forgejo.org/models/db"
	"forgejo.org/models/organization"
	secretscan_model "forgejo.org/models/secretscan"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/optional"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	unittest.MainTest(m)
}

func TestDeleteOrganization(t *testing.T) {
	defer unittest.OverrideFixtures("services/org/TestDeleteOrganization")()
	require.NoError(t, unittest.PrepareTestDatabase())

	org := unittest.AssertExistsAndLoadBean(t, &organization.Organization{ID: 6})
	for _, owner := range []int64{0, 6} {
		require.NoError(t, db.Insert(db.DefaultContext, &secretscan_model.CustomPattern{OwnerID: owner, Name: "p", Pattern: "x", Keywords: "[]", DeletedUnix: 1}))
	}
	live := &secretscan_model.CustomPattern{OwnerID: 6, Name: "live", Pattern: "x", Keywords: "[]", Enabled: true}
	require.NoError(t, db.Insert(db.DefaultContext, live))
	require.NoError(t, DeleteOrganization(db.DefaultContext, org, false))
	unittest.AssertNotExistsBean(t, &organization.Organization{ID: 6})
	// the organization's patterns stay as tombstones without their text: a repository transferred out earlier still
	// labels its alerts "Pattern of a previous owner"
	n, err := db.GetEngine(db.DefaultContext).Where("owner_id = 6 AND (deleted_unix = 0 OR enabled = ? OR pattern <> '')", true).Count(new(secretscan_model.CustomPattern))
	require.NoError(t, err)
	assert.Zero(t, n)
	got := unittest.AssertExistsAndLoadBean(t, &secretscan_model.CustomPattern{ID: live.ID})
	assert.NotZero(t, got.DeletedUnix)
	assert.False(t, got.Enabled)
	assert.Empty(t, got.Pattern)
	labels, err := secretscan_model.LoadRuleLabels(db.DefaultContext, []string{live.RuleID()})
	require.NoError(t, err)
	assert.Equal(t, secretscan_model.RuleLabel{Foreign: true}, labels.Label(live.RuleID(), 2, nil))
	unittest.AssertExistsAndLoadBean(t, &secretscan_model.CustomPattern{Name: "p", OwnerID: 0}) // the site's stay
	unittest.AssertNotExistsBean(t, &organization.OrgUser{OrgID: 6})
	unittest.AssertNotExistsBean(t, &organization.Team{OrgID: 6})
	unittest.AssertNotExistsBean(t, &actions.ActionRunnerToken{OwnerID: optional.Some[int64](6)})
	unittest.AssertNotExistsBean(t, &user_model.Follow{FollowID: 6})
	unittest.AssertNotExistsBean(t, &user_model.BlockedUser{UserID: 6})

	org = unittest.AssertExistsAndLoadBean(t, &organization.Organization{ID: 3})
	err = DeleteOrganization(db.DefaultContext, org, false)
	require.Error(t, err)
	assert.True(t, models.IsErrUserOwnRepos(err))

	user := unittest.AssertExistsAndLoadBean(t, &organization.Organization{ID: 5})
	require.Error(t, DeleteOrganization(db.DefaultContext, user, false))
	unittest.CheckConsistencyFor(t, &user_model.User{}, &organization.Team{})

	assert.Zero(t, unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1001}).NumFollowing)
}
