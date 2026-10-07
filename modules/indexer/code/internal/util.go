// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package internal

import (
	"strings"
	"unicode"

	"forgejo.org/modules/indexer/internal"
)

// FilenameIndexerID returns the document ID of a file on a branch. ':' can't
// be part of a ref name, so the branch can't swallow a part of the filename.
func FilenameIndexerID(repoID int64, branch, filename string) string {
	return internal.Base36(repoID) + "_" + branch + ":" + filename
}

// HasPunctuation reports whether s has a character that word tokenizers drop.
func HasPunctuation(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && !unicode.IsSpace(r)
	})
}
