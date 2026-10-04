// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package release

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/git"
	"forgejo.org/modules/log"
	"forgejo.org/modules/util"

	"go.yaml.in/yaml/v3"
	"xorm.io/builder"
)

const (
	maxNotesCommits    = 1000
	maxNotesConfigSize = 64 << 10
)

var notesConfigPaths = []string{".forgejo/release.yml", ".github/release.yml"}

type GenerateNotesOptions struct {
	TagName         string
	Target          string
	PreviousTagName string
	ConfigFilePath  string
}

type notesConfig struct {
	Changelog struct {
		Exclude struct {
			Labels  []string `yaml:"labels"`
			Authors []string `yaml:"authors"`
		} `yaml:"exclude"`
		Categories []struct {
			Title  string   `yaml:"title"`
			Labels []string `yaml:"labels"`
		} `yaml:"categories"`
	} `yaml:"changelog"`
}

// GenerateReleaseNotes lists the pull requests merged between the previous tag and the given tag (or target) as Markdown.
func GenerateReleaseNotes(ctx context.Context, repo *repo_model.Repository, gitRepo *git.Repository, opts GenerateNotesOptions) (string, error) {
	var end *git.Commit
	var err error
	if gitRepo.IsTagExist(opts.TagName) {
		end, err = gitRepo.GetTagCommit(opts.TagName)
	} else {
		if opts.Target == "" {
			opts.Target = repo.DefaultBranch
		}
		end, err = gitRepo.GetCommit(opts.Target)
	}
	if err != nil {
		return "", err
	}

	prevTag := opts.PreviousTagName
	if prevTag == "" {
		if prevTag, err = findPreviousTag(gitRepo, end.ID.String(), opts.TagName); err != nil {
			return "", err
		}
	}
	var prev *git.Commit
	if prevTag != "" {
		if prev, err = gitRepo.GetTagCommit(prevTag); err != nil {
			return "", err
		}
	}

	commits, err := gitRepo.CommitsBetweenLimit(end, prev, maxNotesCommits, 0)
	if err != nil {
		return "", err
	}
	shas := make([]string, 0, len(commits))
	for _, c := range commits {
		shas = append(shas, c.ID.String())
	}

	var prs issues_model.PullRequestList
	if len(shas) > 0 {
		if err := db.GetEngine(ctx).Where(builder.Eq{"base_repo_id": repo.ID, "has_merged": true}.And(builder.In("merged_commit_id", shas))).Find(&prs); err != nil {
			return "", err
		}
	}
	issues, err := prs.LoadIssues(ctx)
	if err != nil {
		return "", err
	}
	if err := issues.LoadPosters(ctx); err != nil {
		return "", err
	}
	if err := issues.LoadLabels(ctx); err != nil {
		return "", err
	}
	slices.SortFunc(prs, func(a, b *issues_model.PullRequest) int { return cmp.Compare(a.MergedUnix, b.MergedUnix) })

	cfg, err := loadNotesConfig(end, opts.ConfigFilePath)
	if err != nil {
		return "", err
	}
	return renderNotes(repo.HTMLURL(), prs, cfg, prevTag, opts.TagName, len(commits) == maxNotesCommits), nil
}

// findPreviousTag returns the nearest tag reachable from endSHA other than tagName, or "" if there is none.
func findPreviousTag(gitRepo *git.Repository, endSHA, tagName string) (string, error) {
	stdout, _, err := git.NewCommand(gitRepo.Ctx, "describe", "--tags", "--abbrev=0").
		AddOptionValues("--exclude", tagName).
		AddDynamicArguments(endSHA).
		RunStdString(&git.RunOpts{Dir: gitRepo.Path})
	if err != nil {
		if strings.Contains(err.Error(), "No names found") || strings.Contains(err.Error(), "No tags can describe") {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(stdout), nil
}

func loadNotesConfig(commit *git.Commit, path string) (*notesConfig, error) {
	paths := notesConfigPaths
	if path != "" {
		paths = []string{path}
	}
	for _, p := range paths {
		content, err := commit.GetFileContent(p, maxNotesConfigSize)
		if git.IsErrNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		cfg := &notesConfig{}
		if err := yaml.Unmarshal([]byte(content), cfg); err != nil {
			log.Debug("invalid release notes configuration %q: %v", p, err)
			return nil, util.NewInvalidArgumentErrorf("invalid release notes configuration")
		}
		return cfg, nil
	}
	if path != "" {
		return nil, util.NewInvalidArgumentErrorf("release notes configuration file not found")
	}
	return nil, nil
}

var markdownPunct = regexp.MustCompile("[!-/:-@\\[-`{-~]")

// escapeMarkdown backslash-escapes all ASCII punctuation, so the text renders literally.
func escapeMarkdown(s string) string {
	return markdownPunct.ReplaceAllString(strings.NewReplacer("\r", " ", "\n", " ").Replace(s), `\$0`)
}

func renderNotes(repoURL string, prs issues_model.PullRequestList, cfg *notesConfig, prevTag, tagName string, truncated bool) string {
	type category struct {
		title  string
		labels []string
		lines  []string
	}
	var categories []*category
	if cfg != nil {
		for _, c := range cfg.Changelog.Categories {
			categories = append(categories, &category{title: c.Title, labels: c.Labels})
		}
	}
	other := &category{title: "Other Changes", labels: []string{"*"}}
	categories = append(categories, other)

	for _, pr := range prs {
		issue := pr.Issue
		labels := make([]string, 0, len(issue.Labels))
		for _, l := range issue.Labels {
			labels = append(labels, l.Name)
		}
		if cfg != nil && (slices.ContainsFunc(cfg.Changelog.Exclude.Authors, func(a string) bool { return strings.EqualFold(a, issue.Poster.Name) }) ||
			slices.ContainsFunc(labels, func(l string) bool { return slices.Contains(cfg.Changelog.Exclude.Labels, l) })) {
			continue
		}
		line := fmt.Sprintf("* %s by @%s in #%d", escapeMarkdown(issue.Title), issue.Poster.Name, issue.Index)
		for _, c := range categories {
			if slices.Contains(c.labels, "*") || slices.ContainsFunc(labels, func(l string) bool { return slices.Contains(c.labels, l) }) {
				c.lines = append(c.lines, line)
				break
			}
		}
	}

	var sb strings.Builder
	sb.WriteString("## What's Changed\n")
	for _, c := range categories {
		if len(c.lines) == 0 {
			continue
		}
		if len(categories) > 1 {
			fmt.Fprintf(&sb, "### %s\n", escapeMarkdown(c.title))
		}
		sb.WriteString(strings.Join(c.lines, "\n") + "\n")
	}
	if truncated {
		fmt.Fprintf(&sb, "\n_Only the latest %d commits were scanned._\n", maxNotesCommits)
	}
	if prevTag != "" {
		fmt.Fprintf(&sb, "\n**Full Changelog**: %s/compare/%s...%s\n", repoURL, util.PathEscapeSegments(prevTag), util.PathEscapeSegments(tagName))
	} else {
		fmt.Fprintf(&sb, "\n**Full Changelog**: %s/commits/tag/%s\n", repoURL, util.PathEscapeSegments(tagName))
	}
	return sb.String()
}
