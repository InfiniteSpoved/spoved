// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package setting

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"forgejo.org/modules/log"
)

// DefaultOSVMirrorURL is the public OSV bucket.
const DefaultOSVMirrorURL = "https://osv-vulnerabilities.storage.googleapis.com"

// DependencyGraph holds the dependency graph settings, [dependency_graph]: the manifests of the default branch are
// read into a list of dependencies per repository, and an SBOM can be exported from it.
var DependencyGraph = struct {
	Enabled            bool
	MaxManifestSize    int64 // bytes of one lock file; the small formats (go.mod, requirements.txt, ...) stop at 1 MiB
	MaxManifests       int   // manifests read per repository
	MaxDependencies    int   // dependencies stored per repository
	MaxTreeEntries     int   // entries of the tree a scan walks
	ScanTimeout        time.Duration
	MaxConcurrentScans int // scans running at once in this process

	// The OSV mirror (services/vulnerability). An empty OSVMirrorURL means no download (offline).
	OSVMirrorURL        string
	OSVMirrorToken      string // sent to the mirror's own scheme and host only; never shown, registered with log.RegisterSecret
	OSVMirrorUser       string // with it, the token is the password of Basic auth instead of a Bearer token
	OSVMirrorHosts      string // hostmatcher list that replaces [webhook] ALLOWED_HOST_LIST for the mirror, when set
	UpdateInterval      time.Duration
	OSVMaxDownload      int64 // bytes of one ecosystem's all.zip
	OSVMaxEntries       int   // entries of one zip
	OSVMaxEntrySize     int64 // uncompressed bytes of one entry
	OSVMaxUncompressed  int64 // uncompressed bytes of one zip
	OSVEcosystemTimeout time.Duration
}{}

func loadDependencyGraphFrom(rootCfg ConfigProvider) (err error) {
	sec := rootCfg.Section("dependency_graph")
	DependencyGraph.Enabled = sec.Key("ENABLED").MustBool(false)
	DependencyGraph.MaxManifestSize = max(sec.Key("MAX_MANIFEST_SIZE").MustInt64(20<<20), 1)
	DependencyGraph.MaxManifests = max(sec.Key("MAX_MANIFESTS").MustInt(500), 1)
	DependencyGraph.MaxDependencies = max(sec.Key("MAX_DEPENDENCIES").MustInt(50000), 1)
	DependencyGraph.MaxTreeEntries = max(sec.Key("MAX_TREE_ENTRIES").MustInt(200000), 1)
	DependencyGraph.MaxConcurrentScans = max(sec.Key("MAX_CONCURRENT_SCANS").MustInt(1), 1)
	if DependencyGraph.ScanTimeout, err = sec.Key("SCAN_TIMEOUT").MustDuration(60 * time.Second); err != nil {
		return err
	}
	DependencyGraph.ScanTimeout = max(DependencyGraph.ScanTimeout, time.Second)
	return loadOSVMirrorFrom(sec)
}

// MinUpdateInterval and MaxUpdateInterval bound [dependency_graph] UPDATE_INTERVAL.
const (
	MinUpdateInterval = time.Hour
	MaxUpdateInterval = 30 * 24 * time.Hour
)

func loadOSVMirrorFrom(sec ConfigSection) (err error) {
	g := &DependencyGraph
	g.OSVMirrorURL = DefaultOSVMirrorURL
	if sec.HasKey("OSV_MIRROR_URL") {
		g.OSVMirrorURL = strings.TrimSpace(sec.Key("OSV_MIRROR_URL").String())
	}
	if g.OSVMirrorURL != "" {
		u, err := url.Parse(g.OSVMirrorURL)
		switch {
		case err != nil:
			return errors.New("[dependency_graph] OSV_MIRROR_URL is not a valid URL")
		case u.Scheme != "https" && u.Scheme != "http", u.Hostname() == "":
			return errors.New("[dependency_graph] OSV_MIRROR_URL must be an http(s) URL with a host")
		case u.User != nil, u.RawQuery != "", u.Fragment != "", u.ForceQuery:
			return errors.New("[dependency_graph] OSV_MIRROR_URL must have no user info, query or fragment (put a credential in OSV_MIRROR_TOKEN or OSV_MIRROR_TOKEN_URI)")
		}
		if u.Scheme == "http" {
			log.Warn("[dependency_graph] OSV_MIRROR_URL uses http: the advisory data is not protected in transit")
		}
		g.OSVMirrorURL = strings.TrimRight(u.Scheme+"://"+u.Host+u.EscapedPath(), "/")
	}
	g.OSVMirrorUser = strings.TrimSpace(sec.Key("OSV_MIRROR_USER").String())
	g.OSVMirrorHosts = strings.TrimSpace(sec.Key("OSV_MIRROR_ALLOWED_HOSTS").String())
	if err := checkHostList(g.OSVMirrorHosts); err != nil {
		return fmt.Errorf("[dependency_graph] OSV_MIRROR_ALLOWED_HOSTS: %w", err)
	}
	if g.OSVMirrorToken, err = loadOSVMirrorToken(sec); err != nil {
		return err
	}
	if g.OSVMirrorUser != "" && g.OSVMirrorToken == "" {
		return errors.New("[dependency_graph] OSV_MIRROR_USER is set without OSV_MIRROR_TOKEN or OSV_MIRROR_TOKEN_URI")
	}
	if g.OSVMirrorToken != "" {
		log.RegisterSecret(g.OSVMirrorToken)
		if strings.HasPrefix(g.OSVMirrorURL, "http://") {
			log.Warn("[dependency_graph] OSV_MIRROR_TOKEN is sent over http: it is not protected in transit")
		}
	}
	if g.UpdateInterval, err = sec.Key("UPDATE_INTERVAL").MustDuration(24 * time.Hour); err != nil {
		return err
	}
	if g.UpdateInterval < MinUpdateInterval {
		log.Warn("[dependency_graph] UPDATE_INTERVAL %s is below the minimum, using %s", g.UpdateInterval, MinUpdateInterval)
		g.UpdateInterval = MinUpdateInterval
	}
	if g.UpdateInterval > MaxUpdateInterval {
		log.Warn("[dependency_graph] UPDATE_INTERVAL %s is above the maximum, using %s", g.UpdateInterval, MaxUpdateInterval)
		g.UpdateInterval = MaxUpdateInterval
	}
	g.OSVMaxDownload = max(sec.Key("OSV_MAX_DOWNLOAD").MustInt64(512<<20), 1)
	g.OSVMaxEntries = max(sec.Key("OSV_MAX_ENTRIES").MustInt(500000), 1)
	g.OSVMaxEntrySize = max(sec.Key("OSV_MAX_ENTRY_SIZE").MustInt64(4<<20), 1)
	g.OSVMaxUncompressed = max(sec.Key("OSV_MAX_UNCOMPRESSED").MustInt64(4<<30), 1)
	if g.OSVEcosystemTimeout, err = sec.Key("OSV_ECOSYSTEM_TIMEOUT").MustDuration(15 * time.Minute); err != nil {
		return err
	}
	g.OSVEcosystemTimeout = max(g.OSVEcosystemTimeout, time.Second)
	return nil
}

// DependencyGraphState is stored as JSON in the "dependency_graph.enabled" system setting. An empty Mode means the
// app.ini default ([dependency_graph] ENABLED); once the switch is saved in Site administration, the stored value wins.
type DependencyGraphState struct {
	Mode        string `json:"mode"` // "on", "off" or "" (app.ini)
	UpdatedBy   int64  `json:"updated_by"`
	UpdatedUnix int64  `json:"updated_unix"`
}

// Enabled is the site switch in force. A stored value that can't be parsed or isn't known reads as off.
func (s DependencyGraphState) Enabled() bool {
	switch s.Mode {
	case "":
		return DependencyGraph.Enabled
	case "on":
		return true
	}
	return false
}

// loadOSVMirrorToken reads OSV_MIRROR_TOKEN or OSV_MIRROR_TOKEN_URI (a file: URI). It does not use loadSecret, which
// exits the process and whose errors could echo a token pasted into the URI by mistake.
func loadOSVMirrorToken(sec ConfigSection) (string, error) {
	token, uri := sec.Key("OSV_MIRROR_TOKEN").String(), sec.Key("OSV_MIRROR_TOKEN_URI").String()
	switch {
	case token != "" && uri != "":
		return "", errors.New("[dependency_graph] OSV_MIRROR_TOKEN and OSV_MIRROR_TOKEN_URI are both set, use only one")
	case uri == "":
		return checkOSVMirrorToken(strings.TrimSpace(token))
	case !strings.HasPrefix(uri, "file:"):
		return "", errors.New("[dependency_graph] OSV_MIRROR_TOKEN_URI must be a file: URI")
	}
	token, err := loadSecretFromURI(uri)
	if err != nil {
		return "", fmt.Errorf("[dependency_graph] OSV_MIRROR_TOKEN_URI: %w", err)
	}
	return checkOSVMirrorToken(token)
}

// checkOSVMirrorToken accepts 8 to 1024 characters (every registered secret is redacted from the logs wherever it
// occurs, so a short one would blank out ordinary text). The message never holds the token.
func checkOSVMirrorToken(token string) (string, error) {
	if token != "" && (len(token) < 8 || len(token) > 1024) {
		return "", errors.New("[dependency_graph] the OSV mirror token must be 8 to 1024 characters")
	}
	return token, nil
}

// checkHostList refuses an entry that is no builtin (external, private, loopback), CIDR, address or host name pattern.
func checkHostList(list string) error {
	for entry := range strings.SplitSeq(list, ",") {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			if _, _, err := net.ParseCIDR(entry); err != nil {
				return fmt.Errorf("%q is not a CIDR network", entry)
			}
			continue
		}
		for _, r := range entry {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("*?.-_:[]", r)) {
				return fmt.Errorf("%q is not a host name, address, CIDR network or one of external, private, loopback", entry)
			}
		}
	}
	return nil
}
