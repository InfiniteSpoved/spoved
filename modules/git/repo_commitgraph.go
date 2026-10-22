// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package git

import (
	"context"
	"fmt"
	"time"

	"forgejo.org/modules/setting"
)

// WriteCommitGraph write commit graph to speed up repo access
// this requires git v2.18 to be installed
func WriteCommitGraph(ctx context.Context, repoPath string) error {
	if _, _, err := NewCommand(ctx, "commit-graph", "write").RunStdString(&RunOpts{Dir: repoPath}); err != nil {
		return fmt.Errorf("unable to write commit-graph for '%s' : %w", repoPath, err)
	}
	return nil
}

// WriteMultiPackIndexBitmap writes a multi-pack-index with a reachability bitmap, which makes building the pack of a
// full clone or fetch much cheaper. A repository created by `git clone` has none until git's own gc repacks it. The
// geometric repack also packs loose objects (a small clone may have no pack) and leaves large packs as they are.
func WriteMultiPackIndexBitmap(ctx context.Context, repoPath string) error {
	if _, _, err := NewCommand(ctx, "repack", "--geometric=2", "-d", "--write-midx", "--write-bitmap-index", "-q").RunStdString(&RunOpts{Dir: repoPath, Timeout: time.Duration(setting.Git.Timeout.GC) * time.Second}); err != nil {
		return fmt.Errorf("unable to write multi-pack-index bitmap for '%s' : %w", repoPath, err)
	}
	return nil
}
