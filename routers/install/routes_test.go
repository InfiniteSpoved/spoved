// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package install

import (
	"net/http/httptest"
	"strings"
	"testing"

	audit_model "forgejo.org/models/audit"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"
	"forgejo.org/modules/web"
	"forgejo.org/routers/common"
	"forgejo.org/services/context"

	"github.com/stretchr/testify/assert"
)

func TestRoutes(t *testing.T) {
	r := Routes()
	assert.NotNil(t, r)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, 200, w.Code)
	assert.Contains(t, w.Body.String(), `class="page-content install"`)
	// the config path shown on the page comes from the checkout directory, which may contain the product name
	assert.Empty(t, test.BrandMentions(strings.ReplaceAll(w.Body.String(), setting.CustomConf, "")))

	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/no-such", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, 404, w.Code)

	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/assets/img/forgejo.svg", nil)
	r.ServeHTTP(w, req)
	assert.Equal(t, 200, w.Code)
}

func TestMain(m *testing.M) {
	unittest.MainTest(m, &unittest.TestOptions{IniFileOverride: "install.ini"})
}

func TestContexterAuditActor(t *testing.T) {
	var got *audit_model.Actor
	r := web.NewRoute()
	r.Use(common.ProtocolMiddlewares()...)
	r.Use(common.Sessioner(), Contexter())
	r.Get("/", func(ctx *context.Context) {
		got = audit_model.ActorFromContext(ctx)
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	assert.Equal(t, audit_model.SystemActor("install"), got)
}
