// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repo_test

import (
	"strconv"
	"testing"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	system_model "forgejo.org/models/system"
	"forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/json"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/setting/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setUnitPolicy(t *testing.T, p setting.RepoUnitPolicyState) {
	t.Helper()
	v, err := json.Marshal(p)
	require.NoError(t, err)
	require.NoError(t, system_model.SetSettings(db.DefaultContext, map[string]string{setting.Config().RepoUnitPolicy.DynKey(): string(v)}))
	config.GetDynGetter().InvalidateCache()
}

func TestOrgAllowed(t *testing.T) {
	p := setting.RepoUnitPolicyState{
		NotAllowed:   []string{"repo.issues"},
		OrgOverrides: map[string][]int64{"repo.issues": {3}, "repo.wiki": {6}},
	}
	assert.True(t, p.OrgAllowed("repo.issues", 3))
	assert.False(t, p.OrgAllowed("repo.issues", 6))
	assert.False(t, p.OrgAllowed("repo.wiki", 6))
	assert.True(t, p.OrgAllowed("repo.wiki", 3))
	assert.True(t, p.OrgAllowed("repo.projects", 3))
	assert.True(t, setting.RepoUnitPolicyState{}.OrgAllowed("repo.issues", 3))
}

// HiddenUnitCond must match HiddenUnits for every owner and unit
func TestHiddenUnitCondMatchesHiddenUnits(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	defer setUnitPolicy(t, setting.RepoUnitPolicyState{})

	var owners []*user_model.User
	require.NoError(t, db.GetEngine(db.DefaultContext).Find(&owners))

	for name, p := range map[string]setting.RepoUnitPolicyState{
		"empty":       {},
		"not allowed": {NotAllowed: []string{"repo.issues", "repo.packages"}},
		"overrides": {
			NotAllowed:   []string{"repo.issues"},
			OrgOverrides: map[string][]int64{"repo.issues": {3, 999999}, "repo.wiki": {3, 6}},
		},
		"everything": {NotAllowed: []string{"repo.issues", "repo.ext_issues", "repo.pulls", "repo.releases", "repo.wiki", "repo.ext_wiki", "repo.projects", "repo.packages", "repo.actions"}},
	} {
		t.Run(name, func(t *testing.T) {
			setUnitPolicy(t, p)
			if name == "empty" {
				assert.False(t, repo_model.HiddenUnitCond(db.DefaultContext, "`user`.id", "t").IsValid())
			}
			for _, owner := range owners {
				hidden := repo_model.HiddenUnits(db.DefaultContext, owner)
				for _, tp := range unit.AllRepoUnitTypes {
					// the unit type as a literal in place of a column
					cond := repo_model.HiddenUnitCond(db.DefaultContext, "`user`.id", strconv.Itoa(int(tp)))
					got := false
					if cond.IsValid() {
						var err error
						got, err = db.GetEngine(db.DefaultContext).Table("user").Where("`user`.id = ?", owner.ID).And(cond).Exist()
						require.NoError(t, err)
					}
					assert.Equal(t, hidden.Contains(tp), got, "owner %s unit %s", owner.Name, tp)
				}
			}
		})
	}
}

func TestLoadUnitsHidesPolicyUnits(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	defer setUnitPolicy(t, setting.RepoUnitPolicyState{})
	setUnitPolicy(t, setting.RepoUnitPolicyState{NotAllowed: []string{"repo.issues"}})

	orgRepo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 3})  // org3
	userRepo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1}) // user2
	assert.False(t, orgRepo.UnitEnabled(db.DefaultContext, unit.TypeIssues))
	assert.True(t, orgRepo.UnitEnabled(db.DefaultContext, unit.TypePullRequests))
	assert.True(t, userRepo.UnitEnabled(db.DefaultContext, unit.TypeIssues))
	// the row is kept
	unittest.AssertExistsAndLoadBean(t, &repo_model.RepoUnit{RepoID: 3, Type: unit.TypeIssues})

	list := repo_model.RepositoryList{
		unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 3}),
		unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1}),
	}
	require.NoError(t, list.LoadUnits(db.DefaultContext))
	assert.False(t, list[0].UnitEnabled(db.DefaultContext, unit.TypeIssues))
	assert.True(t, list[1].UnitEnabled(db.DefaultContext, unit.TypeIssues))

	setUnitPolicy(t, setting.RepoUnitPolicyState{})
	orgRepo = unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 3})
	assert.True(t, orgRepo.UnitEnabled(db.DefaultContext, unit.TypeIssues))
}
