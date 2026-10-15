// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	audit_model "forgejo.org/models/audit"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	org_model "forgejo.org/models/organization"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/optional"
	"forgejo.org/modules/setting"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/test"
	"forgejo.org/modules/translation"
	"forgejo.org/services/cron"
	user_service "forgejo.org/services/user"
	usergroup_service "forgejo.org/services/usergroup"
	"forgejo.org/tests"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// asActor runs a service call as the user id, as a request of that user would.
func asActor(t *testing.T, id int64, name string) context.Context {
	return audit_model.WithActor(t.Context(), &audit_model.Actor{Type: audit_model.ActorUser, ID: id, Name: name})
}

// accountOne returns the one entry of action written after since.
func accountOne(t *testing.T, since int64, action string) *audit_model.Entry {
	t.Helper()
	entries := auditActions(t, since, action)
	require.Len(t, entries, 1, action)
	assert.Equal(t, audit_model.CategoryAccount, entries[0].Category)
	return entries[0]
}

func assertSubject(t *testing.T, e *audit_model.Entry, actorID, subject int64) {
	t.Helper()
	assert.Equal(t, actorID, e.ActorID, "actor")
	assert.Equal(t, subject, e.SubjectUserID, "subject")
	assert.Equal(t, subject, e.OwnerID)
}

func TestAuditAccountTwoFactor(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	session := loginUser(t, "user2")

	since := auditMaxID(t)
	session.EnrollTOTP(t)
	e := accountOne(t, since, "account.two_factor.enrolled")
	assertSubject(t, e, 2, 2)
	assert.Equal(t, map[string]any{"type": "totp", "name": ""}, auditData(t, e))
	twoFactor, err := auth_model.GetTwoFactorByUID(t.Context(), 2)
	require.NoError(t, err)
	assert.NotContains(t, e.Data, twoFactor.Secret)

	since = auditMaxID(t)
	session.MakeRequest(t, NewRequest(t, "POST", "/user/settings/security/two_factor/regenerate_scratch"), http.StatusSeeOther)
	e = accountOne(t, since, "account.two_factor.scratch_regenerated")
	assertSubject(t, e, 2, 2)
	assert.NotContains(t, e.Data, "scratch_hash")

	since = auditMaxID(t)
	session.MakeRequest(t, NewRequest(t, "POST", "/user/settings/security/two_factor/disable"), http.StatusSeeOther)
	assertSubject(t, accountOne(t, since, "account.two_factor.removed"), 2, 2)

	// WebAuthn keys: there is no virtual authenticator, so the model functions are called as the user would
	ctx := asActor(t, 2, "user2")
	since = auditMaxID(t)
	cred, err := auth_model.CreateCredential(ctx, 2, "laptop key", &webauthn.Credential{ID: []byte("audit-key"), PublicKey: []byte("material")})
	require.NoError(t, err)
	e = accountOne(t, since, "account.two_factor.enrolled")
	assert.Equal(t, map[string]any{"type": "webauthn", "name": "laptop key"}, auditData(t, e))
	assert.NotContains(t, e.Data, "material")

	since = auditMaxID(t)
	deleted, err := auth_model.DeleteCredential(ctx, cred.ID, 2)
	require.NoError(t, err)
	assert.True(t, deleted)
	e = accountOne(t, since, "account.two_factor.removed")
	assert.Equal(t, "laptop key", auditData(t, e)["name"])
}

func TestAuditAccountPassword(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	since := auditMaxID(t)
	session := loginUser(t, "user2")
	session.MakeRequest(t, NewRequestWithValues(t, "POST", "/user/settings/account", map[string]string{
		"old_password": userPassword, "password": "new-Password1!", "retype": "new-Password1!",
	}), http.StatusSeeOther)
	e := accountOne(t, since, "account.user.password_changed")
	assertSubject(t, e, 2, 2)
	assert.NotContains(t, e.Data, "new-Password1!")

	admin := getTokenForLoggedInUser(t, loginUser(t, "user1"), auth_model.AccessTokenScopeWriteAdmin)
	since = auditMaxID(t)
	MakeRequest(t, NewRequestWithJSON(t, "PATCH", "/api/v1/admin/users/user4", map[string]any{
		"login_name": "user4", "source_id": 0, "password": "admin-Set1!", "must_change_password": true,
	}).AddTokenAuth(admin), http.StatusOK)
	e = accountOne(t, since, "account.user.password_changed")
	assertSubject(t, e, 1, 4)
	e = accountOne(t, since, "account.user.updated")
	assertSubject(t, e, 1, 4)
	assert.Equal(t, map[string]any{
		"before": map[string]any{"must_change_password": false},
		"after":  map[string]any{"must_change_password": true},
	}, auditData(t, e))
}

func TestAuditAccountAdmin(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	admin := getTokenForLoggedInUser(t, loginUser(t, "user1"), auth_model.AccessTokenScopeWriteAdmin)

	t.Run("create", func(t *testing.T) {
		since := auditMaxID(t)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/admin/users", api.CreateUserOption{
			Username: "audit-new", Email: "audit-new@example.com", Password: "audit-Pass1!", MustChangePassword: new(bool),
		}).AddTokenAuth(admin), http.StatusCreated)
		u := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "audit-new"})
		e := accountOne(t, since, "account.user.created")
		assertSubject(t, e, 1, u.ID)
		after := auditData(t, e)["after"].(map[string]any)
		assert.Equal(t, "audit-new", after["name"])
		assert.NotContains(t, e.Data, "audit-Pass1!")
		assert.NotContains(t, e.Data, u.Passwd)
		assert.Empty(t, auditActions(t, since, "account.email.added"), "the first address is part of the account")
	})

	t.Run("update", func(t *testing.T) {
		since := auditMaxID(t)
		MakeRequest(t, NewRequestWithJSON(t, "PATCH", "/api/v1/admin/users/user4", map[string]any{
			"login_name": "user4", "source_id": 0, "restricted": true, "location": "elsewhere",
		}).AddTokenAuth(admin), http.StatusOK)
		e := accountOne(t, since, "account.user.updated")
		assertSubject(t, e, 1, 4)
		assert.Equal(t, map[string]any{
			"before": map[string]any{"is_restricted": false},
			"after":  map[string]any{"is_restricted": true},
		}, auditData(t, e), "only the audited fields that changed")

		since = auditMaxID(t)
		user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
		require.NoError(t, user_service.UpdateUser(asActor(t, 4, "user4"), user4, &user_service.UpdateOptions{Language: optional.Some("de-DE")}))
		assert.Empty(t, auditActions(t, since, "account.user.updated"), "a locale change is not audited")
	})

	t.Run("rename", func(t *testing.T) {
		since := auditMaxID(t)
		MakeRequest(t, NewRequestWithJSON(t, "POST", "/api/v1/admin/users/user4/rename", api.RenameUserOption{NewName: "user4-renamed"}).AddTokenAuth(admin), http.StatusNoContent)
		e := accountOne(t, since, "account.user.renamed")
		assertSubject(t, e, 1, 4)
		assert.Equal(t, map[string]any{
			"before": map[string]any{"name": "user4"}, "after": map[string]any{"name": "user4-renamed"},
		}, auditData(t, e))
	})

	t.Run("delete with counts", func(t *testing.T) {
		user8 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 8})
		require.NoError(t, auth_model.NewAccessToken(asActor(t, 8, "user8"), &auth_model.AccessToken{UID: 8, Name: "a", Scope: auth_model.AccessTokenScopeReadUser}, nil))
		require.NoError(t, auth_model.NewAccessToken(asActor(t, 8, "user8"), &auth_model.AccessToken{UID: 8, Name: "b", Scope: auth_model.AccessTokenScopeReadUser}, nil))
		since := auditMaxID(t)
		MakeRequest(t, NewRequest(t, "DELETE", "/api/v1/admin/users/user8").AddTokenAuth(admin), http.StatusNoContent)
		e := accountOne(t, since, "account.user.deleted")
		assertSubject(t, e, 1, user8.ID)
		data := auditData(t, e)
		counts := data["counts"].(map[string]any)
		assert.EqualValues(t, 2, counts["access_tokens"])
		assert.EqualValues(t, 1, counts["emails"])
		assert.Equal(t, false, data["purge"])
		assert.Empty(t, auditActions(t, since, "credential.access_token.deleted"), "the tokens go in bulk with the account")
	})

	t.Run("purge to a tombstone", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Trash.RetentionDays, 30)()
		defer test.MockVariableValue(&setting.Trash.TrashUserRepos, true)()
		since := auditMaxID(t)
		MakeRequest(t, NewRequest(t, "DELETE", "/api/v1/admin/users/user5?purge=true").AddTokenAuth(admin), http.StatusNoContent)
		assertSubject(t, accountOne(t, since, "account.user.purge_started"), 1, 5)
		data := auditData(t, accountOne(t, since, "account.user.deleted"))
		assert.Equal(t, true, data["purge"])
		assert.Equal(t, true, data["tombstone"])
	})

	t.Run("inactive accounts cron", func(t *testing.T) {
		inactive := &user_model.User{Name: "audit-inactive", Email: "audit-inactive@example.com", Passwd: "x-Pass1234!", IsActive: false}
		require.NoError(t, user_model.CreateUser(asActor(t, 1, "user1"), inactive, &user_model.CreateUserOverwriteOptions{IsActive: optional.Some(false)}))
		_, err := db.GetEngine(t.Context()).Exec("UPDATE `user` SET created_unix = 1 WHERE id = ?", inactive.ID)
		require.NoError(t, err)
		since := auditMaxID(t)
		cron.GetTask("delete_inactive_accounts").RunWithUser(t.Context(), nil, &cron.OlderThanConfig{
			BaseConfig: cron.BaseConfig{Enabled: true}, OlderThan: time.Hour,
		})
		var mine *audit_model.Entry
		for _, e := range auditActions(t, since, "account.user.deleted") {
			if e.SubjectUserID == inactive.ID {
				mine = e
			}
		}
		require.NotNil(t, mine)
		assert.Equal(t, audit_model.ActorSystem, mine.ActorType)
		assert.Equal(t, "cron:delete_inactive_accounts", mine.ActorName)
	})
}

func TestAuditAccountActivationAndRoles(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	t.Run("activation", func(t *testing.T) {
		defer test.MockVariableValue(&setting.Service.RegisterEmailConfirm, true)()
		cleanup, code, _ := parseMailHelper(t, `"auditact" <auditact@example.com>`, translation.NewLocale("en-US").TrString("mail.activate_account"))
		defer cleanup()
		session := emptyTestSession(t)
		session.MakeRequest(t, NewRequestWithValues(t, "POST", "/user/sign_up", map[string]string{
			"user_name": "auditact", "email": "auditact@example.com", "password": "examplePassword!1", "retype": "examplePassword!1",
		}), http.StatusOK)
		u := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "auditact"})
		since := auditMaxID(t)
		session.MakeRequest(t, NewRequestWithValues(t, "POST", "/user/activate?code="+*code, map[string]string{"password": "examplePassword!1"}), http.StatusSeeOther)
		e := accountOne(t, since, "account.user.activated")
		assert.Equal(t, u.ID, e.SubjectUserID)
		assert.Equal(t, u.ID, accountOne(t, since, "account.email.activated").SubjectUserID)
	})

	t.Run("role from a user group", func(t *testing.T) {
		group := &org_model.UserGroup{Name: "audit-admins", GrantsSiteAdmin: true}
		require.NoError(t, org_model.InsertUserGroup(t.Context(), group))
		user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
		ctx := asActor(t, 1, "user1")
		since := auditMaxID(t)
		require.NoError(t, usergroup_service.AddMember(ctx, group, user4))
		e := accountOne(t, since, "account.user.role_granted")
		assertSubject(t, e, 1, 4)
		assert.Equal(t, map[string]any{"role": "site_admin", "group_id": float64(group.ID)}, auditData(t, e))

		since = auditMaxID(t)
		require.NoError(t, usergroup_service.RemoveMember(ctx, group, 4))
		assertSubject(t, accountOne(t, since, "account.user.role_revoked"), 1, 4)
	})
}

func TestAuditAccountEmailsAndLinks(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	ctx := asActor(t, 2, "user2")

	since := auditMaxID(t)
	defer test.MockVariableValue(&setting.Service.RegisterEmailConfirm, true)() // added unactivated
	require.NoError(t, user_service.AddEmailAddresses(ctx, user2, []string{"audit-extra@example.com"}))
	e := accountOne(t, since, "account.email.added")
	assertSubject(t, e, 2, 2)
	assert.Equal(t, "audit-extra@example.com", auditData(t, e)["address"])

	extra, err := user_model.GetEmailAddressOfUser(t.Context(), "audit-extra@example.com", 2)
	require.NoError(t, err)
	since = auditMaxID(t)
	require.NoError(t, user_model.ActivateUserEmail(ctx, 2, extra.Email, true))
	accountOne(t, since, "account.email.activated")

	since = auditMaxID(t)
	old := user2.Email
	require.NoError(t, user_service.MakeEmailAddressPrimary(ctx, user2, extra, false))
	assert.Equal(t, true, auditData(t, accountOne(t, since, "account.email.primary"))["primary"])

	since = auditMaxID(t)
	require.NoError(t, user_service.DeleteEmailAddresses(ctx, user2, []string{old}))
	assert.Equal(t, old, auditData(t, accountOne(t, since, "account.email.deleted"))["address"])

	// deactivating the primary address deactivates the account
	admin := asActor(t, 1, "user1")
	since = auditMaxID(t)
	require.NoError(t, user_model.ActivateUserEmail(admin, 2, extra.Email, false))
	assertSubject(t, accountOne(t, since, "account.email.deactivated"), 1, 2)
	assert.Equal(t, map[string]any{
		"before": map[string]any{"is_active": true}, "after": map[string]any{"is_active": false},
	}, auditData(t, accountOne(t, since, "account.user.updated")))

	since = auditMaxID(t)
	require.NoError(t, user_model.LinkExternalToUser(ctx, user2, &user_model.ExternalLoginUser{ExternalID: "ext-42", UserID: 2, LoginSourceID: 7}))
	e = accountOne(t, since, "account.external_login.linked")
	assert.Equal(t, map[string]any{"login_source_id": 7.0, "external_id": "ext-42"}, auditData(t, e))
	since = auditMaxID(t)
	_, err = user_model.RemoveAccountLink(ctx, user2, 7)
	require.NoError(t, err)
	accountOne(t, since, "account.external_login.unlinked")

	since = auditMaxID(t)
	openid := &user_model.UserOpenID{UID: 2, URI: "https://id.example.com/user2"}
	require.NoError(t, user_model.AddUserOpenID(ctx, openid))
	assert.Equal(t, openid.URI, auditData(t, accountOne(t, since, "account.openid.linked"))["uri"])
	since = auditMaxID(t)
	require.NoError(t, user_model.DeleteUserOpenID(ctx, &user_model.UserOpenID{UID: 2, ID: openid.ID}))
	accountOne(t, since, "account.openid.unlinked")
}

func TestAuditAccountFailClosed(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	ctx := asActor(t, 1, "user1")

	t.Run("rollback leaves no entry", func(t *testing.T) {
		since := auditMaxID(t)
		user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
		err := db.WithTx(ctx, func(ctx context.Context) error {
			require.NoError(t, user_service.UpdateUser(ctx, user4, &user_service.UpdateOptions{IsRestricted: optional.Some(true)}))
			return errors.New("roll back")
		})
		require.Error(t, err)
		assert.Empty(t, auditActions(t, since, "account.user.updated"))
		assert.False(t, unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4}).IsRestricted)
	})

	defer test.MockVariableValue(&audit_model.InsertEntry, func(context.Context, *audit_model.Entry) error { return errors.New("audit down") })()

	t.Run("user update", func(t *testing.T) {
		user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
		require.Error(t, user_service.UpdateUser(ctx, user4, &user_service.UpdateOptions{IsAdmin: optional.Some(true)}))
		assert.False(t, unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4}).IsAdmin)
	})

	t.Run("2FA enrol", func(t *testing.T) {
		require.Error(t, auth_model.NewTwoFactor(ctx, &auth_model.TwoFactor{UID: 4}, "secret"))
		unittest.AssertNotExistsBean(t, &auth_model.TwoFactor{UID: 4})
	})

	t.Run("email add", func(t *testing.T) {
		user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
		require.Error(t, user_service.AddEmailAddresses(ctx, user4, []string{"audit-down@example.com"}))
		unittest.AssertNotExistsBean(t, &user_model.EmailAddress{LowerEmail: "audit-down@example.com"})
	})

	t.Run("must change password (CLI)", func(t *testing.T) {
		_, err := user_model.SetMustChangePassword(audit_model.WithCLI(t.Context()), false, true, []string{"user4"}, nil)
		require.Error(t, err)
		assert.False(t, unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4}).MustChangePassword)
	})
}

func TestAuditAccountMustChangePasswordCLI(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	since := auditMaxID(t)
	n, err := user_model.SetMustChangePassword(audit_model.WithCLI(t.Context()), false, true, []string{"user4", "user5"}, nil)
	require.NoError(t, err)
	require.EqualValues(t, 2, n)
	e := accountOne(t, since, "account.user.must_change_password")
	assert.Equal(t, audit_model.ActorCLI, e.ActorType)
	data := auditData(t, e)
	assert.EqualValues(t, 2, data["count"])
	assert.ElementsMatch(t, []any{4.0, 5.0}, data["user_ids"])
}
