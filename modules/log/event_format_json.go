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

const maxJSONMessage = 32 * 1024

var (
	appVersion atomic.Value // string
	hostname   = sync.OnceValue(func() string { h, _ := os.Hostname(); return h })
)

// SetAppVersion sets the "app_version" field of JSON log events (the log package can't import setting).
func SetAppVersion(v string) { appVersion.Store(v) }

func truncateMessage(s string) string {
	if len(s) <= maxJSONMessage {
		return s
	}
	return strings.ToValidUTF8(s[:maxJSONMessage], "") + "…[truncated]"
}

// EventFormatJSONMessage formats the event as one redacted JSON object per line.
func EventFormatJSONMessage(mode *WriterMode, event *Event, _ string, _ ...any) []byte {
	m := make(map[string]any, len(event.Fields)+9)
	for k, v := range event.Fields {
		if s, ok := v.(string); ok {
			v = Redact(s)
		}
		m[k] = v
	}
	logtype := event.Logger
	if logtype == "" || logtype == DEFAULT {
		logtype = "application"
	}
	ver, _ := appVersion.Load().(string)
	m["timestamp"] = event.Time.UnixMilli()
	m["message"] = truncateMessage(Redact(strings.TrimSuffix(event.MsgSimpleText, "\n")))
	m["level"] = event.Level.String()
	m["logtype"] = logtype
	m["logger"] = event.Logger
	m["hostname"] = hostname()
	m["service"] = ServiceName
	m["app_version"] = ver
	if event.Stacktrace != "" && mode.StacktraceLevel <= event.Level {
		m["stacktrace"] = truncateMessage(Redact(event.Stacktrace))
	}
	b, err := json.Marshal(m)
	if err != nil {
		return []byte(`{"message":"log event could not be encoded","service":"` + ServiceName + "\"}\n")
	}
	return append(b, '\n')
}
