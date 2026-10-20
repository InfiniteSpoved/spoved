// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package structs

// OrgBranchWorkflow represents the branch workflow settings of an organization, and the body to change them
type OrgBranchWorkflow struct {
	// DeleteBranchAfterMerge is one of inherit, off, default-on and always
	DeleteBranchAfterMerge string `json:"delete_branch_after_merge"`
	// EnforceDeleteBranchAfterMerge locks the value for all repositories of the organization
	EnforceDeleteBranchAfterMerge bool `json:"enforce_delete_branch_after_merge"`
}

// RepoBranchWorkflow represents the branch workflow settings of a repository
type RepoBranchWorkflow struct {
	// DeleteBranchAfterMerge is the repository's own value: inherit, off, default-on or always
	DeleteBranchAfterMerge string `json:"delete_branch_after_merge"`
	// EffectiveDeleteBranchAfterMerge is the value that applies: off, default-on or always
	EffectiveDeleteBranchAfterMerge string `json:"effective_delete_branch_after_merge"`
	// Source tells where the effective value comes from: instance, org or repo
	Source string `json:"source"`
	// Locked is true when the organization enforces the value
	Locked bool `json:"locked"`
	// LockedBy is the organization that enforces the value
	LockedBy string `json:"locked_by,omitempty"`
}

// EditRepoBranchWorkflowOption options to change the branch workflow settings of a repository
type EditRepoBranchWorkflowOption struct {
	// DeleteBranchAfterMerge is one of inherit, off, default-on and always
	// required: true
	DeleteBranchAfterMerge string `json:"delete_branch_after_merge" binding:"Required"`
}

// BranchingModel names the development branch and the release branch prefixes
type BranchingModel struct {
	// DevelopmentBranch is where every chain of release branches ends; empty is the repository's default branch
	DevelopmentBranch string `json:"development_branch"`
	// ReleasePrefixes start the names of release branches; the rest of the name is read as a version
	ReleasePrefixes []string `json:"release_prefixes"`
}

// OrgBranchingModel represents the branching model of an organization, and the body to change it
type OrgBranchingModel struct {
	// DevelopmentBranch is where every chain of release branches ends; empty is each repository's default branch
	DevelopmentBranch string `json:"development_branch"`
	// ReleasePrefixes start the names of release branches; empty removes the branching model
	ReleasePrefixes []string `json:"release_prefixes"`
	// Enforce locks the branching model for all repositories of the organization; it needs a release prefix
	Enforce bool `json:"enforce"`
}

// RepoBranchingModel represents the branching model of a repository and what it makes of the repository's branches
type RepoBranchingModel struct {
	// Inherit is true when the repository has no branching model of its own
	Inherit bool `json:"inherit"`
	// Own is the repository's own branching model, absent when it inherits
	Own *BranchingModel `json:"own,omitempty"`
	// Effective is the branching model that applies; its development branch is resolved to a branch name
	Effective BranchingModel `json:"effective"`
	// Source tells where the effective value comes from: instance (no branching model), org or repo
	Source string `json:"source"`
	// Locked is true when the organization enforces the branching model
	Locked bool `json:"locked"`
	// LockedBy is the organization that enforces the branching model
	LockedBy string `json:"locked_by,omitempty"`
	// ReleaseBranches are the release branches in version order
	ReleaseBranches []string `json:"release_branches"`
	// IgnoredBranches have a release prefix but no version after it
	IgnoredBranches []string `json:"ignored_branches"`
}

// EditRepoBranchingModelOption options to change the branching model of a repository
type EditRepoBranchingModelOption struct {
	// Inherit removes the repository's own branching model; the other fields are then ignored
	Inherit           bool     `json:"inherit"`
	DevelopmentBranch string   `json:"development_branch"`
	ReleasePrefixes   []string `json:"release_prefixes"`
}
