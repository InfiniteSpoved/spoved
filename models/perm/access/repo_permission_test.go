package access_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	actions_model "forgejo.org/models/actions"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	perm_model "forgejo.org/models/perm"
	"forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/services/authz"

	"code.forgejo.org/xorm/xorm/contexts"
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

func TestGetUserRepoPermissionTwoFactorRequired(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()

	requireTwoFactor := func(t *testing.T, orgIDs ...int64) {
		t.Helper()
		_, err := db.GetEngine(ctx).In("id", orgIDs).Cols("require_two_factor").Update(&user_model.User{RequireTwoFactor: true})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, err := db.GetEngine(ctx).In("id", orgIDs).Cols("require_two_factor").Update(&user_model.User{RequireTwoFactor: false})
			require.NoError(t, err)
		})
	}
	loadUser := func(id int64) *user_model.User {
		return unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: id})
	}
	perm := func(t *testing.T, repoID int64, user *user_model.User) *access.Permission {
		t.Helper()
		p, err := access.GetUserRepoPermission(ctx, unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: repoID}), user)
		require.NoError(t, err)
		return &p
	}

	// org3 (public): private repos 3 and 5, public repo 32; user 2 owns it, user 4 writes repo 3 through team1,
	// user 2 is also a collaborator on repo 3
	t.Run("flag off", func(t *testing.T) {
		assert.True(t, perm(t, 3, loadUser(2)).IsOwner())
		assert.True(t, perm(t, 3, loadUser(4)).CanWrite(unit.TypeCode))
	})

	t.Run("member without 2FA is a stranger", func(t *testing.T) {
		requireTwoFactor(t, 3)
		for _, uid := range []int64{2, 4} {
			assert.False(t, perm(t, 3, loadUser(uid)).HasAccess(), "user %d", uid)
			p := perm(t, 32, loadUser(uid))
			assert.True(t, p.CanRead(unit.TypeCode), "user %d", uid)
			assert.False(t, p.CanWriteIssuesOrPulls(false), "user %d", uid)
			assert.False(t, p.IsAdmin(), "user %d", uid)
		}
		// anonymous is unchanged
		assert.False(t, perm(t, 3, nil).HasAccess())
		assert.True(t, perm(t, 32, nil).CanRead(unit.TypeCode))
	})

	t.Run("TOTP or WebAuthn restores access", func(t *testing.T) {
		requireTwoFactor(t, 3)
		require.NoError(t, db.Insert(ctx, &auth_model.TwoFactor{UID: 2}))
		assert.True(t, perm(t, 3, loadUser(2)).IsOwner())
		_, err := db.DeleteByBean(ctx, &auth_model.TwoFactor{UID: 2})
		require.NoError(t, err)
		require.NoError(t, db.Insert(ctx, &auth_model.WebAuthnCredential{UserID: 4, Name: "key", CredentialID: []byte("cred-4")}))
		defer func() {
			_, err := db.DeleteByBean(ctx, &auth_model.WebAuthnCredential{UserID: 4})
			require.NoError(t, err)
		}()
		assert.True(t, perm(t, 3, loadUser(4)).CanWrite(unit.TypeCode))
		assert.False(t, perm(t, 3, loadUser(2)).HasAccess())
	})

	t.Run("site admin, the organization itself and pseudo users are exempt", func(t *testing.T) {
		requireTwoFactor(t, 3)
		assert.True(t, perm(t, 3, loadUser(1)).IsOwner())
		// deploy-key pushes run as the repository owner
		assert.True(t, perm(t, 3, loadUser(3)).IsOwner())
		assert.True(t, perm(t, 32, user_model.NewActionsUser()).CanRead(unit.TypeCode))
		assert.False(t, perm(t, 3, user_model.NewGhostUser()).HasAccess())
	})

	t.Run("auditor without 2FA is not raised", func(t *testing.T) {
		requireTwoFactor(t, 3)
		auditor := loadUser(8)
		auditor.IsAuditor = true
		p, err := access.GetUserRepoReadPermission(ctx, unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 3}), auditor, nil)
		require.NoError(t, err)
		assert.False(t, p.HasAccess())
		p, err = access.GetUserRepoReadPermission(ctx, unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 32}), auditor, nil)
		require.NoError(t, err)
		assert.True(t, p.CanRead(unit.TypeCode))
	})

	t.Run("private and limited organizations", func(t *testing.T) {
		requireTwoFactor(t, 22, 23)
		// user 5 is a member of the private org 23, user 4 a collaborator on its public repo 40
		assert.False(t, perm(t, 40, loadUser(5)).HasAccess())
		assert.False(t, perm(t, 40, loadUser(4)).HasAccess())
		assert.False(t, perm(t, 41, loadUser(5)).HasAccess())
		// a public repo of a limited org stays readable, as for every signed-in user
		assert.True(t, perm(t, 38, loadUser(2)).CanRead(unit.TypeCode))
		assert.False(t, perm(t, 39, loadUser(2)).HasAccess())
	})

	t.Run("restricted user is never raised", func(t *testing.T) {
		requireTwoFactor(t, 17)
		// user 29 is restricted and a member of org17
		assert.False(t, perm(t, 23, loadUser(29)).HasAccess())
		assert.False(t, perm(t, 24, loadUser(29)).HasAccess())
	})

	t.Run("an unflagged organization adds no query", func(t *testing.T) {
		var queries []string
		defer onSQL(t, func(sql string) error {
			if strings.Contains(sql, "FROM `two_factor`") || strings.Contains(sql, "FROM `webauthn_credential`") {
				queries = append(queries, sql)
			}
			return nil
		})()
		perm(t, 3, loadUser(2))
		assert.Empty(t, queries)
		requireTwoFactor(t, 3)
		perm(t, 3, loadUser(2))
		assert.NotEmpty(t, queries)
	})

	t.Run("a lookup error is returned, never the normal permission", func(t *testing.T) {
		requireTwoFactor(t, 3)
		defer onSQL(t, func(sql string) error {
			if strings.Contains(sql, "FROM `two_factor`") {
				return errors.New("lookup failed")
			}
			return nil
		})()
		_, err := access.GetUserRepoPermission(ctx, unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 3}), loadUser(2))
		require.Error(t, err)
	})
}

var (
	sqlHookOnce sync.Once
	sqlHookFunc atomic.Pointer[func(string) error]
)

type sqlHook struct{}

func (sqlHook) BeforeProcess(c *contexts.ContextHook) (context.Context, error) {
	if f := sqlHookFunc.Load(); f != nil {
		if err := (*f)(c.SQL); err != nil {
			return nil, err
		}
	}
	// xorm keeps the context of the last hook, and db.TracingHook needs its own
	return db.TracingHook{}.BeforeProcess(c)
}

func (sqlHook) AfterProcess(*contexts.ContextHook) error { return nil }

// onSQL calls f with every SQL statement until the returned func is called; an error from f fails the statement.
func onSQL(t *testing.T, f func(string) error) func() {
	t.Helper()
	sqlHookOnce.Do(func() {
		engine, err := db.GetMasterEngine(db.GetEngine(db.DefaultContext))
		require.NoError(t, err)
		engine.AddHook(sqlHook{})
	})
	sqlHookFunc.Store(&f)
	return func() { sqlHookFunc.Store(nil) }
}

// TwoFactorHiddenRepoCond hides exactly the repositories GetUserRepoPermission refuses because of the
// organization's two-factor requirement, and no list built on AccessibleRepositoryCondition shows a refused one.
func TestTwoFactorHiddenRepoCondMatchesPermission(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()
	flagged := []int64{3, 17, 22, 23}
	_, err := db.GetEngine(ctx).In("id", flagged).Cols("require_two_factor").Update(&user_model.User{RequireTwoFactor: true})
	require.NoError(t, err)

	var users []*user_model.User
	require.NoError(t, db.GetEngine(ctx).Where("type <> ?", user_model.UserTypeOrganization).Find(&users))
	var repos []*repo_model.Repository
	require.NoError(t, db.GetEngine(ctx).In("owner_id", flagged).Find(&repos))
	auditor := false
	for _, u := range users {
		if u.ID == 8 {
			u.IsAuditor = true
			auditor = true
		}
		var visible, notHidden []int64
		require.NoError(t, db.GetEngine(ctx).Table("repository").In("owner_id", flagged).
			And(repo_model.AccessibleRepositoryCondition(u, unit.TypeInvalid)).Cols("id").Find(&visible))
		require.NoError(t, db.GetEngine(ctx).Table("repository").In("owner_id", flagged).
			And(repo_model.TwoFactorHiddenRepoCond(u.ID)).Cols("id").Find(&notHidden))
		for _, repo := range repos {
			p, err := access.GetUserRepoReadPermission(ctx, repo, u, nil)
			require.NoError(t, err)
			require.NoError(t, repo.LoadOwner(ctx))
			blocked, err := u.TwoFactorBlocked(ctx, repo.Owner)
			require.NoError(t, err)
			if !p.HasAccess() {
				assert.NotContains(t, visible, repo.ID, "user %d repo %d", u.ID, repo.ID)
			}
			assert.Equal(t, blocked && !p.HasAccess(), !slices.Contains(notHidden, repo.ID), "user %d repo %d", u.ID, repo.ID)
		}
	}
	assert.True(t, auditor)
}

// SearchRepositoryCondition skips AccessibleRepositoryCondition for the actor's own list and for auditors.
func TestSearchRepositoryTwoFactorRequired(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()
	_, err := db.GetEngine(ctx).ID(3).Cols("require_two_factor").Update(&user_model.User{RequireTwoFactor: true})
	require.NoError(t, err)

	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	ids, _, err := repo_model.SearchRepositoryIDs(ctx, &repo_model.SearchRepoOptions{Actor: user2, OwnerID: 2, Private: true})
	require.NoError(t, err)
	assert.NotContains(t, ids, int64(3))
	assert.Contains(t, ids, int64(1)) // own repositories stay

	auditor := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 8})
	auditor.IsAuditor = true
	ids, _, err = repo_model.SearchRepositoryIDs(ctx, &repo_model.SearchRepoOptions{Actor: auditor, AuditorView: true, Private: true, ListOptions: db.ListOptionsAll})
	require.NoError(t, err)
	assert.NotContains(t, ids, int64(3))
	assert.Contains(t, ids, int64(32))
	assert.Contains(t, ids, int64(41)) // other organizations stay
}
