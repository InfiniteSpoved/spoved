// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later
package git

import (
	"strings"
)

// CheckIfDiffDiffers returns if the changes of newCommitID over newMergeBase differ from the changes of oldCommitID
// over oldMergeBase, the diff a reviewer saw. They are the same only if replaying the old changes onto newMergeBase
// (a 3-way merge with oldMergeBase as its base, as a rebase or merging the base branch does) gives exactly the tree
// of newCommitID, so commit IDs, messages and line numbers don't matter but every byte of the result does. A
// conflict counts as changed, and so does a Git without merge-tree --merge-base (before 2.40) when the merge bases
// differ.
func (repo *Repository) CheckIfDiffDiffers(oldMergeBase, oldCommitID, newMergeBase, newCommitID string, env []string) (hasChanged bool, err error) {
	want := oldCommitID + "^{tree}"
	if oldMergeBase != newMergeBase {
		if CheckGitVersionAtLeast("2.40") != nil {
			return true, nil
		}
		stdout, _, err := NewCommand(repo.Ctx, "merge-tree", "--write-tree", "--no-messages").
			AddOptionFormat("--merge-base=%s", oldMergeBase).
			AddDynamicArguments(newMergeBase, oldCommitID).
			RunStdString(&RunOpts{Dir: repo.Path, Env: env})
		if IsErrorExitCode(err, 1) {
			return true, nil // conflict
		} else if err != nil {
			return true, err
		}
		want, _, _ = strings.Cut(stdout, "\n")
	}
	stdout, _, err := NewCommand(repo.Ctx, "rev-parse").AddDynamicArguments(want, newCommitID+"^{tree}").RunStdString(&RunOpts{Dir: repo.Path, Env: env})
	if err != nil {
		return true, err
	}
	trees := strings.Fields(stdout)
	return len(trees) != 2 || trees[0] != trees[1], nil
}
