// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package issue

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"path"
	"slices"
	"strings"

	"forgejo.org/models/organization"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	"forgejo.org/modules/gitrepo"
	"forgejo.org/modules/issue/template"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	api "forgejo.org/modules/structs"

	"go.yaml.in/yaml/v3"
)

// templateDirCandidates issue templates directory
var templateDirCandidates = []string{
	"ISSUE_TEMPLATE",
	"issue_template",
	".forgejo/ISSUE_TEMPLATE",
	".forgejo/issue_template",
	".gitea/ISSUE_TEMPLATE",
	".gitea/issue_template",
	".github/ISSUE_TEMPLATE",
	".github/issue_template",
	".gitlab/ISSUE_TEMPLATE",
	".gitlab/issue_template",
	"docs/ISSUE_TEMPLATE",
	"docs/issue_template",
}

var templateConfigCandidates = []string{
	".forgejo/ISSUE_TEMPLATE/config",
	".forgejo/issue_template/config",
	".gitea/ISSUE_TEMPLATE/config",
	".gitea/issue_template/config",
	".github/ISSUE_TEMPLATE/config",
	".github/issue_template/config",
	"docs/ISSUE_TEMPLATE/config",
	"docs/issue_template/config",
}

// generateIssueTemplateLocations generates all the file paths where we
// look for an issue template, e.g. ".forgejo/ISSUE_TEMPLATE.md".
func generateIssueTemplateLocations() []string {
	var result []string
	prefixes := []string{"", ".forgejo/", ".gitea/", ".github/", "docs/"}
	filenames := []string{"ISSUE_TEMPLATE", "issue_template"}
	extensions := []string{".md", ".yaml", ".yml"}

	for _, prefix := range prefixes {
		for _, filename := range filenames {
			for _, extension := range extensions {
				result = append(result, prefix+filename+extension)
			}
		}
	}

	return result
}

// IssueTemplateCandidates are the single-file issue template paths.
var IssueTemplateCandidates = generateIssueTemplateLocations()

// IsTemplateDirFile reports whether name is a template file directly inside an issue template directory.
// path.Dir cleans name, so "ISSUE_TEMPLATE/../x.md" is rejected.
func IsTemplateDirFile(name string) bool {
	return slices.Contains(templateDirCandidates, path.Dir(name)) && template.CouldBe(path.Base(name))
}

// TemplateCommit returns the default-branch commit of the first template source for which has is true:
// the repo itself, then the owner's ".forgejo" repo, then [repository] DEFAULT_TEMPLATES_REPO.
// own reports whether the commit is the repo's own. closer must always be called once the commit is no longer used.
// Fallback files are read on the server: callers must only read fixed template paths from them.
func TemplateCommit(ctx context.Context, repo *repo_model.Repository, gitRepo *git.Repository, has func(*git.Commit) bool) (commit *git.Commit, own bool, closer func()) {
	if gitRepo != nil && !repo.IsEmpty {
		if c, err := gitRepo.GetBranchCommit(repo.DefaultBranch); err == nil && has(c) {
			return c, true, func() {}
		}
	}

	var sources []*repo_model.Repository
	if r, err := repo_model.GetRepositoryByName(ctx, repo.OwnerID, ".forgejo"); err == nil {
		sources = append(sources, r)
	}
	if r := defaultTemplatesRepo(ctx); r != nil {
		sources = append(sources, r)
	}

	seen := map[int64]bool{repo.ID: true}
	for _, src := range sources {
		if seen[src.ID] || src.IsEmpty {
			continue
		}
		seen[src.ID] = true
		srcGitRepo, err := gitrepo.OpenRepository(ctx, src)
		if err != nil {
			log.Error("OpenRepository(%s): %v", src.FullName(), err)
			continue
		}
		if c, err := srcGitRepo.GetBranchCommit(src.DefaultBranch); err == nil && has(c) {
			return c, false, func() { srcGitRepo.Close() }
		}
		srcGitRepo.Close()
	}
	return nil, false, func() {}
}

// defaultTemplatesRepo returns the DEFAULT_TEMPLATES_REPO if it is owned by a site admin, or by an organization
// with a site admin in its Owners team, so that no other user can supply templates to the whole instance.
func defaultTemplatesRepo(ctx context.Context) *repo_model.Repository {
	name := setting.Repository.DefaultTemplatesRepo
	if name == "" {
		return nil
	}
	ownerName, repoName, _ := strings.Cut(name, "/")
	r, err := repo_model.GetRepositoryByOwnerAndName(ctx, ownerName, repoName)
	if err == nil {
		err = r.LoadOwner(ctx)
	}
	if err != nil {
		log.Warn("[repository] DEFAULT_TEMPLATES_REPO %q is ignored: %v", name, err)
		return nil
	}

	trusted := r.Owner.IsAdmin
	if r.Owner.IsOrganization() {
		if team, err := organization.GetOwnerTeam(ctx, r.OwnerID); err == nil && team.LoadMembers(ctx) == nil {
			trusted = slices.ContainsFunc(team.Members, func(u *user_model.User) bool { return u.IsAdmin })
		}
	}
	if !trusted {
		log.Warn("[repository] DEFAULT_TEMPLATES_REPO %q is ignored: its owner is neither a site admin nor an organization with a site admin among its owners", name)
		return nil
	}
	return r
}

func GetDefaultTemplateConfig() api.IssueConfig {
	return api.IssueConfig{
		BlankIssuesEnabled: true,
		ContactLinks:       make([]api.IssueConfigContactLink, 0),
	}
}

// GetTemplateConfig loads the given issue config file.
// It never returns a nil config.
func GetTemplateConfig(gitRepo *git.Repository, path string, commit *git.Commit) (api.IssueConfig, error) {
	if gitRepo == nil {
		return GetDefaultTemplateConfig(), nil
	}

	treeEntry, err := commit.GetTreeEntryByPath(path)
	if err != nil {
		return GetDefaultTemplateConfig(), err
	}

	reader, err := treeEntry.Blob().DataAsync()
	if err != nil {
		log.Debug("DataAsync: %v", err)
		return GetDefaultTemplateConfig(), nil
	}

	defer reader.Close()

	configContent, err := io.ReadAll(reader)
	if err != nil {
		return GetDefaultTemplateConfig(), err
	}

	issueConfig := GetDefaultTemplateConfig()
	if err := yaml.Unmarshal(configContent, &issueConfig); err != nil {
		return GetDefaultTemplateConfig(), err
	}

	for pos, link := range issueConfig.ContactLinks {
		if link.Name == "" {
			return GetDefaultTemplateConfig(), fmt.Errorf("contact_link at position %d is missing name key", pos+1)
		}

		if link.URL == "" {
			return GetDefaultTemplateConfig(), fmt.Errorf("contact_link at position %d is missing url key", pos+1)
		}

		if link.About == "" {
			return GetDefaultTemplateConfig(), fmt.Errorf("contact_link at position %d is missing about key", pos+1)
		}

		_, err = url.ParseRequestURI(link.URL)
		if err != nil {
			return GetDefaultTemplateConfig(), fmt.Errorf("%s is not a valid URL", link.URL)
		}
	}

	return issueConfig, nil
}

// IsTemplateConfig returns if the given path is a issue config file.
func IsTemplateConfig(path string) bool {
	for _, configName := range templateConfigCandidates {
		if path == configName+".yaml" || path == configName+".yml" {
			return true
		}
	}
	return false
}

// GetTemplatesFromDefaultBranch checks for issue templates in the default branch of the repo or its fallback source,
// returns valid templates and the errors of invalid template files.
func GetTemplatesFromDefaultBranch(ctx context.Context, repo *repo_model.Repository, gitRepo *git.Repository) ([]*api.IssueTemplate, map[string]error) {
	commit, _, closer := TemplateCommit(ctx, repo, gitRepo, HasIssueTemplates)
	defer closer()
	if commit == nil {
		return nil, map[string]error{}
	}
	return templatesFromCommit(commit)
}

// HasIssueTemplates reports whether commit has issue templates, in a template directory or as a single file.
func HasIssueTemplates(commit *git.Commit) bool {
	if templates, errs := templatesFromCommit(commit); len(templates)+len(errs) > 0 {
		return true
	}
	return slices.ContainsFunc(IssueTemplateCandidates, func(name string) bool {
		ok, _ := commit.HasFile(name)
		return ok
	})
}

func templatesFromCommit(commit *git.Commit) ([]*api.IssueTemplate, map[string]error) {
	var issueTemplates []*api.IssueTemplate
	invalidFiles := map[string]error{}
	for _, dirName := range templateDirCandidates {
		tree, err := commit.SubTree(dirName)
		if err != nil {
			log.Debug("get sub tree of %s: %v", dirName, err)
			continue
		}
		entries, err := tree.ListEntries()
		if err != nil {
			log.Debug("list entries in %s: %v", dirName, err)
			return issueTemplates, invalidFiles
		}
		for _, entry := range entries {
			if !template.CouldBe(entry.Name()) {
				continue
			}
			fullName := path.Join(dirName, entry.Name())
			if it, err := template.UnmarshalFromEntry(entry, dirName); err != nil {
				invalidFiles[fullName] = err
			} else {
				if !strings.HasPrefix(it.Ref, "refs/") { // Assume that the ref intended is always a branch - for tags users should use refs/tags/<ref>
					it.Ref = git.BranchPrefix + it.Ref
				}
				issueTemplates = append(issueTemplates, it)
			}
		}
	}
	return issueTemplates, invalidFiles
}

// GetTemplateConfigFromDefaultBranch returns the issue config for this repo, or of its fallback source.
// It never returns a nil config.
func GetTemplateConfigFromDefaultBranch(ctx context.Context, repo *repo_model.Repository, gitRepo *git.Repository) (api.IssueConfig, error) {
	commit, _, closer := TemplateCommit(ctx, repo, gitRepo, func(c *git.Commit) bool { return templateConfigPath(c) != "" })
	defer closer()
	if commit == nil {
		return GetDefaultTemplateConfig(), nil
	}
	return GetTemplateConfig(gitRepo, templateConfigPath(commit), commit)
}

// templateConfigPath returns the path of the issue config file in commit, or "".
func templateConfigPath(commit *git.Commit) string {
	for _, configName := range templateConfigCandidates {
		for _, ext := range []string{".yaml", ".yml"} {
			if _, err := commit.GetTreeEntryByPath(configName + ext); err == nil {
				return configName + ext
			}
		}
	}
	return ""
}

func HasTemplatesOrContactLinks(ctx context.Context, repo *repo_model.Repository, gitRepo *git.Repository) bool {
	ret, _ := GetTemplatesFromDefaultBranch(ctx, repo, gitRepo)
	if len(ret) > 0 {
		return true
	}

	issueConfig, _ := GetTemplateConfigFromDefaultBranch(ctx, repo, gitRepo)
	return len(issueConfig.ContactLinks) > 0
}
