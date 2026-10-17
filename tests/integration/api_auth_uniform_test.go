// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"forgejo.org/modules/jwtx"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"
	"forgejo.org/tests"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A refused API credential gets one generic body whatever the reason; the reason goes to the server log.
func TestAPIAuthUniformErrors(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	ait := newAITester(t)
	defer ait.close()

	refused := func(t *testing.T, req *RequestWrapper) string {
		t.Helper()
		resp := MakeRequest(t, req, http.StatusUnauthorized)
		assert.JSONEq(t, `{"message":"invalid credentials","url":"`+setting.API.SwaggerURL+`"}`, resp.Body.String())
		return resp.Body.String()
	}
	sign := func(key jwtx.SigningKey, claims jwt.MapClaims) string {
		token, err := key.JWT(claims)
		require.NoError(t, err)
		return token
	}
	claims := func(override jwt.MapClaims) jwt.MapClaims {
		c := jwt.MapClaims{
			"iss":          ait.authorizedIntegration.Issuer,
			"aud":          ait.authorizedIntegration.Audience,
			"custom-claim": "custom-claim-value",
		}
		for k, v := range override {
			c[k] = v
		}
		return c
	}
	otherKey, err := jwtx.InitAsymmetricSigningKey(filepath.Join(t.TempDir(), "other.priv"), "RS256")
	require.NoError(t, err)

	t.Run("authorized integration", func(t *testing.T) {
		logChecker, cleanup := test.NewLogChecker(log.DEFAULT, log.INFO)
		defer cleanup()

		valid := ait.signedJWT()
		badSignature := valid[:len(valid)-4] + "AAAA"
		unknown := sign(ait.jwtSigningKey, claims(jwt.MapClaims{"aud": "https://example.org/unknown-audience"}))
		mismatch := sign(ait.jwtSigningKey, claims(jwt.MapClaims{"custom-claim": "other-value"}))
		logChecker.Filter("claim mismatch", "matching authorized_integration not found", badSignature, unknown, mismatch)

		body := refused(t, NewRequest(t, "GET", "/api/v1/user").AddTokenAuth(badSignature))
		for name, token := range map[string]string{
			"unknown integration": unknown,
			"claim mismatch":      mismatch,
			"expired":             sign(ait.jwtSigningKey, claims(jwt.MapClaims{"exp": time.Now().Add(-time.Hour).Unix()})),
			"wrong kid":           sign(otherKey, claims(nil)),
		} {
			got := refused(t, NewRequest(t, "GET", "/api/v1/user").AddTokenAuth(token))
			assert.Equal(t, body, got, name)
			assert.NotContains(t, got, "custom-claim", name)
		}

		filtered, _ := logChecker.Check(5 * time.Second)
		assert.Equal(t, []bool{true, true, false, false, false}, filtered, "reasons logged, tokens never")

		MakeRequest(t, NewRequest(t, "GET", "/api/v1/user").AddTokenAuth(valid), http.StatusOK)
	})

	t.Run("basic", func(t *testing.T) {
		logChecker, cleanup := test.NewLogChecker(log.DEFAULT, log.INFO)
		defer cleanup()
		basic := func(username, password string) *RequestWrapper {
			req := NewRequest(t, "GET", "/api/v1/user")
			req.SetBasicAuth(username, password)
			return req
		}
		const pat = "0123456789abcdef0123456789abcdef01234567"
		logChecker.Filter("unknown user or bad password", "\n2026/01/01 forged line", pat)

		body := refused(t, NewRequest(t, "GET", "/api/v1/user").AddBasicAuth("no-such-user"))
		for name, req := range map[string]*RequestWrapper{
			"wrong password": basic("user2", "wrong"),
			"unknown email":  basic("nobody@example.com", "password"),
			"crlf username":  basic("x\r\n2026/01/01 forged line", "password"),
			"token as name":  basic(pat, ""),
		} {
			assert.Equal(t, body, refused(t, req), name)
		}

		filtered, _ := logChecker.Check(5 * time.Second)
		assert.Equal(t, []bool{true, false, false}, filtered, "reason logged, user input never")
	})
}
