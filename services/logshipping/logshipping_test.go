// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package logshipping

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	system_model "forgejo.org/models/system"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/json"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/setting/config"
	"forgejo.org/modules/test"
	"forgejo.org/modules/util"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testKey    = "NRAK-UNITTESTKEY0123456789"
	testHeader = "Bearer unit-test-header-value"
)

// reset removes every destination, also from the loggers, and the saved state.
func reset(t *testing.T) {
	t.Helper()
	unittest.PrepareTestEnv(t)
	require.NoError(t, Apply(t.Context(), setting.LogShippingState{}))
	require.NoError(t, system_model.SetSettings(t.Context(), map[string]string{setting.Config().LogShipping.DynKey(): ""}))
	config.GetDynGetter().InvalidateCache()
}

func allowLoopback(t *testing.T) {
	t.Helper()
	t.Cleanup(test.MockVariableValue(&setting.Webhook.AllowedHostList, "loopback"))
}

func writersOf(logger string) []string {
	var names []string
	m := log.GetManager().DumpLoggers()
	if l, ok := m[logger].(map[string]any); ok {
		if w, ok := l["EventWriters"].(map[string]any); ok {
			for k := range w {
				names = append(names, k)
			}
		}
	}
	slices.Sort(names)
	return names
}

func admin(t *testing.T) *user_model.User {
	return &user_model.User{ID: 1, Name: "user1", IsAdmin: true}
}

func newRelic(endpoint string, logTypes ...string) Input {
	return Input{Name: new("nr"), Type: new("newrelic"), Endpoint: &endpoint, Key: new(testKey), LogTypes: &logTypes}
}

func TestEndpointHostPolicy(t *testing.T) {
	for endpoint, ok := range map[string]bool{
		"https://logs.example.org/in":    true, // names are checked when dialled
		"https://8.8.8.8/in":             true,
		"https://10.0.0.1/in":            false,
		"https://192.168.1.5:8443/in":    false,
		"https://[::ffff:10.0.0.1]/in":   false, // IPv4-mapped IPv6
		"https://[fd00::1]/in":           false,
		"https://100.64.0.1/in":          false,
		"http://127.0.0.1:3000/in":       false,
		"https://[::1]/in":               false,
		"https://[::ffff:127.0.0.1]/in":  false,
		"https://169.254.169.254/latest": false,
	} {
		err := CheckEndpoint(endpoint)
		assert.Equal(t, ok, err == nil, endpoint)
	}
	allowLoopback(t)
	assert.NoError(t, CheckEndpoint("http://127.0.0.1:3000/in"))
	assert.NoError(t, CheckEndpoint("https://[::ffff:127.0.0.1]/in"))
	assert.Error(t, CheckEndpoint("https://10.0.0.1/in"))
}

func TestDialContextChecksTheResolvedAddress(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	// "localhost" is a name the save-time check lets through; the dial resolves it to a loopback address
	addr := strings.Replace(l.Addr().String(), "127.0.0.1", "localhost", 1)

	_, err = DialContext()(t.Context(), "tcp", addr)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), testKey)

	allowLoopback(t)
	c, err := DialContext()(t.Context(), "tcp", addr)
	require.NoError(t, err)
	c.Close()
}

func TestSaveStoresEncryptedAndNeverReturnsSecrets(t *testing.T) {
	reset(t)
	defer reset(t)
	allowLoopback(t)

	d, err := Save(t.Context(), admin(t), "", Input{
		Name: new("generic"), Type: new("http"), Endpoint: new("https://127.0.0.1:9/in"),
		Headers:  &[]HeaderInput{{Name: "Authorization", Value: testHeader}},
		LogTypes: &[]string{"access"},
	})
	require.NoError(t, err)
	assert.Len(t, d.ID, 8)

	row := unittest.AssertExistsAndLoadBean(t, &system_model.Setting{SettingKey: setting.Config().LogShipping.DynKey()})
	assert.NotContains(t, row.SettingValue, testHeader)
	assert.Contains(t, row.SettingValue, "Authorization")

	_, err = Save(t.Context(), admin(t), "", newRelic("https://127.0.0.1:9/nr", "all"))
	require.NoError(t, err)
	row = unittest.AssertExistsAndLoadBean(t, &system_model.Setting{SettingKey: setting.Config().LogShipping.DynKey()})
	assert.NotContains(t, row.SettingValue, testKey)

	list, err := List(t.Context())
	require.NoError(t, err)
	b, err := json.Marshal(list)
	require.NoError(t, err)
	assert.NotContains(t, string(b), testKey)
	assert.NotContains(t, string(b), testHeader)
	assert.Contains(t, string(b), `"has_key":true`)
	assert.Contains(t, string(b), "Authorization")
}

func TestSaveValidationAndNames(t *testing.T) {
	reset(t)
	defer reset(t)
	allowLoopback(t)
	u := admin(t)

	_, err := Save(t.Context(), u, "", newRelic("https://127.0.0.1:9/nr", "access"))
	require.NoError(t, err)

	for name, in := range map[string]Input{
		"duplicate name": newRelic("https://127.0.0.1:9/other", "access"),
		"bad name":       {Name: new("Bad Name"), Type: new("http"), Endpoint: new("https://127.0.0.1:9/x"), LogTypes: &[]string{"access"}},
		"private host":   {Name: new("p"), Type: new("http"), Endpoint: new("https://10.0.0.1/x"), LogTypes: &[]string{"access"}},
		"userinfo":       {Name: new("u"), Type: new("http"), Endpoint: new("https://a:b@127.0.0.1:9/x"), LogTypes: &[]string{"access"}},
		"no key":         {Name: new("k"), Type: new("newrelic"), Endpoint: new("https://127.0.0.1:9/x"), LogTypes: &[]string{"access"}},
		"long header":    {Name: new("h"), Type: new("http"), Endpoint: new("https://127.0.0.1:9/x"), LogTypes: &[]string{"access"}, Headers: &[]HeaderInput{{Name: "X-A", Value: strings.Repeat("a", 1025)}}},
		"bad type":       {Name: new("t"), Type: new("splunk"), Endpoint: new("https://127.0.0.1:9/x"), LogTypes: &[]string{"access"}},
	} {
		_, err := Save(t.Context(), u, "", in)
		require.ErrorIs(t, err, util.ErrInvalidArgument, name)
		assert.NotContains(t, err.Error(), "a:b", name)
	}
	list, err := List(t.Context())
	require.NoError(t, err)
	assert.Len(t, list, 1)

	_, err = Save(t.Context(), u, "zzzzzzzz", Input{})
	require.ErrorIs(t, err, util.ErrNotExist)
}

func TestPatchEndpointChangeNeedsEverySecret(t *testing.T) {
	reset(t)
	defer reset(t)
	allowLoopback(t)
	u := admin(t)

	nr, err := Save(t.Context(), u, "", newRelic("https://127.0.0.1:9/nr", "access"))
	require.NoError(t, err)
	gen, err := Save(t.Context(), u, "", Input{
		Name: new("gen"), Type: new("http"), Endpoint: new("https://127.0.0.1:9/gen"), LogTypes: &[]string{"access"},
		Headers: &[]HeaderInput{{Name: "Authorization", Value: testHeader}, {Name: "X-Team", Value: "ops"}},
	})
	require.NoError(t, err)

	// a new endpoint without a new key, or without every header value, is refused and changes nothing
	_, err = Save(t.Context(), u, nr.ID, Input{Endpoint: new("https://127.0.0.1:9/elsewhere")})
	require.ErrorIs(t, err, util.ErrInvalidArgument)
	_, err = Save(t.Context(), u, gen.ID, Input{Endpoint: new("https://127.0.0.1:9/elsewhere")})
	require.ErrorIs(t, err, util.ErrInvalidArgument)
	_, err = Save(t.Context(), u, gen.ID, Input{Endpoint: new("https://127.0.0.1:9/elsewhere"), Headers: &[]HeaderInput{{Name: "Authorization", Value: testHeader}, {Name: "X-Team"}}})
	require.ErrorIs(t, err, util.ErrInvalidArgument)
	cur, _ := get(t.Context(), nr.ID)
	assert.Equal(t, "https://127.0.0.1:9/nr", cur.Endpoint)

	// with all secrets given it works
	_, err = Save(t.Context(), u, nr.ID, Input{Endpoint: new("https://127.0.0.1:9/elsewhere"), Key: new("NRAK-NEW")})
	require.NoError(t, err)
	_, err = Save(t.Context(), u, gen.ID, Input{Endpoint: new("https://127.0.0.1:9/elsewhere"), Headers: &[]HeaderInput{{Name: "Authorization", Value: "Bearer new"}, {Name: "X-Team", Value: "sre"}}})
	require.NoError(t, err)

	// with the endpoint unchanged the key and the header values stay, also when a header is sent without a value
	_, err = Save(t.Context(), u, nr.ID, Input{Enabled: new(false)})
	require.NoError(t, err)
	_, err = Save(t.Context(), u, gen.ID, Input{Headers: &[]HeaderInput{{Name: "authorization"}}})
	require.NoError(t, err)
	st := setting.Config().LogShipping.Value(t.Context())
	for _, d := range st.Destinations {
		switch d.ID {
		case nr.ID:
			k, err := decrypt(d.KeyEnc)
			require.NoError(t, err)
			assert.Equal(t, "NRAK-NEW", k)
			assert.False(t, d.Enabled)
		case gen.ID:
			require.Len(t, d.Headers, 1)
			v, err := decrypt(d.Headers[0].ValueEnc)
			require.NoError(t, err)
			assert.Equal(t, "Bearer new", v)
		}
	}
}

func TestApplyAttachesAndKeepsWriters(t *testing.T) {
	reset(t)
	defer reset(t)
	allowLoopback(t)
	t.Cleanup(test.MockVariableValue(&setLogSQL, func(context.Context, bool) {}))
	u := admin(t)

	d, err := Save(t.Context(), u, "", newRelic("https://127.0.0.1:9/nr", "access", "application"))
	require.NoError(t, err)
	name := "ship-" + d.ID
	assert.Contains(t, writersOf("access"), name)
	assert.Contains(t, writersOf("default"), name)
	assert.NotContains(t, writersOf("router"), name)
	w := log.GetManager().GetSharedWriter(name)
	require.NotNil(t, w)

	// an unrelated change elsewhere keeps this destination's writer (and its buffer)
	_, err = Save(t.Context(), u, "", Input{Name: new("other"), Type: new("http"), Endpoint: new("https://127.0.0.1:9/o"), LogTypes: &[]string{"router"}})
	require.NoError(t, err)
	assert.Same(t, w, log.GetManager().GetSharedWriter(name))

	// a changed destination gets a new writer on the new loggers only
	_, err = Save(t.Context(), u, d.ID, Input{LogTypes: &[]string{"router"}})
	require.NoError(t, err)
	assert.NotSame(t, w, log.GetManager().GetSharedWriter(name))
	assert.NotContains(t, writersOf("access"), name)
	assert.Contains(t, writersOf("router"), name)

	// disabled and deleted ones are gone from the loggers and the manager
	_, err = Save(t.Context(), u, d.ID, Input{Enabled: new(false)})
	require.NoError(t, err)
	assert.Nil(t, log.GetManager().GetSharedWriter(name))
	assert.NotContains(t, writersOf("router"), name)
	_, err = Save(t.Context(), u, d.ID, Input{Enabled: new(true)})
	require.NoError(t, err)
	require.NotNil(t, log.GetManager().GetSharedWriter(name))
	require.NoError(t, Remove(t.Context(), u, d.ID))
	assert.Nil(t, log.GetManager().GetSharedWriter(name))
	assert.NotContains(t, writersOf("router"), name)
	require.ErrorIs(t, Remove(t.Context(), u, d.ID), util.ErrNotExist)
}

func TestApplyTogglesSQLLogging(t *testing.T) {
	reset(t)
	defer reset(t)
	allowLoopback(t)
	var mu sync.Mutex
	var calls []bool
	t.Cleanup(test.MockVariableValue(&setLogSQL, func(_ context.Context, on bool) { mu.Lock(); calls = append(calls, on); mu.Unlock() }))
	t.Cleanup(test.MockVariableValue(&setting.Database.LogSQL, false))

	d, err := Save(t.Context(), admin(t), "", Input{Name: new("db"), Type: new("http"), Endpoint: new("https://127.0.0.1:9/x"), LogTypes: &[]string{"database"}})
	require.NoError(t, err)
	assert.True(t, calls[len(calls)-1])
	require.NoError(t, Remove(t.Context(), admin(t), d.ID))
	assert.False(t, calls[len(calls)-1])
}

func TestReloadOnlyWhenTheRevisionChanges(t *testing.T) {
	reset(t)
	defer reset(t)
	allowLoopback(t)
	t.Cleanup(test.MockVariableValue(&setLogSQL, func(context.Context, bool) {}))
	u := admin(t)

	d, err := Save(t.Context(), u, "", newRelic("https://127.0.0.1:9/nr", "access"))
	require.NoError(t, err)
	name := "ship-" + d.ID
	require.NoError(t, Reload(t.Context())) // first call applies
	require.NoError(t, Reload(t.Context()))
	w := log.GetManager().GetSharedWriter(name)
	require.NotNil(t, w)

	// someone else (another pod) changes the setting: only the revision moves, Reload applies it
	st := setting.Config().LogShipping.Value(t.Context())
	st.Destinations[0].Enabled = false
	b, _ := json.Marshal(st)
	require.NoError(t, system_model.SetSettings(t.Context(), map[string]string{setting.Config().LogShipping.DynKey(): string(b)}))
	require.NoError(t, Reload(t.Context()))
	assert.Nil(t, log.GetManager().GetSharedWriter(name))

	// no change of revision: nothing is rebuilt, even if the loggers were touched by hand
	st.Destinations[0].Enabled = true
	require.NoError(t, Apply(t.Context(), st))
	require.NotNil(t, log.GetManager().GetSharedWriter(name))
	require.NoError(t, Apply(t.Context(), setting.LogShippingState{}))
	require.NoError(t, Reload(t.Context()))
	assert.Nil(t, log.GetManager().GetSharedWriter(name), "same revision: Reload must not apply again")
}

func TestTestEventUsesSavedDestinationAndGenericErrors(t *testing.T) {
	reset(t)
	defer reset(t)
	allowLoopback(t)
	t.Cleanup(test.MockVariableValue(&setLogSQL, func(context.Context, bool) {}))

	var mu sync.Mutex
	status := 200
	var got []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		got = append(got, r.Header.Get("Authorization")+" "+string(b))
		w.WriteHeader(status)
		_, _ = w.Write([]byte("the remote says: " + testKey))
	}))
	defer srv.Close()
	t.Cleanup(test.MockVariableValue(&log.ShipTransport, srv.Client().Transport))
	t.Cleanup(test.MockVariableValue(&testInterval, 0*time.Second))

	d, err := Save(t.Context(), admin(t), "", Input{
		Name: new("gen"), Type: new("http"), Endpoint: new(srv.URL), LogTypes: &[]string{"access"},
		Headers: &[]HeaderInput{{Name: "Authorization", Value: testHeader}},
	})
	require.NoError(t, err)

	res, err := Test(t.Context(), d.ID)
	require.NoError(t, err)
	assert.Equal(t, 200, res.Status)
	assert.Empty(t, res.Error)
	require.Len(t, got, 1)
	assert.Contains(t, got[0], testHeader)
	assert.Contains(t, got[0], `"logtype":"test"`)

	mu.Lock()
	status = 503
	mu.Unlock()
	res, err = Test(t.Context(), d.ID)
	require.NoError(t, err)
	assert.Equal(t, 503, res.Status)
	assert.Equal(t, "status 5xx", res.Error)
	assert.NotContains(t, res.Error, testKey)

	_, err = Test(t.Context(), "zzzzzzzz")
	require.ErrorIs(t, err, util.ErrNotExist)

	// the endpoint is gone: a class of failure only
	srv.Close()
	res, err = Test(t.Context(), d.ID)
	require.NoError(t, err)
	assert.Zero(t, res.Status)
	assert.Contains(t, []string{"connection failed", "timeout"}, res.Error)
}

func TestTestEventIsRateLimited(t *testing.T) {
	reset(t)
	defer reset(t)
	allowLoopback(t)
	t.Cleanup(test.MockVariableValue(&setLogSQL, func(context.Context, bool) {}))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	t.Cleanup(test.MockVariableValue(&log.ShipTransport, srv.Client().Transport))

	d, err := Save(t.Context(), admin(t), "", Input{Name: new("gen"), Type: new("http"), Endpoint: new(srv.URL), LogTypes: &[]string{"access"}})
	require.NoError(t, err)
	_, err = Test(t.Context(), d.ID)
	require.NoError(t, err)
	_, err = Test(t.Context(), d.ID)
	require.ErrorIs(t, err, ErrRateLimited)
}

func TestSecretsAreRegisteredForRedaction(t *testing.T) {
	reset(t)
	defer reset(t)
	allowLoopback(t)
	t.Cleanup(test.MockVariableValue(&setLogSQL, func(context.Context, bool) {}))
	_, err := Save(t.Context(), admin(t), "", Input{
		Name: new("gen"), Type: new("http"), Endpoint: new("https://127.0.0.1:9/x"), LogTypes: &[]string{"access"},
		Headers: &[]HeaderInput{{Name: "Authorization", Value: testHeader}},
	})
	require.NoError(t, err)
	assert.NotContains(t, log.Redact("sending "+testHeader), testHeader)
	b, err := json.Marshal(log.GetManager().DumpLoggers())
	require.NoError(t, err)
	assert.NotContains(t, string(b), testHeader)
}
