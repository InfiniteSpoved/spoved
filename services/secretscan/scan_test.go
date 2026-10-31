// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretscan

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	secretscan_model "forgejo.org/models/secretscan"
	system_model "forgejo.org/models/system"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/json"
	"forgejo.org/modules/log"
	"forgejo.org/modules/queue"
	"forgejo.org/modules/secretdetection"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/setting/config"
	"forgejo.org/modules/test"
	"forgejo.org/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// token is a GitHub-shaped token for a seed, built from parts so that no source line holds one.
func token(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "ghp" + "_" + hex.EncodeToString(sum[:])[:36]
}

// enable turns secret scanning on with generous budgets and returns user2/repo1, a repository with a history.
func enable(t *testing.T) *repo_model.Repository {
	t.Helper()
	require.NoError(t, unittest.PrepareTestDatabase())
	bumpRevision(t) // a site switch an earlier test stored is cached by revision
	t.Cleanup(test.MockVariableValue(&setting.SecretScanning.Enabled, true))
	t.Cleanup(test.MockVariableValue(&setting.SecretScanning.GenericRules, false))
	t.Cleanup(test.MockVariableValue(&setting.SecretScanning.MaxConcurrentScans, 1))
	t.Cleanup(test.MockVariableValue(&setting.SecretScanning.ScanTimeout, time.Minute))
	t.Cleanup(test.MockVariableValue(&setting.SecretScanning.MaxBlobSize, 1<<20))
	t.Cleanup(test.MockVariableValue(&setting.SecretScanning.MaxScanBytes, 1<<30))
	t.Cleanup(test.MockVariableValue(&setting.SecretScanning.MaxScanBlobs, 100000))
	t.Cleanup(test.MockVariableValue(&setting.SecretScanning.MaxAlertsPerRepo, 1000))
	t.Cleanup(test.MockVariableValue(&setting.SecretScanning.MaxLocationsPerAlert, 100))
	return unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
}

// work is a clone of a repository to commit in and push from; every commit is a minute after the one before.
type work struct {
	t     *testing.T
	repo  *repo_model.Repository
	dir   string
	clock time.Time
}

func newWork(t *testing.T, repo *repo_model.Repository) *work {
	t.Helper()
	w := &work{t: t, repo: repo, dir: t.TempDir(), clock: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}
	w.git("clone", "-q", "-b", repo.DefaultBranch, repo.RepoPath(), ".")
	return w
}

func (w *work) git(args ...string) string {
	w.t.Helper()
	return w.gitEnv(nil, args...)
}

func (w *work) gitEnv(env []string, args ...string) string {
	w.t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Dev One", "-c", "user.email=dev@example.com", "-c", "commit.gpgsign=false", "-c", "core.quotepath=off"}, args...)...)
	cmd.Dir = w.dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	require.NoError(w.t, err, string(out))
	return strings.TrimSpace(string(out))
}

// commit writes the files and commits them with the author; an empty content removes the file.
func (w *work) commit(files map[string]string, author ...string) string {
	w.t.Helper()
	for p, c := range files {
		full := filepath.Join(w.dir, p)
		if c == "" {
			require.NoError(w.t, os.RemoveAll(full))
			continue
		}
		require.NoError(w.t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(w.t, os.WriteFile(full, []byte(c), 0o644))
	}
	w.clock = w.clock.Add(time.Minute)
	date := w.clock.Format(time.RFC3339)
	env := []string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}
	if len(author) == 2 {
		env = append(env, "GIT_AUTHOR_NAME="+author[0], "GIT_AUTHOR_EMAIL="+author[1])
	}
	w.git("add", "-A")
	w.gitEnv(env, "commit", "-q", "--allow-empty", "-m", "change")
	return w.git("rev-parse", "HEAD")
}

// push pushes HEAD to the ref of the repository (a bare directory, so no hook runs).
func (w *work) push(ref string) {
	w.t.Helper()
	w.git("push", "-q", "--force", w.repo.RepoPath(), "HEAD:"+ref)
}

func (w *work) pushDefault() { w.push("refs/heads/" + w.repo.DefaultBranch) }

func scanOf(t *testing.T, repoID int64, kind string) *secretscan_model.Scan {
	t.Helper()
	s, err := secretscan_model.GetScan(t.Context(), repoID, kind)
	require.NoError(t, err)
	return s
}

func alertsOf(t *testing.T, repoID int64) []*secretscan_model.Alert {
	t.Helper()
	var alerts []*secretscan_model.Alert
	require.NoError(t, db.GetEngine(t.Context()).Where("repo_id = ?", repoID).OrderBy("`index`").Find(&alerts))
	return alerts
}

func locationsOf(t *testing.T, alertID int64) []*secretscan_model.Location {
	t.Helper()
	var locs []*secretscan_model.Location
	require.NoError(t, db.GetEngine(t.Context()).Where("alert_id = ?", alertID).OrderBy("path, start_line").Find(&locs))
	return locs
}

// alertFor is the alert of a token's secret, which has the fingerprint of the token's secret part.
func alertFor(t *testing.T, repoID int64, tok string) *secretscan_model.Alert {
	t.Helper()
	for _, a := range alertsOf(t, repoID) {
		if a.RuleID == "github-token" && a.Fingerprint == secretdetection.Fingerprint([]byte(tok)) {
			return a
		}
	}
	return nil
}

func TestScanPushedKey(t *testing.T) {
	repo := enable(t)
	w := newWork(t, repo)
	tok := token("pushed")

	// the first scan reads the whole history of the repository
	require.NoError(t, Scan(t.Context(), repo.ID))
	first := scanOf(t, repo.ID, secretscan_model.KindRepo)
	require.NotNil(t, first)
	assert.Equal(t, secretscan_model.StatusOK, first.Status)
	assert.Positive(t, first.Blobs)
	assert.Empty(t, alertsOf(t, repo.ID))

	c1 := w.commit(map[string]string{"deploy/env": "# env\nA=1\nGITHUB_TOKEN=" + tok + "\n"}, "Alice Dev", "alice@example.com")
	w.pushDefault()
	require.NoError(t, Scan(t.Context(), repo.ID))

	// incremental: only the new file is read
	second := scanOf(t, repo.ID, secretscan_model.KindRepo)
	assert.EqualValues(t, 1, second.Blobs)
	assert.Equal(t, int64(len("# env\nA=1\nGITHUB_TOKEN="+tok+"\n")), second.Bytes)
	assert.Greater(t, second.Version, first.Version)
	alert := alertFor(t, repo.ID, tok)
	require.NotNil(t, alert)
	assert.Equal(t, int64(1), alert.Index)
	assert.Equal(t, "github", alert.SecretType)
	assert.Equal(t, secretscan_model.StateOpen, alert.State)
	assert.Equal(t, secretscan_model.CurrentKeyVersion, alert.KeyVersion)
	assert.Len(t, alert.Fingerprint, 64)
	assert.Equal(t, 1, alert.LocationCount)
	locs := locationsOf(t, alert.ID)
	require.Len(t, locs, 1)
	assert.Equal(t, secretscan_model.KindRepo, locs[0].Kind)
	assert.Equal(t, "deploy/env", locs[0].Path)
	assert.Equal(t, 3, locs[0].StartLine)
	assert.Equal(t, c1, locs[0].CommitID)
	assert.Equal(t, "Alice Dev", locs[0].AuthorName)
	assert.Equal(t, "alice@example.com", locs[0].AuthorEmail)
	assert.Equal(t, time.Date(2030, 1, 1, 0, 1, 0, 0, time.UTC).Unix(), locs[0].CommitUnix)
	assert.Len(t, locs[0].BlobID, 40)

	// the same key in another file is another place of the same alert
	w.commit(map[string]string{"scripts/run.sh": "#!/bin/sh\n\n\nexport T=" + tok + "\n"})
	w.pushDefault()
	require.NoError(t, Scan(t.Context(), repo.ID))
	assert.Len(t, alertsOf(t, repo.ID), 1)
	alert = alertFor(t, repo.ID, tok)
	locs = locationsOf(t, alert.ID)
	require.Len(t, locs, 2)
	assert.Equal(t, "deploy/env", locs[0].Path)
	assert.Equal(t, "scripts/run.sh", locs[1].Path)
	assert.Equal(t, 4, locs[1].StartLine)
	assert.Equal(t, 2, alert.LocationCount)

	// the same key at the same line of the same file in a new commit adds nothing
	w.commit(map[string]string{"deploy/env": "# env changed\nA=2\nGITHUB_TOKEN=" + tok + "\n"})
	w.pushDefault()
	require.NoError(t, Scan(t.Context(), repo.ID))
	assert.Len(t, alertsOf(t, repo.ID), 1)
	alert = alertFor(t, repo.ID, tok)
	assert.Len(t, locationsOf(t, alert.ID), 2)
	assert.Equal(t, 2, alert.LocationCount)
	assert.EqualValues(t, 1, scanOf(t, repo.ID, secretscan_model.KindRepo).Blobs, "only the changed file was read")

	// a scan with nothing new writes nothing
	before := scanOf(t, repo.ID, secretscan_model.KindRepo)
	require.NoError(t, Scan(t.Context(), repo.ID))
	assert.Equal(t, before.Version, scanOf(t, repo.ID, secretscan_model.KindRepo).Version)
}

func TestScanFullHistory(t *testing.T) {
	repo := enable(t)
	w := newWork(t, repo)
	old, inTag, inPull, inBranch := token("old"), token("tag"), token("pull"), token("branch")

	// added in an old commit and removed in the next one
	c1 := w.commit(map[string]string{"old.txt": "key " + old + "\n"})
	w.commit(map[string]string{"old.txt": ""})
	w.pushDefault()
	// a file that only a tag reaches, and one that only a pull request ref reaches
	w.git("checkout", "-q", "-b", "side")
	c3 := w.commit(map[string]string{"in-pull.txt": "x " + inPull + "\n"})
	w.push("refs/pull/9/head")
	w.git("checkout", "-q", "-b", "other", repo.DefaultBranch)
	w.commit(map[string]string{"in-branch.txt": "x " + inBranch + "\n"})
	w.push("refs/heads/other")
	cmd := exec.Command("git", "hash-object", "-w", "--stdin")
	cmd.Dir = repo.RepoPath()
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	cmd.Stdin = strings.NewReader("tag " + inTag + "\n")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	tagBlob := strings.TrimSpace(string(out))
	tagCmd := exec.Command("git", "update-ref", "refs/tags/just-a-file", tagBlob)
	tagCmd.Dir = repo.RepoPath()
	tagCmd.Env = cmd.Env
	out, err = tagCmd.CombinedOutput()
	require.NoError(t, err, string(out))

	require.NoError(t, Scan(t.Context(), repo.ID))
	require.Equal(t, secretscan_model.StatusOK, scanOf(t, repo.ID, secretscan_model.KindRepo).Status)

	a := alertFor(t, repo.ID, old)
	require.NotNil(t, a, "a key removed in the next commit is still in the history")
	locs := locationsOf(t, a.ID)
	require.Len(t, locs, 1)
	assert.Equal(t, "old.txt", locs[0].Path)
	assert.Equal(t, c1, locs[0].CommitID)

	a = alertFor(t, repo.ID, inPull)
	require.NotNil(t, a, "a key only in refs/pull/N/head")
	locs = locationsOf(t, a.ID)
	require.Len(t, locs, 1)
	assert.Equal(t, c3, locs[0].CommitID)
	assert.Equal(t, "in-pull.txt", locs[0].Path)

	require.NotNil(t, alertFor(t, repo.ID, inBranch))

	a = alertFor(t, repo.ID, inTag)
	require.NotNil(t, a, "a blob only a tag reaches")
	locs = locationsOf(t, a.ID)
	require.Len(t, locs, 1)
	assert.Empty(t, locs[0].CommitID, "no commit brings it")
	assert.Equal(t, tagBlob, locs[0].BlobID)
	assert.Equal(t, 1, locs[0].StartLine)
}

func TestScanPathWithNewline(t *testing.T) {
	repo := enable(t)
	w := newWork(t, repo)
	other, hostile := token("other"), token("hostile")

	w.commit(map[string]string{"other.txt": other + "\n"})
	w.pushDefault()
	require.NoError(t, Scan(t.Context(), repo.ID))
	otherBlob := w.git("rev-parse", "HEAD:other.txt")
	a := alertFor(t, repo.ID, other)
	require.NotNil(t, a)
	base := len(alertsOf(t, repo.ID)) // the fixture's history has findings of its own

	// a file named so that a line of rev-list output could pass for the line of the blob above
	name := "a\n" + otherBlob + " b.txt"
	w.commit(map[string]string{name: hostile + "\n"})
	w.pushDefault()
	require.NoError(t, Scan(t.Context(), repo.ID))

	assert.Len(t, alertsOf(t, repo.ID), base+1)
	a2 := alertFor(t, repo.ID, other)
	assert.Equal(t, a.ID, a2.ID)
	assert.Len(t, locationsOf(t, a2.ID), 1, "the other blob gained no place")
	assert.EqualValues(t, 1, scanOf(t, repo.ID, secretscan_model.KindRepo).Blobs, "and was not read again")
	h := alertFor(t, repo.ID, hostile)
	require.NotNil(t, h)
	locs := locationsOf(t, h.ID)
	require.Len(t, locs, 1)
	assert.Equal(t, "a?"+otherBlob+" b.txt", locs[0].Path, "the whole path, the newline stripped for display")
	assert.Equal(t, secretscan_model.PathHash(name), locs[0].PathHash)
}

func TestScanAuthorTextIsNoRecord(t *testing.T) {
	repo := enable(t)
	w := newWork(t, repo)
	tok := token("author")
	zero := strings.Repeat("0", 40)

	// an author name that looks like the raw line of a commit
	w.commit(map[string]string{"real.txt": tok + "\n"}, ":100644 100644 "+zero+" "+strings.Repeat("a", 40)+" A", "x@example.com")
	w.pushDefault()
	require.NoError(t, Scan(t.Context(), repo.ID))
	a := alertFor(t, repo.ID, tok)
	require.NotNil(t, a)
	locs := locationsOf(t, a.ID)
	require.Len(t, locs, 1)
	assert.Equal(t, "real.txt", locs[0].Path)
	assert.Equal(t, "100644 100644 "+zero+" "+strings.Repeat("a", 40)+" A", locs[0].AuthorName, "git itself drops the leading colon of a name")
}

func TestScanBudgets(t *testing.T) {
	repo := enable(t)
	require.NoError(t, Scan(t.Context(), repo.ID)) // the history the fixture has
	w := newWork(t, repo)

	t.Run("a file over MAX_BLOB_SIZE is skipped without being read", func(t *testing.T) {
		defer test.MockVariableValue(&setting.SecretScanning.MaxBlobSize, 100)()
		big := token("big") + "\n" + strings.Repeat("x", 200)
		w.commit(map[string]string{"big.txt": big, "small.txt": token("small") + "\n"})
		w.pushDefault()
		require.NoError(t, Scan(t.Context(), repo.ID))
		s := scanOf(t, repo.ID, secretscan_model.KindRepo)
		assert.Equal(t, secretscan_model.StatusOK, s.Status)
		assert.EqualValues(t, 1, s.Blobs)
		assert.EqualValues(t, 1, s.SkippedLarge)
		assert.EqualValues(t, len(token("small"))+1, s.Bytes, "the big file's bytes were not read")
		assert.NotNil(t, alertFor(t, repo.ID, token("small")))
		assert.Nil(t, alertFor(t, repo.ID, token("big")))
	})

	t.Run("MAX_SCAN_BLOBS ends the scan as incomplete, and the cron leaves it", func(t *testing.T) {
		defer test.MockVariableValue(&setting.SecretScanning.MaxScanBlobs, 1)()
		w.commit(map[string]string{"b1.txt": token("b1") + "\n", "b2.txt": token("b2") + "\n"})
		w.pushDefault()
		before := len(alertsOf(t, repo.ID))
		require.NoError(t, Scan(t.Context(), repo.ID))
		s := scanOf(t, repo.ID, secretscan_model.KindRepo)
		assert.Equal(t, secretscan_model.StatusIncomplete, s.Status)
		assert.Equal(t, stopBlobs, s.Error)
		assert.EqualValues(t, 1, s.Blobs)
		assert.Len(t, alertsOf(t, repo.ID), before+1, "what was read is kept")
		var tips []string
		require.NoError(t, json.Unmarshal([]byte(s.ScannedTips), &tips))
		assert.Contains(t, tips, w.git("rev-parse", "HEAD"), "the tips read at the start are stored")

		ids, err := secretscan_model.UnscannedRepoIDs(t.Context(), timeutil.TimeStampNow().Add(-86400), nil, 100)
		require.NoError(t, err)
		assert.NotContains(t, ids, repo.ID, "an incomplete scan is not retried")
	})

	t.Run("MAX_SCAN_BYTES ends the scan as incomplete", func(t *testing.T) {
		defer test.MockVariableValue(&setting.SecretScanning.MaxScanBytes, int64(len(token("c1"))+10))()
		w.commit(map[string]string{"c1.txt": token("c1") + "\n", "c2.txt": token("c2") + "\n"})
		w.pushDefault()
		require.NoError(t, Scan(t.Context(), repo.ID))
		s := scanOf(t, repo.ID, secretscan_model.KindRepo)
		assert.Equal(t, secretscan_model.StatusIncomplete, s.Status)
		assert.Equal(t, stopBytes, s.Error)
		assert.EqualValues(t, 1, s.Blobs)
		assert.LessOrEqual(t, s.Bytes, setting.SecretScanning.MaxScanBytes)
	})

	t.Run("MAX_ALERTS_PER_REPO and MAX_LOCATIONS_PER_ALERT", func(t *testing.T) {
		alerts := len(alertsOf(t, repo.ID))
		defer test.MockVariableValue(&setting.SecretScanning.MaxAlertsPerRepo, int64(alerts+1))()
		defer test.MockVariableValue(&setting.SecretScanning.MaxLocationsPerAlert, 2)()
		same := token("same")
		w.commit(map[string]string{"s1.txt": same + "\n", "s2.txt": same + "\n", "s3.txt": same + "\n", "s4.txt": same + "\n", "n1.txt": token("n1") + "\n", "n2.txt": token("n2") + "\n"})
		w.pushDefault()
		require.NoError(t, Scan(t.Context(), repo.ID))
		s := scanOf(t, repo.ID, secretscan_model.KindRepo)
		assert.Equal(t, secretscan_model.StatusIncomplete, s.Status)
		assert.Equal(t, stopAlerts, s.Error)
		assert.Len(t, alertsOf(t, repo.ID), alerts+1)
		if a := alertFor(t, repo.ID, same); a != nil {
			assert.Len(t, locationsOf(t, a.ID), 2)
			assert.Equal(t, 4, a.LocationCount, "the places over the cap are counted")
		}
	})
}

func TestFingerprintUninitialisedKeyingFailsScan(t *testing.T) {
	repo := enable(t)
	w := newWork(t, repo)
	require.NoError(t, Scan(t.Context(), repo.ID))
	good := scanOf(t, repo.ID, secretscan_model.KindRepo)
	before := len(alertsOf(t, repo.ID))

	// the real Fingerprint panics with this value before the keying is set up
	defer test.MockVariableValue(&scanContent, func(*secretdetection.Detector, []byte) ([]secretdetection.Finding, bool) {
		panic("keying: not initialized")
	})()
	w.commit(map[string]string{"k.txt": token("k") + "\n"})
	w.pushDefault()
	require.NoError(t, Scan(t.Context(), repo.ID), "the panic is recovered")

	s := scanOf(t, repo.ID, secretscan_model.KindRepo)
	assert.Equal(t, secretscan_model.StatusFailed, s.Status)
	assert.Equal(t, errPanic, s.Error)
	assert.Equal(t, good.ScannedTips, s.ScannedTips, "the earlier tips stay, the file is scanned again")
	assert.Len(t, alertsOf(t, repo.ID), before)
}

func TestScanPartlyScannedBlobIsIncomplete(t *testing.T) {
	repo := enable(t)
	require.NoError(t, Scan(t.Context(), repo.ID))
	w := newWork(t, repo)
	w.commit(map[string]string{"p.txt": token("partial") + "\n"})
	w.pushDefault()
	defer test.MockVariableValue(&scanContent, func(d *secretdetection.Detector, c []byte) ([]secretdetection.Finding, bool) {
		r, _ := d.Scan(c)
		return r, false
	})()
	require.NoError(t, Scan(t.Context(), repo.ID))
	s := scanOf(t, repo.ID, secretscan_model.KindRepo)
	assert.Equal(t, secretscan_model.StatusIncomplete, s.Status)
	assert.Equal(t, stopPartial, s.Error)
	assert.NotNil(t, alertFor(t, repo.ID, token("partial")), "what was found is kept")
}

func TestScanWiki(t *testing.T) {
	repo := enable(t)
	tok := token("wiki")
	require.True(t, repo.HasWiki(), "the fixture has a wiki")
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-c", "user.name=w", "-c", "user.email=w@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("clone", "-q", repo.WikiPath(), ".")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Secrets.md"), []byte("# Page\n\ntoken "+tok+"\n"), 0o644))
	run("add", "-A")
	run("commit", "-q", "-m", "page")
	run("push", "-q", "origin", "HEAD")

	require.NoError(t, Scan(t.Context(), repo.ID))
	require.NotNil(t, scanOf(t, repo.ID, secretscan_model.KindWiki))
	a := alertFor(t, repo.ID, tok)
	require.NotNil(t, a)
	locs := locationsOf(t, a.ID)
	require.Len(t, locs, 1)
	assert.Equal(t, secretscan_model.KindWiki, locs[0].Kind)
	assert.Equal(t, "Secrets.md", locs[0].Path)
	assert.Equal(t, 3, locs[0].StartLine)
	assert.Equal(t, secretscan_model.StatusOK, scanOf(t, repo.ID, secretscan_model.KindRepo).Status)
}

// holdScan makes the next scan wait after it read the tips; hold returns when it is waiting, and release lets it go on.
func holdScan(t *testing.T) (hold, release func()) {
	t.Helper()
	started, gate := make(chan struct{}), make(chan struct{})
	done := false
	t.Cleanup(test.MockVariableValue(&afterTips, func(_ int64, kind string) {
		if kind == secretscan_model.KindRepo && !done {
			done = true
			close(started)
			<-gate
		}
	}))
	once := false
	release = func() {
		if !once {
			once = true
			close(gate)
		}
	}
	t.Cleanup(release)
	return func() {
		select {
		case <-started:
		case <-time.After(30 * time.Second):
			require.Fail(t, "the scan never reached the hold")
		}
	}, release
}

func scanInBackground(t *testing.T, repoID int64) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- Scan(t.Context(), repoID) }()
	return done
}

func TestScanPushDuringScan(t *testing.T) {
	repo := enable(t)
	w := newWork(t, repo)
	first, second := token("first"), token("second")
	w.commit(map[string]string{"1.txt": first + "\n"})
	w.pushDefault()
	startTips, err := currentTips(t.Context(), repo.RepoPath())
	require.NoError(t, err)

	hold, release := holdScan(t)
	done := scanInBackground(t, repo.ID)
	hold()
	w.commit(map[string]string{"2.txt": second + "\n"})
	w.pushDefault() // after the scan read the tips
	release()
	require.NoError(t, <-done)

	s := scanOf(t, repo.ID, secretscan_model.KindRepo)
	var tips []string
	require.NoError(t, json.Unmarshal([]byte(s.ScannedTips), &tips))
	assert.Equal(t, startTips, tips, "the state is the tips the scan read, not the push")
	assert.NotNil(t, alertFor(t, repo.ID, first))
	assert.Nil(t, alertFor(t, repo.ID, second))

	// the scan the push queued reads what is new
	require.NoError(t, Scan(t.Context(), repo.ID))
	assert.NotNil(t, alertFor(t, repo.ID, second))
	assert.EqualValues(t, 1, scanOf(t, repo.ID, secretscan_model.KindRepo).Blobs)
	require.NoError(t, json.Unmarshal([]byte(scanOf(t, repo.ID, secretscan_model.KindRepo).ScannedTips), &tips))
	endTips, err := currentTips(t.Context(), repo.RepoPath())
	require.NoError(t, err)
	assert.Equal(t, endTips, tips)
}

func TestScanSwitchOffDuringScan(t *testing.T) {
	repo := enable(t)
	w := newWork(t, repo)
	w.commit(map[string]string{"1.txt": token("off") + "\n"})
	w.pushDefault()

	hold, release := holdScan(t)
	done := scanInBackground(t, repo.ID)
	hold()
	storeSiteSwitch(t, "off")
	release()
	require.NoError(t, <-done)

	assert.Nil(t, scanOf(t, repo.ID, secretscan_model.KindRepo), "nothing was written")
	assert.Empty(t, alertsOf(t, repo.ID))
}

func TestScanTrashDuringScan(t *testing.T) {
	repo := enable(t)
	w := newWork(t, repo)
	w.commit(map[string]string{"1.txt": token("trash") + "\n"})
	w.pushDefault()

	hold, release := holdScan(t)
	done := scanInBackground(t, repo.ID)
	hold()
	_, err := db.Exec(t.Context(), "UPDATE repository SET trashed_unix = 1 WHERE id = ?", repo.ID)
	require.NoError(t, err)
	release()
	require.NoError(t, <-done)

	assert.Nil(t, scanOf(t, repo.ID, secretscan_model.KindRepo))
	assert.Empty(t, alertsOf(t, repo.ID))
}

func TestScanTransferDuringScan(t *testing.T) {
	repo := enable(t)
	q := mockQueue(t)
	w := newWork(t, repo)
	w.commit(map[string]string{"1.txt": token("transfer") + "\n"})
	w.pushDefault()

	hold, release := holdScan(t)
	done := scanInBackground(t, repo.ID)
	hold()
	_, err := db.Exec(t.Context(), "UPDATE repository SET owner_id = 3 WHERE id = ?", repo.ID)
	require.NoError(t, err)
	release()
	require.NoError(t, <-done)

	assert.Nil(t, scanOf(t, repo.ID, secretscan_model.KindRepo), "the scan read the rules and the switch of the old owner: discarded")
	assert.Empty(t, alertsOf(t, repo.ID))
	has, err := q.Has(repo.ID)
	require.NoError(t, err)
	assert.True(t, has, "and queued again")
}

func TestScanLostWriteRequeues(t *testing.T) {
	repo := enable(t)
	q := mockQueue(t)
	w := newWork(t, repo)
	w.commit(map[string]string{"1.txt": token("lost") + "\n"})
	w.pushDefault()

	// another process stores a state row while this scan reads
	hold, release := holdScan(t)
	done := scanInBackground(t, repo.ID)
	hold()
	require.NoError(t, secretscan_model.Save(t.Context(), nil, &secretscan_model.Scan{RepoID: repo.ID, Kind: secretscan_model.KindRepo, Status: secretscan_model.StatusOK, ScannedTips: "[]"}))
	release()
	require.NoError(t, <-done)

	st := scanOf(t, repo.ID, secretscan_model.KindRepo)
	assert.Equal(t, "[]", st.ScannedTips, "the other process's row stays")
	var repoLocations int64
	repoLocations, err := db.GetEngine(t.Context()).Where("repo_id = ? AND kind = ?", repo.ID, secretscan_model.KindRepo).Count(new(secretscan_model.Location))
	require.NoError(t, err)
	assert.Zero(t, repoLocations, "the write was rolled back")
	has, err := q.Has(repo.ID)
	require.NoError(t, err)
	assert.True(t, has)
}

// mockQueue replaces the scan queue with one that holds the items.
func mockQueue(t *testing.T) *queue.WorkerPoolQueue[int64] {
	t.Helper()
	cfg, err := setting.NewConfigProviderFromData("[queue.secret_scanning]\nTYPE = channel\n")
	require.NoError(t, err)
	t.Cleanup(test.MockVariableValue(&setting.CfgProvider, cfg))
	q := queue.CreateUniqueQueue(t.Context(), "secret_scanning", func(items ...int64) []int64 { return nil })
	require.NotNil(t, q)
	t.Cleanup(test.MockVariableValue(&scanQueue, q))
	return q
}

var lastRevision int

// storeSiteSwitch stores the site switch as the admin page will and moves the settings revision past the cached
// one: fixtures reset it, and a value is cached by revision.
func storeSiteSwitch(t *testing.T, mode string) {
	t.Helper()
	require.NoError(t, system_model.SetSettings(db.DefaultContext, map[string]string{setting.Config().SecretScanning.DynKey(): `{"mode":"` + mode + `"}`}))
	bumpRevision(t)
}

func bumpRevision(t *testing.T) {
	t.Helper()
	lastRevision = max(lastRevision, system_model.GetRevision(db.DefaultContext)) + 1
	_, err := db.Exec(db.DefaultContext, "UPDATE system_setting SET version=? WHERE setting_key='revision'", lastRevision)
	require.NoError(t, err)
	config.GetDynGetter().InvalidateCache()
}

func TestSwitches(t *testing.T) {
	repo := enable(t)
	org := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 3}) // owned by org3
	setOrg := func(on, locked bool) {
		t.Helper()
		_, err := db.Exec(t.Context(), "DELETE FROM user_setting WHERE user_id = 3 AND setting_key LIKE 'secret_scanning.%'")
		require.NoError(t, err)
		if !on {
			_, err = db.Exec(t.Context(), "INSERT INTO user_setting (user_id, setting_key, setting_value) VALUES (3, ?, 'false')", secretscan_model.OrgKey)
			require.NoError(t, err)
		}
		if locked {
			_, err = db.Exec(t.Context(), "INSERT INTO user_setting (user_id, setting_key, setting_value) VALUES (3, ?, 'true')", secretscan_model.OrgLockedKey)
			require.NoError(t, err)
		}
	}
	reload := func(r *repo_model.Repository) *repo_model.Repository {
		return unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: r.ID})
	}
	optOut := func(r *repo_model.Repository, out bool) {
		t.Helper()
		_, err := db.Exec(t.Context(), "UPDATE repository SET secret_scanning_disabled = ? WHERE id = ?", out, r.ID)
		require.NoError(t, err)
	}

	assert.True(t, Enabled(t.Context(), reload(repo)))
	assert.True(t, Enabled(t.Context(), reload(org)))

	t.Run("repository opt-out", func(t *testing.T) {
		optOut(repo, true)
		assert.False(t, Enabled(t.Context(), reload(repo)))
		optOut(repo, false)
	})
	t.Run("organization off", func(t *testing.T) {
		setOrg(false, false)
		assert.False(t, Enabled(t.Context(), reload(org)))
		assert.True(t, Enabled(t.Context(), reload(repo)), "a personal repository has no organization")
	})
	t.Run("a lock overrides the opt-out of a repository", func(t *testing.T) {
		optOut(org, true)
		setOrg(true, false)
		assert.False(t, Enabled(t.Context(), reload(org)))
		setOrg(true, true)
		assert.True(t, Enabled(t.Context(), reload(org)))
		setOrg(false, true)
		assert.False(t, Enabled(t.Context(), reload(org)), "an organization that is off is off for every repository")
		optOut(org, false)
		setOrg(true, false)
	})
	t.Run("site off", func(t *testing.T) {
		defer test.MockVariableValue(&setting.SecretScanning.Enabled, false)()
		assert.False(t, Enabled(t.Context(), reload(repo)))
		storeSiteSwitch(t, "on")
		assert.True(t, Enabled(t.Context(), reload(repo)), "the stored switch wins over app.ini")
		storeSiteSwitch(t, "off")
		assert.False(t, Enabled(t.Context(), reload(repo)))
		storeSiteSwitch(t, "") // app.ini again
	})
	t.Run("the SQL form agrees", func(t *testing.T) {
		for _, c := range []struct{ on, locked, optOut bool }{{true, false, false}, {true, false, true}, {true, true, true}, {false, false, false}, {false, true, true}} {
			setOrg(c.on, c.locked)
			optOut(org, c.optOut)
			var off []int64
			require.NoError(t, db.GetEngine(t.Context()).Table("repository").Select("id").Where("id = ?", org.ID).And(secretscan_model.OffCond()).Find(&off))
			assert.Equal(t, !Enabled(t.Context(), reload(org)), len(off) == 1, "%+v", c)
		}
	})
}

func TestScanSwitchesAndKeptRows(t *testing.T) {
	repo := enable(t)
	w := newWork(t, repo)
	a, b := token("kept-a"), token("kept-b")
	w.commit(map[string]string{"a.txt": a + "\n"})
	w.pushDefault()
	require.NoError(t, Scan(t.Context(), repo.ID))
	require.NotNil(t, alertFor(t, repo.ID, a))
	state := scanOf(t, repo.ID, secretscan_model.KindRepo)

	// off: no scan, and the rows stay
	_, err := db.Exec(t.Context(), "UPDATE repository SET secret_scanning_disabled = ? WHERE id = ?", true, repo.ID)
	require.NoError(t, err)
	w.commit(map[string]string{"b.txt": b + "\n"})
	w.pushDefault()
	require.NoError(t, Scan(t.Context(), repo.ID))
	assert.Nil(t, alertFor(t, repo.ID, b))
	assert.NotNil(t, alertFor(t, repo.ID, a), "off keeps the alerts")
	assert.Equal(t, state.Version, scanOf(t, repo.ID, secretscan_model.KindRepo).Version)

	// site off
	_, err = db.Exec(t.Context(), "UPDATE repository SET secret_scanning_disabled = ? WHERE id = ?", false, repo.ID)
	require.NoError(t, err)
	storeSiteSwitch(t, "off")
	require.NoError(t, Scan(t.Context(), repo.ID))
	assert.Nil(t, alertFor(t, repo.ID, b))
	assert.NotNil(t, alertFor(t, repo.ID, a))

	// on again: a scan from the stored state reads only what is new
	storeSiteSwitch(t, "on")
	require.NoError(t, Scan(t.Context(), repo.ID))
	assert.NotNil(t, alertFor(t, repo.ID, b))
	assert.EqualValues(t, 1, scanOf(t, repo.ID, secretscan_model.KindRepo).Blobs)
}

func TestForkStartsFromParentState(t *testing.T) {
	parent := enable(t)
	fork := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 2})
	n := &notifier{}
	q := mockQueue(t)
	queued := func(id int64) bool {
		has, err := q.Has(id)
		require.NoError(t, err)
		return has
	}

	// the parent was never scanned: the fork scans everything
	n.ForkRepository(t.Context(), nil, parent, fork)
	assert.Nil(t, scanOf(t, fork.ID, secretscan_model.KindRepo))
	assert.True(t, queued(fork.ID))
	require.NoError(t, q.RemoveAllItems(t.Context()))

	// the parent's scan is incomplete: the same
	require.NoError(t, secretscan_model.Save(t.Context(), nil, &secretscan_model.Scan{RepoID: parent.ID, Kind: secretscan_model.KindRepo, Status: secretscan_model.StatusIncomplete, ScannedTips: `["a"]`}))
	n.ForkRepository(t.Context(), nil, parent, fork)
	assert.Nil(t, scanOf(t, fork.ID, secretscan_model.KindRepo))

	// complete: the fork starts from its tips
	cur := scanOf(t, parent.ID, secretscan_model.KindRepo)
	cur.Status, cur.ScannedTips = secretscan_model.StatusOK, `["abc","def"]`
	require.NoError(t, secretscan_model.Save(t.Context(), cur, cur))
	n.ForkRepository(t.Context(), nil, parent, fork)
	s := scanOf(t, fork.ID, secretscan_model.KindRepo)
	require.NotNil(t, s)
	assert.Equal(t, `["abc","def"]`, s.ScannedTips)
	assert.Equal(t, secretscan_model.StatusOK, s.Status)
	assert.True(t, queued(fork.ID))
	assert.Empty(t, alertsOf(t, fork.ID), "the alerts stay with the parent")
}

func TestNotifierEnqueues(t *testing.T) {
	repo := enable(t)
	q := mockQueue(t)
	n := &notifier{}
	queued := func() bool {
		has, err := q.Has(repo.ID)
		require.NoError(t, err)
		if has {
			require.NoError(t, q.RemoveAllItems(t.Context()))
		}
		return has
	}
	events := map[string]func(){
		"push":      func() { n.PushCommits(t.Context(), nil, repo, nil, nil) },
		"mirror":    func() { n.SyncPushCommits(t.Context(), nil, repo, nil, nil) },
		"migrate":   func() { n.MigrateRepository(t.Context(), nil, nil, repo) },
		"transfer":  func() { n.TransferRepository(t.Context(), nil, repo, "old") },
		"wiki new":  func() { n.NewWikiPage(t.Context(), nil, repo, "p", "") },
		"wiki edit": func() { n.EditWikiPage(t.Context(), nil, repo, "p", "") },
	}
	for name, fire := range events {
		fire()
		assert.True(t, queued(), name)
	}
	// the queue holds a repository once
	Enqueue(repo.ID)
	require.ErrorIs(t, q.Push(repo.ID), queue.ErrAlreadyInQueue)
	require.NoError(t, q.RemoveAllItems(t.Context()))

	defer test.MockVariableValue(&setting.SecretScanning.Enabled, false)()
	for name, fire := range events {
		fire()
		assert.False(t, queued(), "off: "+name)
	}
}

func TestEnqueueUnscanned(t *testing.T) {
	enable(t)
	q := mockQueue(t)
	ids := func() map[int64]bool {
		m := map[int64]bool{}
		for id := int64(1); id < 2000; id++ {
			if has, err := q.Has(id); err == nil && has {
				m[id] = true
			}
		}
		require.NoError(t, q.RemoveAllItems(t.Context()))
		return m
	}
	ready := func() []int64 {
		var all []int64
		require.NoError(t, db.GetEngine(t.Context()).Table("repository").Select("id").Where("is_empty = ? AND status = ? AND trashed_unix = 0", false, repo_model.RepositoryReady).Find(&all))
		return all
	}
	require.NoError(t, EnqueueUnscanned(t.Context()))
	got := ids()
	assert.NotEmpty(t, got)
	for _, id := range ready() {
		assert.True(t, got[id], id)
	}

	// states: ok and incomplete are left alone, a failure waits a day
	save := func(id int64, status string, age time.Duration) {
		require.NoError(t, secretscan_model.Save(t.Context(), nil, &secretscan_model.Scan{RepoID: id, Kind: secretscan_model.KindRepo, Status: status, ScannedTips: "[]"}))
		_, err := db.Exec(t.Context(), "UPDATE secret_scan SET updated_unix = ? WHERE repo_id = ? AND kind = 'repo'", time.Now().Add(-age).Unix(), id)
		require.NoError(t, err)
	}
	save(1, secretscan_model.StatusOK, 0)
	save(2, secretscan_model.StatusIncomplete, 48*time.Hour)
	save(3, secretscan_model.StatusFailed, time.Hour)
	save(4, secretscan_model.StatusFailed, 48*time.Hour)
	_, err := db.Exec(t.Context(), "UPDATE repository SET secret_scanning_disabled = ? WHERE id = ?", true, 5)
	require.NoError(t, err)
	require.NoError(t, EnqueueUnscanned(t.Context()))
	got = ids()
	assert.False(t, got[1], "scanned")
	assert.False(t, got[2], "incomplete: not retried")
	assert.False(t, got[3], "failed an hour ago")
	assert.True(t, got[4], "failed two days ago")
	assert.False(t, got[5], "turned off by its admin")

	defer test.MockVariableValue(&setting.SecretScanning.Enabled, false)()
	require.NoError(t, EnqueueUnscanned(t.Context()))
	assert.Empty(t, ids(), "the site switch is off")
}

func TestSettingsMerge(t *testing.T) {
	enable(t)
	ctx := t.Context()
	assert.EqualValues(t, 1, setting.SecretScanningSettings(ctx).MaxConcurrentScans)
	generic := true
	cfg := setting.SecretScanningConfig{GenericRules: &generic, Limits: map[string]int64{"max_alerts_per_repo": 5, "scan_timeout": 90, "max_blob_size": 0, "nope": 1}}
	o := cfg.Merge()
	assert.True(t, o.GenericRules)
	assert.EqualValues(t, 5, o.MaxAlertsPerRepo)
	assert.Equal(t, 90*time.Second, o.ScanTimeout)
	assert.EqualValues(t, 1<<20, o.MaxBlobSize, "out of bounds: the app.ini value")
	assert.Equal(t, setting.SecretScanning.MaxScanBytes, o.MaxScanBytes)
	assert.Equal(t, setting.SecretScanning.MaxScanBytes, setting.SecretScanningConfig{Invalid: true}.Merge().MaxScanBytes)
}

func TestScanCanary(t *testing.T) {
	repo := enable(t)
	w := newWork(t, repo)
	canary := token("canary")
	body := strings.TrimPrefix(canary, "ghp"+"_")
	lc, cancel := test.NewLogChecker(log.DEFAULT, log.TRACE)
	defer cancel()
	lc.Filter(canary, body, body[:12]).StopMark("canary-stop-mark")

	w.commit(map[string]string{"c.env": "export T=" + canary + "\n"}, "Dev", "dev@example.com")
	w.pushDefault()
	require.NoError(t, Scan(t.Context(), repo.ID))
	require.NotNil(t, alertFor(t, repo.ID, canary))
	// a scan that panics with the secret in the panic value
	defer test.MockVariableValue(&scanContent, func(*secretdetection.Detector, []byte) ([]secretdetection.Finding, bool) { panic("boom " + canary) })()
	w.commit(map[string]string{"d.env": "export T=" + token("canary2") + "\n"})
	w.pushDefault()
	require.NoError(t, Scan(t.Context(), repo.ID))
	assert.Equal(t, secretscan_model.StatusFailed, scanOf(t, repo.ID, secretscan_model.KindRepo).Status)

	for _, table := range []string{"secret_scan", "secret_scanning_alert", "secret_scanning_location", "secret_scanning_alert_index", "audit_event"} {
		rows, err := db.GetEngine(t.Context()).Query("SELECT * FROM " + table)
		require.NoError(t, err)
		for _, row := range rows {
			for col, v := range row {
				for _, needle := range []string{canary, body, body[:12]} {
					assert.NotContains(t, string(v), needle, "%s.%s", table, col)
				}
			}
		}
	}
	log.Info("canary-stop-mark")
	seen, stopped := lc.Check(5 * time.Second)
	require.True(t, stopped)
	assert.Equal(t, []bool{false, false, false}, seen, "the secret is in no log line")
}
