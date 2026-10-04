package access_test

import (
	"testing"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/db"
	perm_model "forgejo.org/models/perm"
	"forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/services/authz"

	"github.com/stretchr/testify/assert"
	mock "github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func assertAccess(t *testing.T, expectedMode perm_model.AccessMode, perm *access.Permission) {
	assert.Equal(t, expectedMode, perm.AccessMode)

	for _, unit := range perm.Units {
		assert.Equal(t, expectedMode, perm.UnitAccessMode(unit.Type))
	}
}

func TestActionTaskCanAccessOwnRepo(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	actionTask := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionTask{ID: 47})
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: actionTask.RepoID})

	perm, err := access.GetActionRepoPermission(db.DefaultContext, repo, actionTask)
	require.NoError(t, err)
	assertAccess(t, perm_model.AccessModeWrite, &perm)
}

func TestActionTaskCanAccessPublicRepo(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	actionTask := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionTask{ID: 47})
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})

	perm, err := access.GetActionRepoPermission(db.DefaultContext, repo, actionTask)
	require.NoError(t, err)
	assertAccess(t, perm_model.AccessModeRead, &perm)
}

func TestActionTaskCanAccessPublicRepoOfLimitedOrg(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	actionTask := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionTask{ID: 47})
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 38})

	perm, err := access.GetActionRepoPermission(db.DefaultContext, repo, actionTask)
	require.NoError(t, err)
	assertAccess(t, perm_model.AccessModeRead, &perm)
}

func TestActionTaskNoAccessPublicRepoOfPrivateOrg(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	actionTask := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionTask{ID: 47})
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 40})

	perm, err := access.GetActionRepoPermission(db.DefaultContext, repo, actionTask)
	require.NoError(t, err)
	assertAccess(t, perm_model.AccessModeNone, &perm)
}

func TestActionTaskNoAccessPrivateRepo(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	actionTask := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionTask{ID: 47})
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 2})

	perm, err := access.GetActionRepoPermission(db.DefaultContext, repo, actionTask)
	require.NoError(t, err)
	assertAccess(t, perm_model.AccessModeNone, &perm)
}

func TestGetUserRepoPermissionWithReducer(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	t.Run("no unit-level overrides", func(t *testing.T) {
		user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})

		// Baseline check that without a reducer, we get AccessModeOwner...
		permWithoutReducer, err := access.GetUserRepoPermission(t.Context(), repo, user)
		require.NoError(t, err)
		require.NotNil(t, permWithoutReducer)
		assert.True(t, permWithoutReducer.IsOwner())
		assert.True(t, permWithoutReducer.IsAdmin())
		assert.True(t, permWithoutReducer.HasAccess())
		assert.True(t, permWithoutReducer.CanWrite(unit.TypeIssues))

		reducer := authz.NewMockAuthorizationReducer(t)
		reducer.On(
			"ReduceRepoAccess",
			mock.Anything, // context
			mock.MatchedBy(func(repo *repo_model.Repository) bool { // repo
				return repo.ID == 1
			}),
			perm_model.AccessModeOwner, // incoming access mode
		).Return(perm_model.AccessModeNone, nil)

		permWithReducer, err := access.GetUserRepoPermissionWithReducer(t.Context(), repo, user, reducer)
		require.NoError(t, err)
		require.NotNil(t, permWithReducer)
		assert.False(t, permWithReducer.IsOwner())
		assert.False(t, permWithReducer.IsAdmin())
		assert.False(t, permWithReducer.HasAccess())
		assert.False(t, permWithReducer.CanWrite(unit.TypeIssues))
	})

	t.Run("team unit-level overrides", func(t *testing.T) {
		user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 15})
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 32})

		// Baseline check that without a reducer, we get mixed access for different units...
		permWithoutReducer, err := access.GetUserRepoPermission(t.Context(), repo, user)
		require.NoError(t, err)
		require.NotNil(t, permWithoutReducer)
		require.NotEmpty(t, permWithoutReducer.UnitsMode) // unit-specific access modes loaded
		assert.True(t, permWithoutReducer.CanRead(unit.TypeCode))
		assert.False(t, permWithoutReducer.CanWrite(unit.TypeCode))
		assert.True(t, permWithoutReducer.CanRead(unit.TypeIssues))
		assert.True(t, permWithoutReducer.CanWrite(unit.TypeIssues))

		reducer := authz.NewMockAuthorizationReducer(t)
		reducer.On(
			"ReduceRepoAccess",
			mock.Anything, // context
			mock.MatchedBy(func(repo *repo_model.Repository) bool { // repo
				return repo.ID == 32
			}),
			mock.Anything, // incoming access mode - will vary for each unit
		).Return(perm_model.AccessModeRead, nil)

		permWithReducer, err := access.GetUserRepoPermissionWithReducer(t.Context(), repo, user, reducer)
		require.NoError(t, err)
		require.NotNil(t, permWithReducer)
		require.NotEmpty(t, permWithReducer.UnitsMode) // unit-specific access modes loaded
		assert.True(t, permWithReducer.CanRead(unit.TypeCode))
		assert.False(t, permWithReducer.CanWrite(unit.TypeCode))
		assert.True(t, permWithReducer.CanRead(unit.TypeIssues))
		assert.False(t, permWithReducer.CanWrite(unit.TypeIssues))
	})
}

func TestGetUserRepoPermissionOrganization(t *testing.T) {
	defer unittest.OverrideFixtures("models/perm/access/TestGetUserRepoPermissionOrganization")()
	require.NoError(t, unittest.PrepareTestDatabase())

	t.Run("Owners", func(t *testing.T) {
		// Member of Owners team:
		user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 2})
		perm, err := access.GetUserRepoPermission(db.DefaultContext, repo, user)
		require.NoError(t, err)
		assertAccess(t, perm_model.AccessModeOwner, &perm)
	})

	t.Run("Admin", func(t *testing.T) {
		// Member of Admin team `team12creators`
		user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 28})
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 3})

		perm, err := access.GetUserRepoPermission(db.DefaultContext, repo, user)
		require.NoError(t, err)
		assertAccess(t, perm_model.AccessModeAdmin, &perm)
	})

	t.Run("Custom access", func(t *testing.T) {
		// If not Owner, and not Admin, then the team can specify No access, Read, or Write for each individual unit.
		// User.ID = 15 is part of Team.ID = 7, which has unit=Issues,AccessMode=Write, for the repo.ID=32
		user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 15}) // part of team.ID = 7
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 32})

		perm, err := access.GetUserRepoPermission(db.DefaultContext, repo, user)
		require.NoError(t, err)
		assert.Equal(t, perm_model.AccessModeWrite, perm.AccessMode)
		assert.Equal(t, perm_model.AccessModeRead, perm.UnitAccessMode(unit.TypeCode)) // code unit is enabled on repo, so that defaults access mode to read
		assert.Equal(t, perm_model.AccessModeWrite, perm.UnitAccessMode(unit.TypeIssues))
		assert.Equal(t, perm_model.AccessModeNone, perm.UnitAccessMode(unit.TypePullRequests))
		assert.Equal(t, perm_model.AccessModeNone, perm.UnitAccessMode(unit.TypeReleases))
		assert.Equal(t, perm_model.AccessModeNone, perm.UnitAccessMode(unit.TypeWiki))
		assert.Equal(t, perm_model.AccessModeNone, perm.UnitAccessMode(unit.TypeExternalWiki))
		assert.Equal(t, perm_model.AccessModeNone, perm.UnitAccessMode(unit.TypeExternalTracker))
		assert.Equal(t, perm_model.AccessModeNone, perm.UnitAccessMode(unit.TypeProjects))
		assert.Equal(t, perm_model.AccessModeNone, perm.UnitAccessMode(unit.TypePackages))
		assert.Equal(t, perm_model.AccessModeNone, perm.UnitAccessMode(unit.TypeActions))
	})
}

func TestGetUserRepoReadPermission(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	loadAuditor := func(id int64) *user_model.User {
		u := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: id})
		u.IsAuditor = true
		return u
	}
	// user 8 has no own access to these private repositories of a user, a public org and a private org,
	// nor to the public repository of the private org.
	repoIDs := []int64{2, 3, 40, 41}

	t.Run("GetUserRepoPermission ignores the role", func(t *testing.T) {
		user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 8})
		for _, id := range repoIDs {
			repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: id})
			perm, err := access.GetUserRepoPermission(ctx, repo, user)
			require.NoError(t, err)
			assert.False(t, perm.HasAccess(), "repo %d", id)
			auditorPerm, err := access.GetUserRepoPermission(ctx, repo, loadAuditor(8))
			require.NoError(t, err)
			assert.Equal(t, perm, auditorPerm, "repo %d", id)
		}
	})

	t.Run("read on every enabled unit, nothing more", func(t *testing.T) {
		for _, id := range repoIDs {
			repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: id})
			perm, err := access.GetUserRepoReadPermission(ctx, repo, loadAuditor(8), nil)
			require.NoError(t, err)
			require.NoError(t, repo.LoadUnits(ctx))
			assert.Equal(t, repo.Units, perm.Units, "repo %d", id)
			for _, u := range repo.Units {
				assert.Equal(t, perm_model.AccessModeRead, perm.UnitAccessMode(u.Type), "repo %d unit %s", id, u.Type)
			}
			assert.False(t, perm.IsAdmin(), "repo %d", id)
		}
	})

	t.Run("disabled units stay inaccessible", func(t *testing.T) {
		// repo 2 has no pull requests unit
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 2})
		perm, err := access.GetUserRepoReadPermission(ctx, repo, loadAuditor(8), nil)
		require.NoError(t, err)
		assert.True(t, perm.CanRead(unit.TypeCode))
		assert.False(t, perm.CanRead(unit.TypePullRequests))
	})

	t.Run("own access is kept", func(t *testing.T) {
		// user 15 can write issues of repo 32 through a team, and only read its code
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 32})
		perm, err := access.GetUserRepoReadPermission(ctx, repo, loadAuditor(15), nil)
		require.NoError(t, err)
		assert.True(t, perm.CanWrite(unit.TypeIssues))
		assert.True(t, perm.CanRead(unit.TypeCode))
		assert.False(t, perm.CanWrite(unit.TypeCode))
	})

	t.Run("no role, no raise", func(t *testing.T) {
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 3})
		perm, err := access.GetUserRepoReadPermission(ctx, repo, unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 8}), nil)
		require.NoError(t, err)
		assert.False(t, perm.HasAccess())
		perm, err = access.GetUserRepoReadPermission(ctx, repo, nil, nil)
		require.NoError(t, err)
		assert.False(t, perm.HasAccess())
	})

	t.Run("the reducer applies after the raise", func(t *testing.T) {
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 3})
		reducer := authz.NewMockAuthorizationReducer(t)
		reducer.On("ReduceRepoAccess", mock.Anything, mock.Anything, mock.Anything).Return(perm_model.AccessModeNone, nil)
		perm, err := access.GetUserRepoReadPermission(ctx, repo, loadAuditor(8), reducer)
		require.NoError(t, err)
		assert.False(t, perm.HasAccess())
	})
}

func TestGetUserRepoPermissionWithReducerToNone(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	// user 15 has unit-level access to repo 32 through a team
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 32})
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 15})
	reducer := authz.NewMockAuthorizationReducer(t)
	reducer.On("ReduceRepoAccess", mock.Anything, mock.Anything, mock.Anything).Return(perm_model.AccessModeNone, nil)

	perm, err := access.GetUserRepoPermissionWithReducer(t.Context(), repo, user, reducer)
	require.NoError(t, err)
	assert.False(t, perm.HasAccess())
}
