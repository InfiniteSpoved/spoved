// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/dependency"
	"forgejo.org/modules/git"
	"forgejo.org/modules/queue"
	"forgejo.org/modules/repository"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"
	"forgejo.org/modules/timeutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const goMod = "module example.com/m\n\ngo 1.22\n\nrequire (\n\tgithub.com/gin-gonic/gin v1.9.1\n\tgolang.org/x/net v0.10.0 // indirect\n)\n"

const lockV3 = `{"lockfileVersion":3,"packages":{"":{"dependencies":{"left-pad":"^1.3.0"}},"node_modules/left-pad":{"version":"1.3.0","license":"WTFPL"}}}`

// commitFiles replaces the default branch of the repository with one commit holding the files (and symlinks, path
// to target) and returns its SHA. The repository is a bare directory of the fixtures, so git is run from a clone.
func commitFiles(t *testing.T, repo *repo_model.Repository, files, symlinks map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "-c", "core.quotepath=off"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", repo.DefaultBranch)
	for p, c := range files {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644))
	}
	for p, target := range symlinks {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755))
		require.NoError(t, os.Symlink(target, filepath.Join(dir, p)))
	}
	run("add", "-A")
	run("commit", "-q", "--allow-empty", "-m", "files")
	run("push", "-q", "--force", repo.RepoPath(), "HEAD:refs/heads/"+repo.DefaultBranch)
	return run("rev-parse", "HEAD")
}

func enable(t *testing.T) *repo_model.Repository {
	t.Helper()
	require.NoError(t, unittest.PrepareTestDatabase())
	t.Cleanup(test.MockVariableValue(&setting.DependencyGraph.Enabled, true))
	t.Cleanup(test.MockVariableValue(&setting.DependencyGraph.MaxManifestSize, 20<<20))
	t.Cleanup(test.MockVariableValue(&setting.DependencyGraph.MaxManifests, 500))
	t.Cleanup(test.MockVariableValue(&setting.DependencyGraph.MaxDependencies, 50000))
	t.Cleanup(test.MockVariableValue(&setting.DependencyGraph.MaxTreeEntries, 200000))
	t.Cleanup(test.MockVariableValue(&setting.DependencyGraph.ScanTimeout, time.Minute))
	return unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
}

func rowsOf(t *testing.T, repoID int64) map[string]*repo_model.Dependency {
	t.Helper()
	var rows []*repo_model.Dependency
	require.NoError(t, db.GetEngine(t.Context()).Where("repo_id = ?", repoID).Find(&rows))
	m := map[string]*repo_model.Dependency{}
	for _, r := range rows {
		m[r.Ecosystem+"|"+r.Name+"@"+r.Version+"|"+r.ManifestPath] = r
	}
	return m
}

func TestScan(t *testing.T) {
	repo := enable(t)
	sha := commitFiles(t, repo, map[string]string{
		"go.mod":                             goMod,
		"web/package-lock.json":              lockV3,
		"web/package.json":                   `{"dependencies":{"left-pad":"^1.3.0"}}`,
		"vendor/go.mod":                      goMod, // vendored
		"a/node_modules/x/package-lock.json": lockV3,
		"vendored/go.mod":                    "module v\n\nrequire example.com/vend v1.0.0\n",
		"testdata/go.mod":                    goMod,
		"req/requirements.txt":               "django==4.2\n-r ../../../../etc/passwd\n--index-url http://169.254.169.254\n",
		"bad/pom.xml":                        `<?xml version="1.0"?><!DOCTYPE x [<!ENTITY e SYSTEM "file:///etc/passwd">]><project><artifactId>&e;</artifactId></project>`,
		"we\nird/go.mod":                     goMod,
		"link/package-lock.json.txt":         "not a manifest",
		"README.md":                          "hi",
	}, map[string]string{
		"link/go.mod":     "/etc/passwd",
		"link2/go.mod":    "../../other-repo/go.mod",
		"link3/yarn.lock": "../web/package-lock.json",
	})

	require.NoError(t, Scan(t.Context(), repo.ID))

	rows := rowsOf(t, repo.ID)
	require.Len(t, rows, 5, "%v", keys(rows))
	assert.True(t, rows["Go|github.com/gin-gonic/gin@v1.9.1|go.mod"].Direct)
	assert.False(t, rows["Go|golang.org/x/net@v0.10.0|go.mod"].Direct)
	assert.Contains(t, rows, "Go|example.com/vend@v1.0.0|vendored/go.mod")
	left := rows["npm|left-pad@1.3.0|web/package-lock.json"]
	require.NotNil(t, left)
	assert.True(t, left.Direct)
	assert.Equal(t, "WTFPL", left.License)
	assert.Contains(t, rows, "PyPI|django@4.2|req/requirements.txt")

	scan, err := repo_model.GetRepoDependencyScan(t.Context(), repo.ID)
	require.NoError(t, err)
	assert.Equal(t, sha, scan.CommitSHA)
	assert.Equal(t, repo_model.DependencyScanOK, scan.Status)
	assert.Empty(t, scan.Error)
	assert.Equal(t, 4, scan.ManifestCount, "go.mod, the lock file, vendored/go.mod, requirements.txt")
	assert.Equal(t, 5, scan.DependencyCount)
	assert.ElementsMatch(t, []repo_model.SkippedManifest{
		{Path: "bad/pom.xml", Code: "parse_error"},
		{Path: "weird/go.mod", Code: "invalid_path"},
	}, scan.SkippedManifests(), "the path of the second one is stored without its newline")
	assert.NotZero(t, scan.ScannedUnix)

	// a second scan of the same commit reads nothing
	var parses atomic.Int32
	defer test.MockVariableValue(&parseManifest, func(context.Context, *dependency.Format, []byte, dependency.Finder) ([]dependency.Dependency, error) {
		parses.Add(1)
		return nil, nil
	})()
	require.NoError(t, Scan(t.Context(), repo.ID))
	assert.Zero(t, parses.Load())

	// a push that removes the manifests drops their rows
	sha2 := commitFiles(t, repo, map[string]string{"README.md": "hi"}, nil)
	parseManifest = func(_ context.Context, f *dependency.Format, data []byte, find dependency.Finder) ([]dependency.Dependency, error) {
		return f.Parse(data, find)
	}
	require.NoError(t, Scan(t.Context(), repo.ID))
	assert.Empty(t, rowsOf(t, repo.ID))
	scan, _ = repo_model.GetRepoDependencyScan(t.Context(), repo.ID)
	assert.Equal(t, sha2, scan.CommitSHA)
	assert.Zero(t, scan.ManifestCount)
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestScanCaps(t *testing.T) {
	repo := enable(t)
	files := map[string]string{
		"go.mod":                goMod,
		"a/go.mod":              goMod,
		"web/package-lock.json": lockV3,
	}
	commitFiles(t, repo, files, nil)

	t.Run("manifest size", func(t *testing.T) {
		defer test.MockVariableValue(&setting.DependencyGraph.MaxManifestSize, int64(len(lockV3)-1))()
		require.NoError(t, Scan(t.Context(), repo.ID))
		scan, _ := repo_model.GetRepoDependencyScan(t.Context(), repo.ID)
		assert.Equal(t, []repo_model.SkippedManifest{{Path: "web/package-lock.json", Code: "manifest_too_large"}}, scan.SkippedManifests())
		assert.Equal(t, 2, scan.ManifestCount)
		assert.Len(t, rowsOf(t, repo.ID), 4, "the other manifests were read")
	})

	t.Run("too many manifests", func(t *testing.T) {
		defer test.MockVariableValue(&setting.DependencyGraph.MaxManifests, 1)()
		require.NoError(t, db.TruncateBeans(t.Context(), &repo_model.DependencyScan{}))
		require.NoError(t, Scan(t.Context(), repo.ID))
		scan, _ := repo_model.GetRepoDependencyScan(t.Context(), repo.ID)
		assert.Equal(t, 1, scan.ManifestCount)
		assert.Len(t, scan.SkippedManifests(), 2)
		for _, s := range scan.SkippedManifests() {
			assert.Equal(t, "too_many_manifests", s.Code)
		}
		assert.Len(t, rowsOf(t, repo.ID), 2)
	})

	t.Run("too many dependencies", func(t *testing.T) {
		defer test.MockVariableValue(&setting.DependencyGraph.MaxDependencies, 2)()
		require.NoError(t, db.TruncateBeans(t.Context(), &repo_model.DependencyScan{}))
		require.NoError(t, Scan(t.Context(), repo.ID))
		scan, _ := repo_model.GetRepoDependencyScan(t.Context(), repo.ID)
		assert.Equal(t, 2, scan.DependencyCount, "a manifest that would pass the cap is skipped whole")
		assert.Equal(t, 1, scan.ManifestCount)
		assert.Len(t, scan.SkippedManifests(), 2)
		assert.Equal(t, "too_many_dependencies", scan.SkippedManifests()[0].Code)
		assert.Len(t, rowsOf(t, repo.ID), 2)
	})

	t.Run("tree too large keeps the old rows", func(t *testing.T) {
		require.NoError(t, db.TruncateBeans(t.Context(), &repo_model.DependencyScan{}))
		require.NoError(t, Scan(t.Context(), repo.ID))
		before := len(rowsOf(t, repo.ID))
		require.Positive(t, before)
		old, _ := repo_model.GetRepoDependencyScan(t.Context(), repo.ID)

		many := map[string]string{"go.mod": goMod}
		for i := range 10 {
			many["d/f"+strconv.Itoa(i)] = "x"
		}
		commitFiles(t, repo, many, nil)
		defer test.MockVariableValue(&setting.DependencyGraph.MaxTreeEntries, 5)()
		require.NoError(t, Scan(t.Context(), repo.ID))
		scan, _ := repo_model.GetRepoDependencyScan(t.Context(), repo.ID)
		assert.Equal(t, repo_model.DependencyScanFailed, scan.Status)
		assert.Equal(t, "tree_too_large", scan.Error)
		assert.Equal(t, old.CommitSHA, scan.CommitSHA, "the rows still belong to the old commit")
		assert.Len(t, rowsOf(t, repo.ID), before)
	})
}

func TestScanTimeoutKeepsOldRows(t *testing.T) {
	repo := enable(t)
	sha := commitFiles(t, repo, map[string]string{"go.mod": goMod}, nil)
	require.NoError(t, Scan(t.Context(), repo.ID))
	require.Len(t, rowsOf(t, repo.ID), 2)

	commitFiles(t, repo, map[string]string{"go.mod": "module m\n\nrequire example.com/new v1.0.0\n"}, nil)
	defer test.MockVariableValue(&setting.DependencyGraph.ScanTimeout, 100*time.Millisecond)()
	defer test.MockVariableValue(&parseManifest, func(ctx context.Context, f *dependency.Format, data []byte, find dependency.Finder) ([]dependency.Dependency, error) {
		<-ctx.Done() // a parser that takes too long
		return f.Parse(data, find)
	})()
	require.NoError(t, Scan(t.Context(), repo.ID))

	scan, _ := repo_model.GetRepoDependencyScan(t.Context(), repo.ID)
	assert.Equal(t, "timeout", scan.Error)
	assert.Equal(t, repo_model.DependencyScanFailed, scan.Status)
	assert.Equal(t, sha, scan.CommitSHA)
	rows := rowsOf(t, repo.ID)
	assert.Len(t, rows, 2)
	assert.Contains(t, rows, "Go|github.com/gin-gonic/gin@v1.9.1|go.mod")
}

func TestScanSkips(t *testing.T) {
	repo := enable(t)
	commitFiles(t, repo, map[string]string{"go.mod": goMod}, nil)

	t.Run("deleted repository", func(t *testing.T) {
		require.NoError(t, Scan(t.Context(), 999999))
		assert.Empty(t, rowsOf(t, 999999))
	})
	t.Run("trashed repository", func(t *testing.T) {
		_, err := db.GetEngine(t.Context()).Exec("UPDATE repository SET trashed_unix = ? WHERE id = ?", timeutil.TimeStampNow(), repo.ID)
		require.NoError(t, err)
		defer func() {
			_, _ = db.GetEngine(t.Context()).Exec("UPDATE repository SET trashed_unix = 0 WHERE id = ?", repo.ID)
		}()
		require.NoError(t, Scan(t.Context(), repo.ID))
		assert.Empty(t, rowsOf(t, repo.ID))
	})
	t.Run("disabled", func(t *testing.T) {
		defer test.MockVariableValue(&setting.DependencyGraph.Enabled, false)()
		require.NoError(t, Scan(t.Context(), repo.ID))
		assert.Empty(t, rowsOf(t, repo.ID))
		scan, _ := repo_model.GetRepoDependencyScan(t.Context(), repo.ID)
		assert.Nil(t, scan)
	})
	t.Run("empty and migrating", func(t *testing.T) {
		for _, set := range []string{"is_empty = ?", "status = ?"} {
			_, err := db.GetEngine(t.Context()).Exec("UPDATE repository SET "+set+" WHERE id = ?", map[string]any{"is_empty = ?": true, "status = ?": int(repo_model.RepositoryBeingMigrated)}[set], repo.ID)
			require.NoError(t, err)
			require.NoError(t, Scan(t.Context(), repo.ID))
			assert.Empty(t, rowsOf(t, repo.ID))
		}
		require.NoError(t, unittest.PrepareTestDatabase())
	})
}

func TestEnqueueUnscanned(t *testing.T) {
	repo := enable(t)
	commitFiles(t, repo, map[string]string{"go.mod": goMod}, nil)
	ids, err := repo_model.UnscannedDependencyRepoIDs(t.Context(), 0, 1000)
	require.NoError(t, err)
	assert.Contains(t, ids, repo.ID, "never scanned")

	require.NoError(t, repo_model.ReplaceRepoDependencies(t.Context(), &repo_model.DependencyScan{RepoID: repo.ID, CommitSHA: "old", Status: "ok"}, []*repo_model.Dependency{}))
	ids, _ = repo_model.UnscannedDependencyRepoIDs(t.Context(), 0, 1000)
	assert.Contains(t, ids, repo.ID, "scanned at another commit than the head of the branch table")

	var head string
	_, err = db.GetEngine(t.Context()).SQL("SELECT commit_id FROM branch WHERE repo_id = ? AND name = ?", repo.ID, repo.DefaultBranch).Get(&head)
	require.NoError(t, err)
	require.NotEmpty(t, head)
	require.NoError(t, repo_model.ReplaceRepoDependencies(t.Context(), &repo_model.DependencyScan{RepoID: repo.ID, CommitSHA: head, Status: "ok"}, nil))
	ids, _ = repo_model.UnscannedDependencyRepoIDs(t.Context(), 0, 1000)
	assert.NotContains(t, ids, repo.ID, "up to date")

	// a failed scan of a new head waits for the retry time
	_, err = db.GetEngine(t.Context()).Exec("UPDATE repo_dependency_scan SET commit_sha = 'old', error = 'timeout', scanned_unix = ? WHERE repo_id = ?", timeutil.TimeStampNow(), repo.ID)
	require.NoError(t, err)
	ids, _ = repo_model.UnscannedDependencyRepoIDs(t.Context(), timeutil.TimeStampNow().Add(-3600), 1000)
	assert.NotContains(t, ids, repo.ID)
	ids, _ = repo_model.UnscannedDependencyRepoIDs(t.Context(), timeutil.TimeStampNow().Add(3600), 1000)
	assert.Contains(t, ids, repo.ID)

	// the batch is limited
	ids, _ = repo_model.UnscannedDependencyRepoIDs(t.Context(), timeutil.TimeStampNow().Add(3600), 1)
	assert.Len(t, ids, 1)

	// disabled: the cron task does nothing
	defer test.MockVariableValue(&setting.DependencyGraph.Enabled, false)()
	require.NoError(t, EnqueueUnscanned(t.Context()))
}

// TestQueueDedup pushes a repository 20 times while its scan is blocked: one running, at most one waiting.
func TestQueueDedup(t *testing.T) {
	enable(t)
	cfg, err := setting.NewConfigProviderFromData("[queue.dependency_graph]\nTYPE = channel\nMAX_WORKERS = 1\nBATCH_LENGTH = 1\n")
	require.NoError(t, err)
	defer test.MockVariableValue(&setting.CfgProvider, cfg)()
	release := make(chan struct{})
	var started, handled atomic.Int32
	q := queue.CreateUniqueQueue(t.Context(), "dependency_graph", func(items ...int64) []int64 {
		started.Add(1)
		<-release
		handled.Add(int32(len(items)))
		return nil
	})
	require.NotNil(t, q)
	defer test.MockVariableValue(&scanQueue, q)()
	go q.Run()

	Enqueue(1)
	require.Eventually(t, func() bool { return started.Load() == 1 }, 5*time.Second, 10*time.Millisecond)
	for range 20 {
		Enqueue(1)
	}
	assert.LessOrEqual(t, q.GetQueueItemNumber(), 1)
	close(release)
	require.Eventually(t, func() bool { return handled.Load() >= 2 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	assert.EqualValues(t, 2, handled.Load(), "one scan that ran and one that waited")

	t.Run("disabled", func(t *testing.T) {
		defer test.MockVariableValue(&setting.DependencyGraph.Enabled, false)()
		before := started.Load()
		Enqueue(1)
		time.Sleep(100 * time.Millisecond)
		assert.Equal(t, before, started.Load())
	})
}

// TestNotifier: only the default branch of the repository queues a scan; tags, other branches and the refs of AGit
// and pull requests never do. Mirror syncs and a changed default branch do.
func TestNotifier(t *testing.T) {
	repo := enable(t)
	cfg, err := setting.NewConfigProviderFromData("[queue.dependency_graph]\nTYPE = channel\n")
	require.NoError(t, err)
	defer test.MockVariableValue(&setting.CfgProvider, cfg)()
	q := queue.CreateUniqueQueue(t.Context(), "dependency_graph", func(items ...int64) []int64 { return nil })
	require.NotNil(t, q)
	defer test.MockVariableValue(&scanQueue, q)()
	n := &notifier{}
	queued := func() bool {
		has, err := q.Has(repo.ID)
		require.NoError(t, err)
		if has {
			require.NoError(t, q.RemoveAllItems(t.Context()))
		}
		return has
	}
	push := func(ref string) *repository.PushUpdateOptions {
		return &repository.PushUpdateOptions{RefFullName: git.RefName(ref)}
	}

	for _, ref := range []string{"refs/heads/feature", "refs/tags/master", "refs/for/master", "refs/for/master/topic", "refs/pull/1/head", "refs/heads/master/sub"} {
		n.PushCommits(t.Context(), nil, repo, push(ref), nil)
		n.SyncPushCommits(t.Context(), nil, repo, push(ref), nil)
		assert.False(t, queued(), ref)
	}
	n.PushCommits(t.Context(), nil, repo, push("refs/heads/master"), nil)
	assert.True(t, queued(), "push to the default branch")
	n.SyncPushCommits(t.Context(), nil, repo, push("refs/heads/master"), nil)
	assert.True(t, queued(), "mirror sync of the default branch")
	n.ChangeDefaultBranch(t.Context(), repo)
	assert.True(t, queued(), "a new default branch")

	defer test.MockVariableValue(&setting.DependencyGraph.Enabled, false)()
	n.PushCommits(t.Context(), nil, repo, push("refs/heads/master"), nil)
	n.ChangeDefaultBranch(t.Context(), repo)
	assert.False(t, queued(), "the feature is off")
}
