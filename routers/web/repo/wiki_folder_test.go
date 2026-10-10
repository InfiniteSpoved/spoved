// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPLv3-or-later

package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"forgejo.org/modules/git"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWikiFolderPages(t *testing.T) {
	dir := t.TempDir()
	files := []string{"z.md", "a/b.md", "a/c/d.markdown", "a.md", "A.md", "a-b.md", "_Sidebar.md", "sub/_Sidebar.md", "x.txt", "README.MD"}
	for _, f := range files {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, f), []byte("# Title of "+f+"\n"), 0o644))
	}
	require.NoError(t, os.Symlink("z.md", filepath.Join(dir, "link.md")))
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=a", "-c", "user.email=a@b", "commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	repo, err := git.OpenRepository(t.Context(), dir)
	require.NoError(t, err)
	defer repo.Close()
	commit, err := repo.GetCommit("HEAD")
	require.NoError(t, err)

	paths := func(limit int) []string {
		pages, err := listWikiFolderPages(&commit.Tree, "", limit, nil)
		require.NoError(t, err)
		var out []string
		for _, p := range pages {
			out = append(out, p.Path)
		}
		return out
	}
	// path (byte) order, Markdown files only, no symlinks, no root sidebar
	all := []string{"A.md", "README.MD", "a-b.md", "a.md", "a/b.md", "a/c/d.markdown", "sub/_Sidebar.md", "z.md"}
	assert.Equal(t, all, paths(100))
	// cut after limit+1 entries
	assert.Equal(t, all[:4], paths(3))

	home, err := wikiFolderHome(&commit.Tree)
	require.NoError(t, err)
	assert.Equal(t, "README.MD", home)

	assert.Equal(t, "Install", wikiFolderTitle([]byte("intro\n#  Install \nmore"), "x"))
	assert.Equal(t, "x", wikiFolderTitle([]byte("## Sub\n#nospace"), "x"))
}
