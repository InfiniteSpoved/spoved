// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package git

import (
	"context"
	"sort"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/optional"

	"github.com/gobwas/glob"
	"xorm.io/builder"
)

type ProtectedBranchRules []*ProtectedBranch

func (rules ProtectedBranchRules) GetFirstMatched(branchName string) *ProtectedBranch {
	for _, rule := range rules {
		if rule.Match(branchName) {
			return rule
		}
	}
	return nil
}

// GetMatched returns the first matching repository rule and the first matching organization rule, in that order.
// The rules must be sorted. A ref has to pass every returned rule.
func (rules ProtectedBranchRules) GetMatched(branchName string) ProtectedBranchRules {
	var matched ProtectedBranchRules
	for _, rule := range rules {
		if rule.Match(branchName) && (len(matched) == 0 || len(matched) == 1 && matched[0].OrgID == 0 && rule.OrgID != 0) {
			matched = append(matched, rule)
		}
	}
	return matched
}

func (rules ProtectedBranchRules) sort() {
	sort.Slice(rules, func(i, j int) bool {
		if (rules[i].OrgID == 0) != (rules[j].OrgID == 0) {
			return rules[i].OrgID == 0 // repository rules come before organization rules
		}
		rules[i].loadGlob()
		rules[j].loadGlob()
		if rules[i].isPlainName != rules[j].isPlainName {
			return rules[i].isPlainName // plain name comes first, so plain name means "less"
		}
		return rules[i].CreatedUnix < rules[j].CreatedUnix
	})
}

// FindRepoProtectedBranchRules load all repository's protected rules
func FindRepoProtectedBranchRules(ctx context.Context, repoID int64) (ProtectedBranchRules, error) {
	var rules ProtectedBranchRules
	err := db.GetEngine(ctx).Where("repo_id = ?", repoID).Asc("created_unix").Find(&rules)
	if err != nil {
		return nil, err
	}
	rules.sort() // to make non-glob rules have higher priority, and for same glob/non-glob rules, first created rules have higher priority
	return rules, nil
}

// FindProtectedBranchRulesForRepo loads the repository's rules followed by the organization rules that apply to it.
func FindProtectedBranchRulesForRepo(ctx context.Context, repo *repo_model.Repository) (ProtectedBranchRules, error) {
	rules, err := findProtectedBranchRulesForRepo(ctx, repo.ID)
	if err != nil {
		return nil, err
	}
	for _, rule := range rules {
		if rule.OrgID != 0 {
			rule.Repo = repo
		}
	}
	return rules, nil
}

// findProtectedBranchRulesForRepo reads the owner of the repository at query time, so a new repository is
// protected at once and a transferred one gets the rules of its new owner.
func findProtectedBranchRulesForRepo(ctx context.Context, repoID int64) (ProtectedBranchRules, error) {
	var rules ProtectedBranchRules
	err := db.GetEngine(ctx).Where(builder.Eq{"repo_id": repoID}.Or(builder.And(
		builder.Eq{"repo_id": 0},
		builder.In("org_id", builder.Select("owner_id").From("repository").Where(builder.Eq{"id": repoID})),
		builder.Eq{"apply_to_selected_repos": false}.Or(
			builder.In("id", builder.Select("protected_branch_id").From("protected_branch_repo").Where(builder.Eq{"repo_id": repoID})),
		),
	))).Asc("created_unix").Find(&rules)
	if err != nil {
		return nil, err
	}
	rules.sort()
	return rules, nil
}

// GetMatchedProtectedBranchRules returns the repository rule and the organization rule that match the branch.
// Enforcement must check every returned rule.
func GetMatchedProtectedBranchRules(ctx context.Context, repoID int64, branchName string) (ProtectedBranchRules, error) {
	rules, err := findProtectedBranchRulesForRepo(ctx, repoID)
	if err != nil {
		return nil, err
	}
	rules = rules.GetMatched(branchName)
	if len(rules) > 0 && rules[len(rules)-1].OrgID != 0 {
		repo, err := repo_model.GetRepositoryByID(ctx, repoID)
		if err != nil {
			return nil, err
		}
		rules[len(rules)-1].Repo = repo
	}
	return rules, nil
}

// FindAllMatchedBranches find all matched branches
func FindAllMatchedBranches(ctx context.Context, repoID int64, ruleName string) ([]string, error) {
	results := make([]string, 0, 10)
	for page := 1; ; page++ {
		branchNames, err := FindBranchNames(ctx, FindBranchOptions{
			ListOptions: db.ListOptions{
				PageSize: 100,
				Page:     page,
			},
			RepoID:          repoID,
			IsDeletedBranch: optional.Some(false),
		})
		if err != nil {
			return nil, err
		}
		rule := glob.MustCompile(ruleName)

		for _, branch := range branchNames {
			if rule.Match(branch) {
				results = append(results, branch)
			}
		}
		if len(branchNames) < 100 {
			break
		}
	}

	return results, nil
}

// GetFirstMatchProtectedBranchRule returns the matching repository rule, or the matching organization rule if
// no repository rule matches. Use it for display only, enforcement must use GetMatchedProtectedBranchRules.
func GetFirstMatchProtectedBranchRule(ctx context.Context, repoID int64, branchName string) (*ProtectedBranch, error) {
	rules, err := GetMatchedProtectedBranchRules(ctx, repoID, branchName)
	if err != nil || len(rules) == 0 {
		return nil, err
	}
	return rules[0], nil
}

// IsBranchProtected checks if branch is protected
func IsBranchProtected(ctx context.Context, repoID int64, branchName string) (bool, error) {
	rule, err := GetFirstMatchProtectedBranchRule(ctx, repoID, branchName)
	if err != nil {
		return false, err
	}
	return rule != nil, nil
}
