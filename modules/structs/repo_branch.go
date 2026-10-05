// Copyright 2016 The Gogs Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package structs

import (
	"time"
)

// ApprovalRule is a named approval requirement of a branch protection. All applicable rules must be satisfied to merge.
type ApprovalRule struct {
	// Name of the rule, unique within the branch protection
	Name string `json:"name"`
	// RequiredCount is the number of approvals needed, at least 1
	RequiredCount int64 `json:"required_count"`
	// Usernames of the users who can approve. Teams are the teams (of the owning organization) whose members can approve.
	// Both may be empty only with RequireCodeOwners, then any user with write access can approve.
	Usernames []string `json:"usernames"`
	Teams     []string `json:"teams"`
	// PathPatterns are globs separated by semicolons; the rule applies only if the pull request changes a matching file.
	// `*` does not match `/`, use `**`. Empty applies to every pull request.
	PathPatterns string `json:"path_patterns"`
	// RequireCodeOwners also requires that for every changed file with owners in CODEOWNERS of the target branch, one of its owners approved
	RequireCodeOwners bool `json:"require_code_owners"`
}

// Branch represents a repository branch
type Branch struct {
	Name                          string         `json:"name"`
	Commit                        *PayloadCommit `json:"commit"`
	Protected                     bool           `json:"protected"`
	RequiredApprovals             int64          `json:"required_approvals"`
	EnableStatusCheck             bool           `json:"enable_status_check"`
	StatusCheckContexts           []string       `json:"status_check_contexts"`
	UserCanPush                   bool           `json:"user_can_push"`
	UserCanMerge                  bool           `json:"user_can_merge"`
	EffectiveBranchProtectionName string         `json:"effective_branch_protection_name"`
}

// BranchProtection represents a branch protection for a repository
type BranchProtection struct {
	// Deprecated: true
	BranchName              string   `json:"branch_name"`
	RuleName                string   `json:"rule_name"`
	EnablePush              bool     `json:"enable_push"`
	EnablePushWhitelist     bool     `json:"enable_push_whitelist"`
	PushWhitelistUsernames  []string `json:"push_whitelist_usernames"`
	PushWhitelistTeams      []string `json:"push_whitelist_teams"`
	PushWhitelistDeployKeys bool     `json:"push_whitelist_deploy_keys"`
	// Public key IDs (`key_id` of a deploy key) of the deploy keys allowed to push. IDs which are not
	// write deploy keys of the repository are ignored.
	PushWhitelistDeployKeyIDs     []int64  `json:"push_whitelist_deploy_key_ids"`
	EnableMergeWhitelist          bool     `json:"enable_merge_whitelist"`
	MergeWhitelistUsernames       []string `json:"merge_whitelist_usernames"`
	MergeWhitelistTeams           []string `json:"merge_whitelist_teams"`
	EnableStatusCheck             bool     `json:"enable_status_check"`
	StatusCheckContexts           []string `json:"status_check_contexts"`
	RequiredApprovals             int64    `json:"required_approvals"`
	EnableApprovalsWhitelist      bool     `json:"enable_approvals_whitelist"`
	ApprovalsWhitelistUsernames   []string `json:"approvals_whitelist_username"`
	ApprovalsWhitelistTeams       []string `json:"approvals_whitelist_teams"`
	BlockOnRejectedReviews        bool     `json:"block_on_rejected_reviews"`
	BlockOnOfficialReviewRequests bool     `json:"block_on_official_review_requests"`
	BlockOnOutdatedBranch         bool     `json:"block_on_outdated_branch"`
	DismissStaleApprovals         bool     `json:"dismiss_stale_approvals"`
	IgnoreStaleApprovals          bool     `json:"ignore_stale_approvals"`
	RequireSignedCommits          bool     `json:"require_signed_commits"`
	ProtectedFilePatterns         string   `json:"protected_file_patterns"`
	UnprotectedFilePatterns       string   `json:"unprotected_file_patterns"`
	ApplyToAdmins                 bool     `json:"apply_to_admins"`

	// Block merging while code review conversations are unresolved, optionally not counting outdated ones.
	BlockOnUnresolvedConversations bool `json:"block_on_unresolved_conversations"`
	IgnoreOutdatedConversations    bool `json:"ignore_outdated_conversations"`
	// Block merging while tasks of the pull request are open.
	BlockOnIncompleteTasks bool `json:"block_on_incomplete_tasks"`

	// Users, teams and deploy keys (public key IDs) allowed to delete matching branches. Empty lists block everyone.
	// The default branch can never be deleted. IDs which are not write deploy keys of the repository are ignored.
	DeleteAllowlistUsernames    []string `json:"delete_allowlist_usernames"`
	DeleteAllowlistTeams        []string `json:"delete_allowlist_teams"`
	DeleteAllowlistDeployKeyIDs []int64  `json:"delete_allowlist_deploy_key_ids"`
	// Users, teams and deploy keys (public key IDs) allowed to force-push matching branches. Empty lists block everyone.
	// A force push must still pass the other checks of the rule. IDs which are not write deploy keys of the repository are ignored.
	ForcePushAllowlistUsernames    []string `json:"force_push_allowlist_usernames"`
	ForcePushAllowlistTeams        []string `json:"force_push_allowlist_teams"`
	ForcePushAllowlistDeployKeyIDs []int64  `json:"force_push_allowlist_deploy_key_ids"`
	// ApprovalRules are the named approval requirements.
	ApprovalRules []*ApprovalRule `json:"approval_rules"`
	// swagger:strfmt date-time
	Created time.Time `json:"created_at"`
	// swagger:strfmt date-time
	Updated time.Time `json:"updated_at"`
}

// CreateBranchProtectionOption options for creating a branch protection
type CreateBranchProtectionOption struct {
	// Deprecated: true
	BranchName              string   `json:"branch_name"`
	RuleName                string   `json:"rule_name"`
	EnablePush              bool     `json:"enable_push"`
	EnablePushWhitelist     bool     `json:"enable_push_whitelist"`
	PushWhitelistUsernames  []string `json:"push_whitelist_usernames"`
	PushWhitelistTeams      []string `json:"push_whitelist_teams"`
	PushWhitelistDeployKeys bool     `json:"push_whitelist_deploy_keys"`
	// Public key IDs (`key_id` of a deploy key) of the deploy keys allowed to push. IDs which are not
	// write deploy keys of the repository are ignored.
	PushWhitelistDeployKeyIDs     []int64  `json:"push_whitelist_deploy_key_ids"`
	EnableMergeWhitelist          bool     `json:"enable_merge_whitelist"`
	MergeWhitelistUsernames       []string `json:"merge_whitelist_usernames"`
	MergeWhitelistTeams           []string `json:"merge_whitelist_teams"`
	EnableStatusCheck             bool     `json:"enable_status_check"`
	StatusCheckContexts           []string `json:"status_check_contexts"`
	RequiredApprovals             int64    `json:"required_approvals"`
	EnableApprovalsWhitelist      bool     `json:"enable_approvals_whitelist"`
	ApprovalsWhitelistUsernames   []string `json:"approvals_whitelist_username"`
	ApprovalsWhitelistTeams       []string `json:"approvals_whitelist_teams"`
	BlockOnRejectedReviews        bool     `json:"block_on_rejected_reviews"`
	BlockOnOfficialReviewRequests bool     `json:"block_on_official_review_requests"`
	BlockOnOutdatedBranch         bool     `json:"block_on_outdated_branch"`
	DismissStaleApprovals         bool     `json:"dismiss_stale_approvals"`
	IgnoreStaleApprovals          bool     `json:"ignore_stale_approvals"`
	RequireSignedCommits          bool     `json:"require_signed_commits"`
	ProtectedFilePatterns         string   `json:"protected_file_patterns"`
	UnprotectedFilePatterns       string   `json:"unprotected_file_patterns"`
	ApplyToAdmins                 bool     `json:"apply_to_admins"`

	// Block merging while code review conversations are unresolved, optionally not counting outdated ones.
	BlockOnUnresolvedConversations bool `json:"block_on_unresolved_conversations"`
	IgnoreOutdatedConversations    bool `json:"ignore_outdated_conversations"`
	// Block merging while tasks of the pull request are open.
	BlockOnIncompleteTasks bool `json:"block_on_incomplete_tasks"`

	// Users, teams and deploy keys (public key IDs) allowed to delete matching branches. Empty lists block everyone.
	// The default branch can never be deleted. IDs which are not write deploy keys of the repository are ignored.
	DeleteAllowlistUsernames    []string `json:"delete_allowlist_usernames"`
	DeleteAllowlistTeams        []string `json:"delete_allowlist_teams"`
	DeleteAllowlistDeployKeyIDs []int64  `json:"delete_allowlist_deploy_key_ids"`
	// Users, teams and deploy keys (public key IDs) allowed to force-push matching branches. Empty lists block everyone.
	// A force push must still pass the other checks of the rule. IDs which are not write deploy keys of the repository are ignored.
	ForcePushAllowlistUsernames    []string `json:"force_push_allowlist_usernames"`
	ForcePushAllowlistTeams        []string `json:"force_push_allowlist_teams"`
	ForcePushAllowlistDeployKeyIDs []int64  `json:"force_push_allowlist_deploy_key_ids"`
	// ApprovalRules are the named approval requirements. On edit, null keeps them and an empty list removes them. At most 20.
	ApprovalRules []*ApprovalRule `json:"approval_rules"`
}

// EditBranchProtectionOption options for editing a branch protection
type EditBranchProtectionOption struct {
	EnablePush              *bool    `json:"enable_push"`
	EnablePushWhitelist     *bool    `json:"enable_push_whitelist"`
	PushWhitelistUsernames  []string `json:"push_whitelist_usernames"`
	PushWhitelistTeams      []string `json:"push_whitelist_teams"`
	PushWhitelistDeployKeys *bool    `json:"push_whitelist_deploy_keys"`
	// Public key IDs (`key_id` of a deploy key) of the deploy keys allowed to push. IDs which are not
	// write deploy keys of the repository are ignored.
	PushWhitelistDeployKeyIDs     []int64  `json:"push_whitelist_deploy_key_ids"`
	EnableMergeWhitelist          *bool    `json:"enable_merge_whitelist"`
	MergeWhitelistUsernames       []string `json:"merge_whitelist_usernames"`
	MergeWhitelistTeams           []string `json:"merge_whitelist_teams"`
	EnableStatusCheck             *bool    `json:"enable_status_check"`
	StatusCheckContexts           []string `json:"status_check_contexts"`
	RequiredApprovals             *int64   `json:"required_approvals"`
	EnableApprovalsWhitelist      *bool    `json:"enable_approvals_whitelist"`
	ApprovalsWhitelistUsernames   []string `json:"approvals_whitelist_username"`
	ApprovalsWhitelistTeams       []string `json:"approvals_whitelist_teams"`
	BlockOnRejectedReviews        *bool    `json:"block_on_rejected_reviews"`
	BlockOnOfficialReviewRequests *bool    `json:"block_on_official_review_requests"`
	BlockOnOutdatedBranch         *bool    `json:"block_on_outdated_branch"`
	DismissStaleApprovals         *bool    `json:"dismiss_stale_approvals"`
	IgnoreStaleApprovals          *bool    `json:"ignore_stale_approvals"`
	RequireSignedCommits          *bool    `json:"require_signed_commits"`
	ProtectedFilePatterns         *string  `json:"protected_file_patterns"`
	UnprotectedFilePatterns       *string  `json:"unprotected_file_patterns"`
	ApplyToAdmins                 *bool    `json:"apply_to_admins"`

	// Block merging while code review conversations are unresolved, optionally not counting outdated ones.
	BlockOnUnresolvedConversations *bool `json:"block_on_unresolved_conversations"`
	IgnoreOutdatedConversations    *bool `json:"ignore_outdated_conversations"`
	// Block merging while tasks of the pull request are open.
	BlockOnIncompleteTasks *bool `json:"block_on_incomplete_tasks"`

	// Users, teams and deploy keys (public key IDs) allowed to delete matching branches. Empty lists block everyone.
	// The default branch can never be deleted. IDs which are not write deploy keys of the repository are ignored.
	DeleteAllowlistUsernames    []string `json:"delete_allowlist_usernames"`
	DeleteAllowlistTeams        []string `json:"delete_allowlist_teams"`
	DeleteAllowlistDeployKeyIDs []int64  `json:"delete_allowlist_deploy_key_ids"`
	// Users, teams and deploy keys (public key IDs) allowed to force-push matching branches. Empty lists block everyone.
	// A force push must still pass the other checks of the rule. IDs which are not write deploy keys of the repository are ignored.
	ForcePushAllowlistUsernames    []string `json:"force_push_allowlist_usernames"`
	ForcePushAllowlistTeams        []string `json:"force_push_allowlist_teams"`
	ForcePushAllowlistDeployKeyIDs []int64  `json:"force_push_allowlist_deploy_key_ids"`
	// ApprovalRules are the named approval requirements. On edit, null keeps them and an empty list removes them. At most 20.
	ApprovalRules []*ApprovalRule `json:"approval_rules"`
}
