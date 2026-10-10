// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package release

import (
	"testing"

	issues_model "forgejo.org/models/issues"
	user_model "forgejo.org/models/user"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestRenderNotes(t *testing.T) {
	pr := func(index int64, title, poster string, labels ...string) *issues_model.PullRequest {
		issue := &issues_model.Issue{Index: index, Title: title, Poster: &user_model.User{Name: poster}}
		for _, l := range labels {
			issue.Labels = append(issue.Labels, &issues_model.Label{Name: l})
		}
		return &issues_model.PullRequest{Issue: issue}
	}
	prs := issues_model.PullRequestList{
		pr(1, "Add SSO login", "alice", "feature"),
		pr(2, "Fix crash", "bob", "bug"),
		pr(3, "Bump deps", "renovate-bot", "dependencies"),
		pr(4, "Update docs", "carol"),
	}
	const url = "https://example.com/owner/repo"

	t.Run("flat", func(t *testing.T) {
		assert.Equal(t, `## What's Changed
* Add SSO login by @alice in #1
* Fix crash by @bob in #2
* Bump deps by @renovate-bot in #3
* Update docs by @carol in #4

**Full Changelog**: https://example.com/owner/repo/compare/v1.0...v1.1
`, renderNotes(url, prs, nil, notesExtras{}, "v1.0", "v1.1", false))
	})

	t.Run("first release and truncated", func(t *testing.T) {
		assert.Equal(t, `## What's Changed

_Only the latest 1000 commits were scanned._

**Full Changelog**: https://example.com/owner/repo/commits/tag/v1.0
`, renderNotes(url, nil, nil, notesExtras{}, "", "v1.0", true))
	})

	t.Run("config", func(t *testing.T) {
		cfg := &notesConfig{}
		require.NoError(t, yaml.Unmarshal([]byte(`
changelog:
  exclude:
    labels: [dependencies]
    authors: [Carol]
  categories:
    - title: New features
      labels: [feature]
    - title: Bug fixes
      labels: [bug]
`), cfg))
		assert.Equal(t, `## What's Changed
### New features
* Add SSO login by @alice in #1
### Bug fixes
* Fix crash by @bob in #2

**Full Changelog**: https://example.com/owner/repo/compare/v1.0...v1.1
`, renderNotes(url, prs, cfg, notesExtras{}, "v1.0", "v1.1", false))
	})

	t.Run("wildcard and other", func(t *testing.T) {
		cfg := &notesConfig{}
		require.NoError(t, yaml.Unmarshal([]byte(`
changelog:
  categories:
    - title: "Bugs [fixed]"
      labels: [bug]
`), cfg))
		assert.Equal(t, `## What's Changed
### Bugs \[fixed\]
* Fix crash by @bob in #2
### Other Changes
* Add SSO login by @alice in #1
* Bump deps by @renovate-bot in #3
* Update docs by @carol in #4

**Full Changelog**: https://example.com/owner/repo/compare/v1.0...v1.1
`, renderNotes(url, prs, cfg, notesExtras{}, "v1.0", "v1.1", false))

		require.NoError(t, yaml.Unmarshal([]byte(`
changelog:
  categories:
    - title: Everything
      labels: ["*"]
`), cfg))
		assert.Contains(t, renderNotes(url, prs, cfg, notesExtras{}, "v1.0", "v1.1", false), "### Everything\n* Add SSO login")
	})
}

func TestRenderNotesExtras(t *testing.T) {
	pr := func(index int64, title, poster string, labels ...string) *issues_model.PullRequest {
		issue := &issues_model.Issue{Index: index, Title: title, Poster: &user_model.User{Name: poster}}
		for _, l := range labels {
			issue.Labels = append(issue.Labels, &issues_model.Label{Name: l})
		}
		return &issues_model.PullRequest{Issue: issue}
	}
	const url = "https://example.com/owner/repo"
	prs := issues_model.PullRequestList{
		pr(1, "Add SSO login", "alice", "feature"),
		pr(2, "Bump deps", "renovate-bot", "feature"),
		pr(3, "Fix crash", "bob", "bug"),
	}

	t.Run("new contributors and direct commits", func(t *testing.T) {
		extras := notesExtras{
			contributors: []newContributor{{name: "alice", index: 1}},
			commits:      []directCommit{{title: "Tweak [x]", author: "Dave", sha: "0123456789abcdef"}},
		}
		assert.Equal(t, `## What's Changed
* Add SSO login by @alice in #1
### Direct commits
* Tweak \[x\] by Dave ([0123456789](https://example.com/owner/repo/commit/0123456789abcdef))

## New Contributors
* @alice made their first contribution in #1

**Full Changelog**: https://example.com/owner/repo/compare/v1.0...v1.1
`, renderNotes(url, prs[:1], nil, extras, "v1.0", "v1.1", false))
	})

	t.Run("category exclude", func(t *testing.T) {
		cfg := &notesConfig{}
		require.NoError(t, yaml.Unmarshal([]byte(`
changelog:
  exclude:
    labels: [bug]
  categories:
    - title: New features
      labels: [feature]
      exclude:
        authors: [Renovate-Bot]
    - title: Everything else
      labels: ["*"]
      exclude:
        labels: [skip]
`), cfg))
		prs := append(prs, pr(4, "Skipped", "carol", "skip"))
		assert.Equal(t, `## What's Changed
### New features
* Add SSO login by @alice in #1
### Everything else
* Bump deps by @renovate-bot in #2
### Other Changes
* Skipped by @carol in #4

**Full Changelog**: https://example.com/owner/repo/compare/v1.0...v1.1
`, renderNotes(url, prs, cfg, notesExtras{}, "v1.0", "v1.1", false))
	})
}

func TestEscapeMarkdown(t *testing.T) {
	assert.Equal(t, `\[x\]\(http\:\/\/evil\) \<b\> \# \*a\* \_b\_ \!\[i\]\(u\)`, escapeMarkdown("[x](http://evil) <b> # *a* _b_ ![i](u)"))
	assert.Equal(t, "line one line two", escapeMarkdown("line one\nline two"))
}
