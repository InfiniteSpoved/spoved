// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/modules/git"
	"forgejo.org/modules/hostmatcher"
	"forgejo.org/modules/ratelimit"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"
	"forgejo.org/routers"
	"forgejo.org/tests"

	"github.com/markbates/goth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slow is a refill rate that never refills during a test
const slow = 0.0001

func mockRateLimit(t *testing.T, classes map[string]setting.RateLimitClass, exempt string) {
	t.Helper()
	t.Cleanup(test.MockVariableValue(&setting.RateLimit.Enabled, true))
	t.Cleanup(test.MockVariableValue(&setting.RateLimit.Classes, classes))
	t.Cleanup(test.MockVariableValue(&setting.RateLimit.ExemptCIDRs, hostmatcher.ParseHostMatchList("", exempt)))
	ratelimit.ResetForTest()
	t.Cleanup(ratelimit.ResetForTest)
}

func fromIP(rw *RequestWrapper, ip string) *RequestWrapper {
	rw.Request.RemoteAddr = ip + ":1234"
	return rw
}

func TestRateLimitDisabled(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	ratelimit.ResetForTest()

	for range 3 {
		resp := MakeRequest(t, NewRequest(t, "GET", "/api/v1/version"), http.StatusOK)
		assert.Empty(t, resp.Header().Get("X-RateLimit-Limit"))
	}
}

func TestRateLimitAPI(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	token := getUserToken(t, "user2", auth_model.AccessTokenScopeAll)
	mockRateLimit(t, map[string]setting.RateLimitClass{
		ratelimit.API:      {AuthenticatedRate: slow, AuthenticatedBurst: 3, AnonymousRate: slow, AnonymousBurst: 2},
		ratelimit.APIWrite: {AuthenticatedRate: slow, AuthenticatedBurst: 1},
	}, "")

	t.Run("anonymous per IP", func(t *testing.T) {
		for _, remaining := range []string{"1", "0"} {
			resp := MakeRequest(t, fromIP(NewRequest(t, "GET", "/api/v1/version"), "192.0.2.1"), http.StatusOK)
			assert.Equal(t, "2", resp.Header().Get("X-RateLimit-Limit"))
			assert.Equal(t, remaining, resp.Header().Get("X-RateLimit-Remaining"))
			assert.NotEmpty(t, resp.Header().Get("X-RateLimit-Reset"))
			assert.Contains(t, resp.Header().Get("Access-Control-Expose-Headers"), "X-RateLimit-Remaining")
		}
		resp := MakeRequest(t, fromIP(NewRequest(t, "GET", "/api/v1/version"), "192.0.2.1"), http.StatusTooManyRequests)
		assert.NotEmpty(t, resp.Header().Get("Retry-After"))
		assert.Equal(t, "0", resp.Header().Get("X-RateLimit-Remaining"))
		assert.Contains(t, resp.Body.String(), "rate limit exceeded, retry in")

		// the other API mount shares the class
		MakeRequest(t, fromIP(NewRequest(t, "GET", "/api/forgejo/v1/version"), "192.0.2.1"), http.StatusTooManyRequests)
		// other IPs have their own bucket
		MakeRequest(t, fromIP(NewRequest(t, "GET", "/api/v1/version"), "192.0.2.2"), http.StatusOK)
	})

	t.Run("forwarded headers only from trusted proxies", func(t *testing.T) {
		// 192.0.2.1 is not a trusted proxy, so its X-Forwarded-For is ignored
		req := fromIP(NewRequest(t, "GET", "/api/v1/version"), "192.0.2.1").SetHeader("X-Forwarded-For", "198.51.100.1")
		MakeRequest(t, req, http.StatusTooManyRequests)
		req = fromIP(NewRequest(t, "GET", "/api/v1/version"), "192.0.2.1").SetHeader("X-Real-IP", "198.51.100.1")
		MakeRequest(t, req, http.StatusTooManyRequests)
		// loopback is trusted by default
		req = fromIP(NewRequest(t, "GET", "/api/v1/version"), "127.0.0.1").SetHeader("X-Forwarded-For", "192.0.2.1")
		MakeRequest(t, req, http.StatusTooManyRequests)
		req = fromIP(NewRequest(t, "GET", "/api/v1/version"), "127.0.0.1").SetHeader("X-Forwarded-For", "198.51.100.1")
		MakeRequest(t, req, http.StatusOK)
	})

	t.Run("authenticated per user, writes separately", func(t *testing.T) {
		// the IP is exhausted, the user isn't
		resp := MakeRequest(t, fromIP(NewRequest(t, "GET", "/api/v1/user"), "192.0.2.1").AddTokenAuth(token), http.StatusOK)
		assert.Equal(t, "3", resp.Header().Get("X-RateLimit-Limit"))

		req := NewRequestWithJSON(t, "POST", "/api/v1/markdown", map[string]string{"Text": "hi"})
		MakeRequest(t, fromIP(req.AddTokenAuth(token), "192.0.2.3"), http.StatusOK)
		req = NewRequestWithJSON(t, "POST", "/api/v1/markdown", map[string]string{"Text": "hi"})
		MakeRequest(t, fromIP(req.AddTokenAuth(token), "192.0.2.4"), http.StatusTooManyRequests)

		// reads have their own bucket: 3 in all
		MakeRequest(t, fromIP(NewRequest(t, "GET", "/api/v1/user"), "192.0.2.4").AddTokenAuth(token), http.StatusOK)
		MakeRequest(t, fromIP(NewRequest(t, "GET", "/api/v1/user"), "192.0.2.4").AddTokenAuth(token), http.StatusOK)
		MakeRequest(t, fromIP(NewRequest(t, "GET", "/api/v1/user"), "192.0.2.4").AddTokenAuth(token), http.StatusTooManyRequests)
	})
}

func TestRateLimitExempt(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	mockRateLimit(t, map[string]setting.RateLimitClass{
		ratelimit.API:  {AuthenticatedRate: slow, AuthenticatedBurst: 1, AnonymousRate: slow, AnonymousBurst: 1},
		ratelimit.Auth: {AuthenticatedRate: slow, AuthenticatedBurst: 1},
	}, "192.0.2.0/24")

	for range 3 {
		resp := MakeRequest(t, fromIP(NewRequest(t, "GET", "/api/v1/version"), "192.0.2.9"), http.StatusOK)
		assert.Empty(t, resp.Header().Get("X-RateLimit-Limit"))
		req := NewRequestWithValues(t, "POST", "/user/login", map[string]string{"user_name": "user2", "password": "wrong"})
		MakeRequest(t, fromIP(req, "192.0.2.9"), http.StatusOK)
	}
	MakeRequest(t, fromIP(NewRequest(t, "GET", "/api/v1/version"), "198.51.100.9"), http.StatusOK)
	MakeRequest(t, fromIP(NewRequest(t, "GET", "/api/v1/version"), "198.51.100.9"), http.StatusTooManyRequests)
}

func TestRateLimitNeverLimited(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	tiny := setting.RateLimitClass{AuthenticatedRate: slow, AuthenticatedBurst: 1, AnonymousRate: slow, AnonymousBurst: 1}
	mockRateLimit(t, map[string]setting.RateLimitClass{
		ratelimit.API: tiny, ratelimit.APIWrite: tiny, ratelimit.Git: tiny, ratelimit.Web: tiny, ratelimit.Auth: tiny,
	}, "")
	internalRoutes := routers.InternalRoutes()

	for range 3 {
		resp := MakeRequest(t, fromIP(NewRequest(t, "GET", "/api/healthz"), "192.0.2.1"), http.StatusOK)
		assert.Empty(t, resp.Header().Get("X-RateLimit-Limit"))

		// the Actions runner API
		req := NewRequestWithBody(t, "POST", "/api/actions/ping.v1.PingService/Ping", strings.NewReader(`{"data":"x"}`)).
			SetHeader("Content-Type", "application/json")
		resp = MakeRequest(t, fromIP(req, "192.0.2.1"), http.StatusOK)
		assert.Empty(t, resp.Header().Get("X-RateLimit-Limit"))

		MakeRequest(t, fromIP(NewRequest(t, "GET", "/assets/img/logo.svg"), "192.0.2.1"), http.StatusOK)

		// the internal API is a separate router without the rate limit middleware
		req = fromIP(NewRequest(t, "GET", "/api/internal/serv/none/1"), "192.0.2.1")
		recorder := NewNilResponseRecorder()
		internalRoutes.ServeHTTP(recorder, req.Request)
		assert.NotEqual(t, http.StatusTooManyRequests, recorder.Code)
		assert.Empty(t, recorder.Header().Get("X-RateLimit-Limit"))
	}
}

func TestRateLimitWebAndGit(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	session := loginUser(t, "user2")
	mockRateLimit(t, map[string]setting.RateLimitClass{
		ratelimit.Web: {AuthenticatedRate: slow, AuthenticatedBurst: 1, AnonymousRate: slow, AnonymousBurst: 1},
		ratelimit.Git: {AuthenticatedRate: slow, AuthenticatedBurst: 1, AnonymousRate: slow, AnonymousBurst: 1},
	}, "")

	MakeRequest(t, fromIP(NewRequest(t, "GET", "/explore/repos"), "192.0.2.1"), http.StatusOK)
	resp := MakeRequest(t, fromIP(NewRequest(t, "GET", "/explore/repos"), "192.0.2.1"), http.StatusTooManyRequests)
	assert.Contains(t, resp.Body.String(), "You have sent too many requests")
	assert.NotEmpty(t, resp.Header().Get("Retry-After"))
	// the 404 page is limited too
	MakeRequest(t, fromIP(NewRequest(t, "GET", "/no/such/page/at/all"), "192.0.2.1"), http.StatusTooManyRequests)

	// git has its own bucket
	MakeRequest(t, fromIP(NewRequest(t, "GET", "/user2/repo1.git/info/refs?service=git-upload-pack"), "192.0.2.1"), http.StatusOK)
	resp = MakeRequest(t, fromIP(NewRequest(t, "GET", "/user2/repo1.git/info/refs?service=git-upload-pack"), "192.0.2.1"), http.StatusTooManyRequests)
	assert.Contains(t, resp.Header().Get("Content-Type"), "text/plain")
	assert.Contains(t, resp.Body.String(), "rate limit exceeded, retry in")

	// a signed in user has their own bucket
	session.MakeRequest(t, fromIP(NewRequest(t, "GET", "/explore/repos"), "192.0.2.1"), http.StatusOK)
	session.MakeRequest(t, fromIP(NewRequest(t, "GET", "/explore/repos"), "192.0.2.1"), http.StatusTooManyRequests)
}

func TestRateLimitGitClone(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		mockRateLimit(t, map[string]setting.RateLimitClass{
			ratelimit.Git: {AuthenticatedRate: slow, AuthenticatedBurst: 1, AnonymousRate: slow, AnonymousBurst: 1},
		}, "")
		u.Path = "/user2/repo1.git"

		// use up the only token of this client
		resp, err := http.Get(u.String() + "/info/refs?service=git-upload-pack")
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		err = git.CloneWithArgs(t.Context(), nil, u.String(), filepath.Join(t.TempDir(), "repo1"), git.CloneRepoOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "remote: rate limit exceeded, retry in")
	})
}

func TestRateLimitSignIn(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	mockRateLimit(t, map[string]setting.RateLimitClass{
		ratelimit.Auth: {AuthenticatedRate: slow, AuthenticatedBurst: 2},
	}, "")
	signIn := func(ip, user, password string, status int) {
		t.Helper()
		req := NewRequestWithValues(t, "POST", "/user/login", map[string]string{"user_name": user, "password": password})
		MakeRequest(t, fromIP(req, ip), status)
	}

	t.Run("successful sign-ins never consume", func(t *testing.T) {
		for range 5 {
			signIn("192.0.2.1", "user2", userPassword, http.StatusSeeOther)
		}
	})

	t.Run("failures per username and per IP", func(t *testing.T) {
		signIn("192.0.2.1", "user2", "wrong", http.StatusOK)
		signIn("192.0.2.1", "user2", "wrong", http.StatusOK)
		// the user name is exhausted, from any IP and in any case, even with the right password
		signIn("192.0.2.2", "User2", userPassword, http.StatusTooManyRequests)
		// the IP is exhausted for every user name
		signIn("192.0.2.1", "user4", userPassword, http.StatusTooManyRequests)
		signIn("192.0.2.3", "user4", userPassword, http.StatusSeeOther)
	})

	t.Run("basic auth", func(t *testing.T) {
		basic := func(ip, path, user, password string, status int) *http.Response {
			t.Helper()
			req := fromIP(NewRequest(t, "GET", path), ip)
			req.Request.SetBasicAuth(user, password)
			return MakeRequest(t, req, status).Result()
		}
		for range 5 {
			basic("192.0.2.10", "/api/v1/user", "user4", userPassword, http.StatusOK)
		}
		basic("192.0.2.10", "/api/v1/user", "user4", "wrong", http.StatusUnauthorized)
		basic("192.0.2.11", "/api/v1/user", "user4", "wrong", http.StatusUnauthorized)
		// 429 rather than 401, so that git credential helpers keep the stored password
		resp := basic("192.0.2.12", "/api/v1/user", "user4", userPassword, http.StatusTooManyRequests)
		assert.NotEmpty(t, resp.Header.Get("Retry-After"))
		resp = basic("192.0.2.12", "/user2/repo1.git/info/refs?service=git-upload-pack", "user4", userPassword, http.StatusTooManyRequests)
		assert.Contains(t, resp.Header.Get("Content-Type"), "text/plain")
		basic("192.0.2.12", "/api/packages/user2/generic/pkg/1.0/file.txt", "user4", userPassword, http.StatusTooManyRequests)

		// another user name from an exhausted IP
		basic("192.0.2.10", "/api/v1/user", "user8", "wrong", http.StatusUnauthorized)
		basic("192.0.2.10", "/api/v1/user", "user8", userPassword, http.StatusTooManyRequests)
	})
}

func TestRateLimitTwoFactor(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	session := loginUser(t, "user2")
	session.EnrollTOTP(t)
	session.MakeRequest(t, NewRequest(t, "POST", "/user/logout"), http.StatusSeeOther)

	mockRateLimit(t, map[string]setting.RateLimitClass{
		ratelimit.Auth: {AuthenticatedRate: slow, AuthenticatedBurst: 2},
	}, "")
	session = loginUser(t, "user2") // stops at the 2FA page
	twoFactor := func(ip string, status int) {
		t.Helper()
		req := NewRequestWithValues(t, "POST", "/user/two_factor", map[string]string{"passcode": "000000"})
		session.MakeRequest(t, fromIP(req, ip), status)
		req = NewRequestWithValues(t, "POST", "/user/two_factor/scratch", map[string]string{"token": "wrongtoken"})
		session.MakeRequest(t, fromIP(req, ip), status)
	}
	twoFactor("192.0.2.1", http.StatusOK)
	// the same 2FA session from another IP shares the per-user bucket
	twoFactor("192.0.2.2", http.StatusTooManyRequests)
}

func TestRateLimitLinkAccount(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	gitlabName := "gitlab"
	addAuthSource(t, authSourcePayloadGitLabCustom(gitlabName))
	defer mockCompleteUserAuth(func(res http.ResponseWriter, req *http.Request) (goth.User, error) {
		return goth.User{Provider: gitlabName, UserID: "5678"}, nil
	})()
	mockRateLimit(t, map[string]setting.RateLimitClass{
		ratelimit.Auth: {AuthenticatedRate: slow, AuthenticatedBurst: 2},
	}, "")

	session := emptyTestSession(t)
	req := NewRequest(t, "GET", fmt.Sprintf("/user/oauth2/%s/callback?code=XYZ&state=XYZ", gitlabName))
	session.MakeRequest(t, req, http.StatusSeeOther)
	linkSignIn := func(ip, password string, status int) {
		t.Helper()
		req := NewRequestWithValues(t, "POST", "/user/link_account_signin", map[string]string{"user_name": "user2", "password": password})
		session.MakeRequest(t, fromIP(req, ip), status)
	}
	linkSignIn("192.0.2.1", "wrong", http.StatusOK)
	linkSignIn("192.0.2.2", "wrong", http.StatusOK)
	linkSignIn("192.0.2.3", userPassword, http.StatusTooManyRequests)
	// the web sign-in form shares the per-username bucket
	req = NewRequestWithValues(t, "POST", "/user/login", map[string]string{"user_name": "user2", "password": userPassword})
	MakeRequest(t, fromIP(req, "192.0.2.4"), http.StatusTooManyRequests)
}

func TestRateLimitAuthIPEndpoints(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	mockRateLimit(t, map[string]setting.RateLimitClass{
		ratelimit.Auth: {AuthenticatedRate: slow, AuthenticatedBurst: 2},
	}, "")

	// every request counts, successful or not
	for range 2 {
		req := NewRequestWithValues(t, "POST", "/user/forgot_password", map[string]string{"email": "user2@example.com"})
		MakeRequest(t, fromIP(req, "192.0.2.1"), http.StatusOK)
	}
	for _, path := range []string{"/user/forgot_password", "/user/sign_up", "/user/activate", "/user/recover_account"} {
		MakeRequest(t, fromIP(NewRequest(t, "POST", path), "192.0.2.1"), http.StatusTooManyRequests)
	}
}
