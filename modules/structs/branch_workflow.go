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
