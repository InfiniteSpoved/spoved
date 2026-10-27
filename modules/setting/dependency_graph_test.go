// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package setting

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forgejo.org/modules/secret"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDependencyGraphSettings(t *testing.T) {
	cfg, err := NewConfigProviderFromData("")
	require.NoError(t, err)
	require.NoError(t, loadDependencyGraphFrom(cfg))
	assert.False(t, DependencyGraph.Enabled)
	assert.EqualValues(t, 20<<20, DependencyGraph.MaxManifestSize)
	assert.Equal(t, 500, DependencyGraph.MaxManifests)
	assert.Equal(t, 50000, DependencyGraph.MaxDependencies)
	assert.Equal(t, 200000, DependencyGraph.MaxTreeEntries)
	assert.Equal(t, 60*time.Second, DependencyGraph.ScanTimeout)

	cfg, err = NewConfigProviderFromData("[dependency_graph]\nENABLED = true\nMAX_MANIFESTS = 3\nSCAN_TIMEOUT = 5s\nMAX_DEPENDENCIES = -4\n")
	require.NoError(t, err)
	require.NoError(t, loadDependencyGraphFrom(cfg))
	assert.True(t, DependencyGraph.Enabled)
	assert.Equal(t, 3, DependencyGraph.MaxManifests)
	assert.Equal(t, 5*time.Second, DependencyGraph.ScanTimeout)
	assert.Equal(t, 1, DependencyGraph.MaxDependencies)
	DependencyGraph.Enabled = false
}

func TestOSVMirrorSettings(t *testing.T) {
	load := func(ini string) error {
		cfg, err := NewConfigProviderFromData(ini)
		require.NoError(t, err)
		return loadDependencyGraphFrom(cfg)
	}
	t.Cleanup(func() { require.NoError(t, load("")) })

	require.NoError(t, load(""))
	assert.Equal(t, DefaultOSVMirrorURL, DependencyGraph.OSVMirrorURL)
	assert.Equal(t, 24*time.Hour, DependencyGraph.UpdateInterval)

	require.NoError(t, load("[dependency_graph]\nOSV_MIRROR_URL = http://mirror.example:8080/osv/\nUPDATE_INTERVAL = 1m\n"))
	assert.Equal(t, "http://mirror.example:8080/osv", DependencyGraph.OSVMirrorURL)
	assert.Equal(t, time.Hour, DependencyGraph.UpdateInterval, "raised to the minimum")

	require.NoError(t, load("[dependency_graph]\nOSV_MIRROR_URL =\nUPDATE_INTERVAL = 9000h\n"))
	assert.Empty(t, DependencyGraph.OSVMirrorURL, "empty: no download")
	assert.Equal(t, MaxUpdateInterval, DependencyGraph.UpdateInterval)

	for _, bad := range []string{
		"ftp://mirror.example/", "https://user:pw@mirror.example/", "https://mirror.example/?a=b", "https:///x", "mirror.example", "http://[::1",
	} {
		assert.Error(t, load("[dependency_graph]\nOSV_MIRROR_URL = "+bad+"\n"), bad)
	}
}

func TestOSVMirrorCredentialSettings(t *testing.T) {
	load := func(ini string) error {
		cfg, err := NewConfigProviderFromData(ini)
		require.NoError(t, err)
		return loadDependencyGraphFrom(cfg)
	}
	t.Cleanup(func() { require.NoError(t, load("")) })

	require.NoError(t, load("[dependency_graph]\nOSV_MIRROR_URL = https://art.example/artifactory/osv\nOSV_MIRROR_TOKEN = s3cret-token-123\nOSV_MIRROR_USER = ci\nOSV_MIRROR_ALLOWED_HOSTS = private, loopback\n"))
	assert.Equal(t, "s3cret-token-123", DependencyGraph.OSVMirrorToken)
	assert.Equal(t, "ci", DependencyGraph.OSVMirrorUser)
	assert.Equal(t, "private, loopback", DependencyGraph.OSVMirrorHosts)

	file := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(file, []byte("from-file-token\n"), 0o600))
	require.NoError(t, load("[dependency_graph]\nOSV_MIRROR_TOKEN_URI = file:"+file+"\n"))
	assert.Equal(t, "from-file-token", DependencyGraph.OSVMirrorToken)

	err := load("[dependency_graph]\nOSV_MIRROR_TOKEN = abcdefgh\nOSV_MIRROR_TOKEN_URI = file:" + file + "\n")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "from-file")
	require.Error(t, load("[dependency_graph]\nOSV_MIRROR_TOKEN_URI = http://x/y\n"))
	require.Error(t, load("[dependency_graph]\nOSV_MIRROR_TOKEN_URI = file:/does/not/exist\n"), "an error, not an exit")

	err = load("[dependency_graph]\nOSV_MIRROR_TOKEN = short\n")
	require.Error(t, err, "under 8 characters")
	assert.NotContains(t, err.Error(), "short")
	require.Error(t, load("[dependency_graph]\nOSV_MIRROR_TOKEN = "+strings.Repeat("a", 1025)+"\n"))
	require.NoError(t, load("[dependency_graph]\nOSV_MIRROR_TOKEN = "+strings.Repeat("a", 1024)+"\n"))
	require.Error(t, load("[dependency_graph]\nOSV_MIRROR_USER = ci\n"), "a user without a token")
	require.NoError(t, load("[dependency_graph]\nOSV_MIRROR_ALLOWED_HOSTS = external, 10.0.0.0/8, *.corp.example, art.example:8443\n"))
	for _, bad := range []string{"10.0.0.0/33", "bad host", "http://art.example", "a|b"} {
		require.Error(t, load("[dependency_graph]\nOSV_MIRROR_ALLOWED_HOSTS = "+bad+"\n"), bad)
	}

	err = load("[dependency_graph]\nOSV_MIRROR_URL = https://user:pw@art.example/x\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OSV_MIRROR_TOKEN")
	assert.NotContains(t, err.Error(), "pw")
}

func TestDependencyGraphConfigMerge(t *testing.T) {
	load := func(ini string) {
		cfg, err := NewConfigProviderFromData(ini)
		require.NoError(t, err)
		require.NoError(t, loadDependencyGraphFrom(cfg))
	}
	t.Cleanup(func() { load("") })
	defer func(k string) { SecretKey = k }(SecretKey)
	SecretKey = "dependency-graph-test-key"
	enc := func(s string) *string {
		v, err := secret.EncryptSecret(SecretKey, s)
		require.NoError(t, err)
		return &v
	}
	str := func(s string) *string { return &s }

	load("[dependency_graph]\nOSV_MIRROR_URL = https://art.example/osv\nOSV_MIRROR_TOKEN = app-ini-token-1\nOSV_MIRROR_USER = ci\nOSV_MIRROR_ALLOWED_HOSTS = private\nMAX_CONCURRENT_SCANS = 3\n")

	t.Run("nothing stored is app.ini", func(t *testing.T) {
		o := DependencyGraphConfig{}.Merge()
		assert.Equal(t, DependencyGraph, o)
		assert.Equal(t, 3, o.MaxConcurrentScans)
		assert.Equal(t, "app-ini-token-1", o.OSVMirrorToken)
	})

	t.Run("stored values win", func(t *testing.T) {
		o := DependencyGraphConfig{
			Limits: map[string]int64{"max_concurrent_scans": 7, "update_interval": 2 * 3600, "scan_timeout": 90, "osv_max_download": 1 << 20},
			Source: OSVSourceMirror, MirrorURL: "https://other.example/osv", SignIn: OSVSignInToken,
			TokenEnc: enc("stored-token-1"), TokenOrigin: "https://other.example", Hosts: str("10.1.0.0/16"),
		}.Merge()
		assert.Equal(t, 7, o.MaxConcurrentScans)
		assert.Equal(t, 2*time.Hour, o.UpdateInterval)
		assert.Equal(t, 90*time.Second, o.ScanTimeout)
		assert.EqualValues(t, 1<<20, o.OSVMaxDownload)
		assert.Equal(t, "https://other.example/osv", o.OSVMirrorURL)
		assert.Equal(t, "stored-token-1", o.OSVMirrorToken)
		assert.Empty(t, o.OSVMirrorUser, "Bearer")
		assert.Equal(t, "10.1.0.0/16", o.OSVMirrorHosts)
		assert.True(t, o.OSVMirrorHostsFromPage, "a page list: the dial check blocks the internal addresses")
		assert.False(t, o.MirrorConfigInvalid)
		assert.Equal(t, 500, o.MaxManifests, "not stored: app.ini")
	})

	t.Run("an invalid stored limit falls back to app.ini", func(t *testing.T) {
		o := DependencyGraphConfig{Limits: map[string]int64{"max_concurrent_scans": 17, "update_interval": 60, "unknown": 1}}.Merge()
		assert.Equal(t, 3, o.MaxConcurrentScans)
		assert.Equal(t, 24*time.Hour, o.UpdateInterval)
		assert.False(t, o.MirrorConfigInvalid)
	})

	t.Run("an unusable stored mirror URL turns the mirror off", func(t *testing.T) {
		o := DependencyGraphConfig{Source: OSVSourceMirror, MirrorURL: "ftp://x.example/", SignIn: OSVSignInBasic, User: "ci"}.Merge()
		assert.True(t, o.MirrorConfigInvalid, "never another source than the one chosen")
	})

	t.Run("an empty stored host list is app.ini's", func(t *testing.T) {
		o := DependencyGraphConfig{Hosts: str(" ")}.Merge()
		assert.Equal(t, "private", o.OSVMirrorHosts)
		assert.False(t, o.OSVMirrorHostsFromPage)
		assert.False(t, o.MirrorConfigInvalid)
	})

	t.Run("an invalid host list turns the mirror off", func(t *testing.T) {
		for _, bad := range []string{"bad host", "loopback", "*", "10.0.0.0/8"} {
			o := DependencyGraphConfig{Source: OSVSourcePublic, Hosts: str(bad)}.Merge()
			assert.True(t, o.MirrorConfigInvalid, bad)
			assert.Equal(t, "private", o.OSVMirrorHosts, "never the stored list")
		}
		assert.True(t, DependencyGraphConfig{Invalid: true}.Merge().MirrorConfigInvalid, "a value that can't be parsed")
		assert.True(t, DependencyGraphConfig{TokenEnc: str("not-hex")}.Merge().MirrorConfigInvalid, "a token that can't be decrypted")
		assert.True(t, DependencyGraphConfig{SignIn: "odd"}.Merge().MirrorConfigInvalid)
	})

	t.Run("sources", func(t *testing.T) {
		o := DependencyGraphConfig{Source: OSVSourcePublic}.Merge()
		assert.Equal(t, DefaultOSVMirrorURL, o.OSVMirrorURL)
		assert.Empty(t, o.OSVMirrorToken, "no credential to the public bucket")
		o = DependencyGraphConfig{Source: OSVSourceNone}.Merge()
		assert.Empty(t, o.OSVMirrorURL)
		assert.Empty(t, o.OSVMirrorToken)
	})

	t.Run("a token goes only to its own origin", func(t *testing.T) {
		// the app.ini token is for art.example: a stored URL elsewhere doesn't get it
		o := DependencyGraphConfig{Source: OSVSourceMirror, MirrorURL: "https://evil.example/osv", SignIn: OSVSignInToken}.Merge()
		assert.Empty(t, o.OSVMirrorToken)
		// a stored token entered for another host
		o = DependencyGraphConfig{Source: OSVSourceMirror, MirrorURL: "http://art.example/osv", SignIn: OSVSignInBasic, User: "ci", TokenEnc: enc("stored-token-1"), TokenOrigin: "https://art.example"}.Merge()
		assert.Empty(t, o.OSVMirrorToken, "another scheme")
		assert.Empty(t, o.OSVMirrorUser)
		o = DependencyGraphConfig{Source: OSVSourceMirror, MirrorURL: "https://art.example/other", SignIn: OSVSignInBasic, User: "bot", TokenEnc: enc("stored-token-1"), TokenOrigin: "https://art.example"}.Merge()
		assert.Equal(t, "stored-token-1", o.OSVMirrorToken)
		assert.Equal(t, "bot", o.OSVMirrorUser)
		o = DependencyGraphConfig{Source: OSVSourceMirror, MirrorURL: "https://art.example/osv", SignIn: OSVSignInNone}.Merge()
		assert.Empty(t, o.OSVMirrorToken)
	})
}

func TestDependencyGraphLimits(t *testing.T) {
	for _, l := range DependencyGraphLimits {
		assert.NoError(t, l.Check(l.Min), l.Name)
		assert.NoError(t, l.Check(l.Max), l.Name)
		require.ErrorContains(t, l.Check(l.Max+1), l.Name)
		require.Error(t, l.Check(l.Min-1), l.Name)
		_, ok := dependencyGraphLimitDefaults[l.Name]
		assert.True(t, ok, l.Name)
	}
	l, _ := DependencyGraphLimitByName("max_concurrent_scans")
	assert.EqualValues(t, 16, l.Max)
	l, _ = DependencyGraphLimitByName("max_manifest_size")
	assert.EqualValues(t, 100<<20, l.Max)
	l, _ = DependencyGraphLimitByName("update_interval")
	assert.EqualValues(t, 720*3600, l.Max)
	assert.EqualValues(t, 3600, l.Min)
	for _, name := range []string{"scan_timeout", "osv_ecosystem_timeout"} {
		l, _ = DependencyGraphLimitByName(name)
		assert.EqualValues(t, 24*3600, l.Max, name)
	}

	cfg, err := NewConfigProviderFromData("[dependency_graph]\nMAX_CONCURRENT_SCANS = 500\nSCAN_TIMEOUT = 48h\n")
	require.NoError(t, err)
	require.NoError(t, loadDependencyGraphFrom(cfg))
	t.Cleanup(func() {
		cfg, _ := NewConfigProviderFromData("")
		require.NoError(t, loadDependencyGraphFrom(cfg))
	})
	assert.Equal(t, 16, DependencyGraph.MaxConcurrentScans, "app.ini is held to the same bounds")
	assert.Equal(t, 24*time.Hour, DependencyGraph.ScanTimeout)
}

func TestCheckPageHostList(t *testing.T) {
	for _, ok := range []string{"", "external", "art.example", "art.example, external", "10.1.2.3", "2001:db8::1", "10.1.0.0/16", "192.168.4.0/24", "2001:db8::/48", "127.0.0.0/16"} {
		assert.NoError(t, CheckPageHostList(ok), ok)
	}
	for _, bad := range []string{"*", "*.example", "art?.example", "loopback", "private", "0.0.0.0/0", "10.0.0.0/8", "2001:db8::/32", "bad host", "-x.example", "10.0.0.0/33", "127.0.0.1", "::1", "0.0.0.0", "169.254.169.254", "fe80::1", "localhost", "a.localhost."} {
		assert.Error(t, CheckPageHostList(bad), bad)
	}
}

func TestInstanceAndLoopbackHosts(t *testing.T) {
	defer func(app, local, domain, ssh, addr string) {
		AppURL, LocalURL, Domain, SSH.Domain, HTTPAddr = app, local, domain, ssh, addr
	}(AppURL, LocalURL, Domain, SSH.Domain, HTTPAddr)
	AppURL, LocalURL, Domain, SSH.Domain, HTTPAddr = "https://git.example/", "http://10.0.0.5:3000/", "git.example", "ssh.git.example", "10.0.0.5"
	for _, h := range []string{"git.example", "GIT.example.", "ssh.git.example", "10.0.0.5", "[10.0.0.5]"} {
		assert.True(t, IsInstanceHost(h), h)
	}
	for _, h := range []string{"", "art.example", "example"} {
		assert.False(t, IsInstanceHost(h), h)
	}
	for _, h := range []string{"localhost", "LOCALHOST.", "a.localhost", "127.0.0.1", "127.9.9.9", "[::1]", "0.0.0.0", "::"} {
		assert.True(t, IsLoopbackHost(h), h)
	}
	for _, h := range []string{"art.example", "10.0.0.1", "localhost.example"} {
		assert.False(t, IsLoopbackHost(h), h)
	}
	assert.Contains(t, MirrorBlockedHosts(), "169.254.0.0/16")
	assert.Contains(t, MirrorBlockedHosts(), "10.0.0.5", "the address this instance listens on")
}
