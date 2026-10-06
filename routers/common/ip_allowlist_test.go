// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package common

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"
	"forgejo.org/modules/web"

	"github.com/stretchr/testify/assert"
)

// AC7: the client IP comes from X-Forwarded-For/X-Real-IP only when the TCP peer is a trusted proxy
func TestClientIPFromTrustedProxyOnly(t *testing.T) {
	defer test.MockVariableValue(&setting.ReverseProxyLimit, 1)()
	defer test.MockVariableValue(&setting.ReverseProxyTrustedProxies, []string{"127.0.0.0/8", "::1"})()
	r := web.NewRoute()
	r.Use(ProtocolMiddlewares()...)
	r.Get("/", func(w http.ResponseWriter, req *http.Request) { _, _ = w.Write([]byte(req.RemoteAddr)) })

	clientIP := func(peer string, header http.Header) string {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = peer
		req.Header = header
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, req)
		return setting.IPFromRemoteAddr(resp.Body.String()).String()
	}

	// an untrusted peer can't spoof its address with either header
	assert.Equal(t, "198.51.100.7", clientIP("198.51.100.7:1234", http.Header{"X-Forwarded-For": {"10.1.2.3"}}))
	assert.Equal(t, "198.51.100.7", clientIP("198.51.100.7:1234", http.Header{"X-Real-Ip": {"10.1.2.3"}}))

	// a trusted proxy sets the client address
	assert.Equal(t, "198.51.100.7", clientIP("127.0.0.1:1234", http.Header{"X-Forwarded-For": {"198.51.100.7"}}))
	assert.Equal(t, "198.51.100.7", clientIP("[::1]:1234", http.Header{"X-Real-Ip": {"198.51.100.7"}}))
	assert.Equal(t, "198.51.100.7", clientIP("127.0.0.1:1234", http.Header{"X-Forwarded-For": {"10.1.2.3, 198.51.100.7"}}))

	// a proxy that appends X-Forwarded-For passes the client's own X-Real-IP on: X-Forwarded-For wins
	assert.Equal(t, "198.51.100.7", clientIP("127.0.0.1:1234", http.Header{"X-Forwarded-For": {"198.51.100.7"}, "X-Real-Ip": {"10.1.2.3"}}))
	// a proxy that adds its own X-Forwarded-For line keeps the client's line first: the last address wins
	assert.Equal(t, "198.51.100.7", clientIP("127.0.0.1:1234", http.Header{"X-Forwarded-For": {"10.1.2.3", "198.51.100.7"}}))
}
