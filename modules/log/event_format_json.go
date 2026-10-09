// Copyright 2026 The Forgejo Authors.
// SPDX-License-Identifier: GPL-3.0-or-later

package log

import (
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"forgejo.org/modules/json"
)

// ServiceName is the "service" field of every JSON log event.
const ServiceName = "spoved"

const (
	maxJSONMessage = 32 * 1024
	maxJSONField   = 2 * 1024 // path, user agent, ... come from the client
)

var (
	appVersion atomic.Value // string
	hostname   = sync.OnceValue(func() string { h, _ := os.Hostname(); return h })
)

// SetAppVersion sets the "app_version" field of JSON log events (the log package can't import setting).
func SetAppVersion(v string) { appVersion.Store(v) }

// truncate cuts s to max bytes; callers redact first, so a secret can't be cut into an unrecognisable piece
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[:max], "") + "…[truncated]"
}

// EventFormatJSONMessage formats the event as one redacted JSON object per line.
func EventFormatJSONMessage(mode *WriterMode, event *Event) []byte {
	m := make(map[string]any, len(event.Fields)+9)
	for k, v := range event.Fields {
		if s, ok := v.(string); ok {
			v = truncate(Redact(s), maxJSONField)
		}
		m[k] = v
	}
	logtype := event.Logger
	if logtype == "" || logtype == DEFAULT {
		logtype = "application"
	}
	ver, _ := appVersion.Load().(string)
	m["timestamp"] = event.Time.UnixMilli()
	m["message"] = truncate(Redact(strings.TrimSuffix(event.MsgSimpleText, "\n")), maxJSONMessage)
	m["level"] = event.Level.String()
	m["logtype"] = logtype
	m["logger"] = event.Logger
	m["hostname"] = hostname()
	m["service"] = ServiceName
	m["app_version"] = ver
	if event.Stacktrace != "" && mode.StacktraceLevel <= event.Level {
		m["stacktrace"] = truncate(Redact(event.Stacktrace), maxJSONMessage)
	}
	b, err := json.Marshal(m)
	if err != nil {
		return []byte(`{"message":"log event could not be encoded","service":"` + ServiceName + "\"}\n")
	}
	return append(b, '\n')
}
