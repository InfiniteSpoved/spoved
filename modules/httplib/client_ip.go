// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package httplib

import (
	"context"
	"net/http"
)

type ambiguousClientIPKey struct{}

// WithAmbiguousClientIP marks a request whose trusted proxy sent disagreeing X-Real-IP and X-Forwarded-For headers:
// its RemoteAddr may be an address the client chose.
func WithAmbiguousClientIP(req *http.Request) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), ambiguousClientIPKey{}, true))
}

// IsClientIPAmbiguous reports whether WithAmbiguousClientIP marked the request.
func IsClientIPAmbiguous(req *http.Request) bool {
	ambiguous, _ := req.Context().Value(ambiguousClientIPKey{}).(bool)
	return ambiguous
}
