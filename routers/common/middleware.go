// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package common

import (
	"fmt"
	"net/http"
	"net/netip"
	"runtime/trace"
	"slices"
	"strings"

	"forgejo.org/modules/cache"
	"forgejo.org/modules/process"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/web/middleware"
	"forgejo.org/modules/web/routing"
	"forgejo.org/services/context"

	"code.forgejo.org/go-chi/session"
	"github.com/chi-middleware/proxy"
	chi "github.com/go-chi/chi/v5"
)

// ProtocolMiddlewares returns HTTP protocol related middlewares, and it provides a global panic recovery
func ProtocolMiddlewares() (handlers []any) {
	// first, normalize the URL path
	handlers = append(handlers, stripSlashesMiddleware)

	// prepare the ContextData and panic recovery
	handlers = append(handlers, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					if err == http.ErrAbortHandler {
						panic(err) // net/http cuts the connection; an error page would end the started body normally
					}
					RenderPanicErrorPage(resp, req, err) // it should never panic
				}
			}()
			req = req.WithContext(middleware.WithContextData(req.Context()))
			next.ServeHTTP(resp, req)
		})
	})

	// wrap the request and response, use the process context and add it to the process manager
	handlers = append(handlers, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
			ctx, _, finished := process.GetManager().AddTypedContext(req.Context(), fmt.Sprintf("%s: %s", req.Method, req.RequestURI), process.RequestProcessType, true)
			defer finished()
			trace.Log(ctx, "method", req.Method)
			trace.Log(ctx, "url", req.RequestURI)

			respWriter := context.WrapResponseWriter(resp)
			next.ServeHTTP(respWriter, req.WithContext(cache.WithCacheContext(ctx)))

			trace.Logf(ctx, "status", "%d", respWriter.WrittenStatus())
		})
	})

	if setting.ReverseProxyLimit > 0 {
		opt := proxy.NewForwardedHeadersOptions().
			WithForwardLimit(setting.ReverseProxyLimit).
			ClearTrustedProxies()
		for _, n := range setting.ReverseProxyTrustedProxies {
			if !strings.Contains(n, "/") {
				opt.AddTrustedProxy(n)
			} else {
				opt.AddTrustedNetwork(n)
			}
		}
		handlers = append(handlers, joinForwardedFor(setting.ReverseProxyTrustedProxies), proxy.ForwardedHeaders(opt))
	}

	if setting.IsRouteLogEnabled() {
		handlers = append(handlers, routing.NewLoggerHandler())
	}

	if setting.IsAccessLogEnabled() {
		handlers = append(handlers, context.AccessLogger())
	}

	return handlers
}

// joinForwardedFor makes proxy.ForwardedHeaders see the address a trusted proxy appended: that reads only the first
// X-Forwarded-For line and prefers X-Real-IP, both of which a client can send through a proxy that only appends
// X-Forwarded-For. So when a trusted peer sends any X-Forwarded-For, all its lines are joined and X-Real-IP is dropped.
func joinForwardedFor(trusted []string) func(http.Handler) http.Handler {
	var prefixes []netip.Prefix
	for _, n := range trusted {
		if p, err := netip.ParsePrefix(n); err == nil {
			prefixes = append(prefixes, p)
		} else if a, err := netip.ParseAddr(n); err == nil {
			prefixes = append(prefixes, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
			xff := req.Header.Values("X-Forwarded-For")
			if len(xff) == 0 {
				next.ServeHTTP(resp, req)
				return
			}
			peer := setting.IPFromRemoteAddr(req.RemoteAddr)
			if req.RemoteAddr == "@" { // a unix socket, which proxy.ForwardedHeaders trusts as 127.0.0.1
				peer = netip.AddrFrom4([4]byte{127, 0, 0, 1})
			}
			if slices.ContainsFunc(prefixes, func(p netip.Prefix) bool { return p.Contains(peer.Unmap()) }) {
				req.Header.Set("X-Forwarded-For", strings.Join(xff, ", "))
				req.Header.Del("X-Real-IP")
			}
			next.ServeHTTP(resp, req)
		})
	}
}

func stripSlashesMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		// Ensure that URL.RawPath is always set.
		req.URL.RawPath = req.URL.EscapedPath()

		sanitize := func(path string) string {
			sanitizedPath := &strings.Builder{}
			prevWasSlash := false
			for _, chr := range strings.TrimRight(path, "/") {
				if chr != '/' || !prevWasSlash {
					sanitizedPath.WriteRune(chr)
				}
				prevWasSlash = chr == '/'
			}
			return sanitizedPath.String()
		}

		// Sanitize the unescaped path for application logic.
		req.URL.Path = sanitize(req.URL.Path)
		rctx := chi.RouteContext(req.Context())
		if rctx != nil {
			// Sanitize the escaped path for routing.
			rctx.RoutePath = sanitize(req.URL.RawPath)
		}
		next.ServeHTTP(resp, req)
	})
}

func Sessioner() func(next http.Handler) http.Handler {
	return session.Sessioner(session.Options{
		Provider:       setting.SessionConfig.Provider,
		ProviderConfig: setting.SessionConfig.ProviderConfig,
		CookieName:     setting.SessionConfig.CookieName,
		CookiePath:     setting.SessionConfig.CookiePath,
		Gclifetime:     setting.SessionConfig.Gclifetime,
		Maxlifetime:    setting.SessionConfig.Maxlifetime,
		Secure:         setting.SessionConfig.Secure,
		SameSite:       setting.SessionConfig.SameSite,
		Domain:         setting.SessionConfig.Domain,
		DeferSetCookie: true,
	})
}
