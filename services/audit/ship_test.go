// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package audit

import (
	"cmp"
	"slices"
	"sync"
	"testing"
	"time"

	audit_model "forgejo.org/models/audit"
	"forgejo.org/models/db"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/json"
	"forgejo.org/modules/log"
	"forgejo.org/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capture struct {
	mu      sync.Mutex
	lines   []string
	block   chan struct{}
	started chan struct{}
}

func (c *capture) Write(p []byte) (int, error) {
	if c.block != nil {
		c.started <- struct{}{}
		<-c.block
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, string(p))
	return len(p), nil
}
func (c *capture) Close() error { return nil }

func (c *capture) get() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.lines...)
}

// shipTo adds a writer with the JSON format to the audit logger, as the log shipping destinations do.
func shipTo(t *testing.T, c *capture, bufferLen int) {
	t.Helper()
	w := log.NewEventWriterBase("audit-capture", "capture", log.WriterMode{Level: log.TRACE, BufferLen: bufferLen})
	w.FormatEvent = log.EventFormatJSONMessage
	w.OutputWriteCloser = c
	l := log.GetManager().GetLogger("audit")
	l.AddWriters(w)
	t.Cleanup(func() { _ = l.RemoveWriter("audit-capture") })
}

func TestSealShipsEachSealedRowOnce(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	const secret = "audit-ship-secret-0001"
	log.RegisterSecret(secret)
	c := &capture{}
	shipTo(t, c, 1000)

	insertUnsealed(t, 2)
	require.NoError(t, db.Insert(t.Context(), &audit_model.Entry{
		CreatedUnix: timeutil.TimeStamp(2000), Category: "auth", Action: "auth.signin.failed", Result: "failure", ActorType: "user", ActorID: 2,
		IP: "203.0.113.5", UserAgent: "UA/1", Data: `{"token":"` + secret + `"}`,
	}))
	require.NoError(t, SealAll(t.Context()))
	require.Eventually(t, func() bool { return len(c.get()) == 4 }, 5*time.Second, 10*time.Millisecond)

	var events []map[string]any
	for _, l := range c.get() {
		assert.NotContains(t, l, secret) // data goes through the same redaction as every field
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(l), &m))
		events = append(events, m)
	}
	entries := allEntries(t) // by id; audit.chain.started has the highest id but seq 1
	require.Len(t, entries, 4)
	slices.SortFunc(entries, func(a, b *audit_model.Entry) int { return cmp.Compare(*a.Seq, *b.Seq) })
	for i, e := range entries {
		m := events[i]
		assert.Equal(t, e.Action, m["message"], "the message is the action key only")
		assert.Equal(t, e.Action, m["action"])
		assert.EqualValues(t, *e.Seq, m["seq"])
		assert.Equal(t, *e.Hash, m["hash"])
		assert.Equal(t, "audit", m["logtype"])
		assert.IsType(t, "", m["data"])
	}
	assert.Equal(t, "audit.chain.started", events[0]["action"])
	failed := events[3]
	assert.Equal(t, "warn", failed["level"]) // result != success
	assert.Equal(t, "info", events[1]["level"])
	assert.Equal(t, "203.0.113.5", failed["ip"])
	assert.Equal(t, "UA/1", failed["user_agent"])
	assert.EqualValues(t, 2, failed["actor_id"])

	// nothing new, nothing shipped
	require.NoError(t, SealAll(t.Context()))
	time.Sleep(50 * time.Millisecond)
	assert.Len(t, c.get(), 4)
}

// A writer that never finishes does not slow or fail sealing.
func TestSealDoesNotWaitForTheWriter(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	c := &capture{block: make(chan struct{}), started: make(chan struct{}, 100)}
	shipTo(t, c, 1)
	t.Cleanup(func() { close(c.block) }) // runs before the writer is removed, which waits for it

	insertUnsealed(t, 20)
	done := make(chan error, 1)
	go func() { done <- SealAll(t.Context()) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Seal waited for the writer")
	}
	assert.EqualValues(t, 21, mustVerify(t).LastSeq)
}
