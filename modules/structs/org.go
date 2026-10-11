// Copyright 2015 The Gogs Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package structs

import "time"

// Organization represents an organization
type Organization struct {
	ID                        int64  `json:"id"`
	Name                      string `json:"name"`
	FullName                  string `json:"full_name"`
	Email                     string `json:"email"`
	AvatarURL                 string `json:"avatar_url"`
	Description               string `json:"description"`
	Website                   string `json:"website"`
	Location                  string `json:"location"`
	Visibility                string `json:"visibility"`
	RepoAdminChangeTeamAccess bool   `json:"repo_admin_change_team_access"`
	// whether the organization is archived (read-only)
	Archived bool      `json:"archived"`
	Created  time.Time `json:"created"`
	// deprecated
	UserName string `json:"username"`

	// only organization owners may change the visibility of its repositories; includes the site floor
	RestrictRepoVisibilityChange bool `json:"restrict_repo_visibility_change"`
	// who may fork its private repositories: `all`, `org` (only into the organization) or `none` (site admins only); includes the site floor
	// enum: ["all", "org", "none"]
	PrivateForkPolicy string `json:"private_fork_policy"`

	// whether members must use two-factor authentication; only shown to the organization's owners and site administrators
	RequireTwoFactor *bool `json:"require_two_factor,omitempty"`
}

// OrganizationPermissions list different users permissions on an organization
type OrganizationPermissions struct {
	IsOwner             bool `json:"is_owner"`
	IsAdmin             bool `json:"is_admin"`
	CanWrite            bool `json:"can_write"`
	CanRead             bool `json:"can_read"`
	CanCreateRepository bool `json:"can_create_repository"`
}

// CreateOrgOption options for creating an organization
type CreateOrgOption struct {
	// required: true
	UserName    string `json:"username" binding:"Required;Username;MaxSize(40)"`
	FullName    string `json:"full_name" binding:"MaxSize(100)"`
	Email       string `json:"email" binding:"MaxSize(255)"`
	Description string `json:"description" binding:"MaxSize(255)"`
	Website     string `json:"website" binding:"ValidSiteUrl;MaxSize(255)"`
	Location    string `json:"location" binding:"MaxSize(50)"`
	// possible values are `public` (default), `limited` or `private`
	// enum: ["public", "limited", "private"]
	Visibility                string `json:"visibility" binding:"In(,public,limited,private)"`
	RepoAdminChangeTeamAccess bool   `json:"repo_admin_change_team_access"`
}

// EditOrgOption options for editing an organization
type EditOrgOption struct {
	FullName    *string `json:"full_name" binding:"MaxSize(100)"`
	Email       *string `json:"email" binding:"MaxSize(255)"`
	Description *string `json:"description" binding:"MaxSize(255)"`
	Website     *string `json:"website" binding:"ValidSiteUrl;MaxSize(255)"`
	Location    *string `json:"location" binding:"MaxSize(50)"`
	// possible values are `public`, `limited` or `private`
	// enum: ["public", "limited", "private"]
	Visibility                *string `json:"visibility" binding:"In(public,limited,private)"`
	RepoAdminChangeTeamAccess *bool   `json:"repo_admin_change_team_access"`

	// only organization owners may change the visibility of its repositories; can't be false while the site requires it
	RestrictRepoVisibilityChange *bool `json:"restrict_repo_visibility_change"`
	// who may fork its private repositories: `all`, `org` (only into the organization) or `none` (site admins only); can't be weaker than the site requires
	// enum: ["all", "org", "none"]
	PrivateForkPolicy *string `json:"private_fork_policy" binding:"In(all,org,none)"`
	// require two-factor authentication for members: those without it lose access to the organization's private repositories at once (list them with `filter=2fa_disabled` on the members endpoint); the doer needs it too unless they are a site administrator
	RequireTwoFactor *bool `json:"require_two_factor"`
}

// RenameOrgOption options when renaming an organization
type RenameOrgOption struct {
	// New username for this org. This name cannot be in use yet by any other user.
	//
	// required: true
	// unique: true
	NewName string `json:"new_name" binding:"Required"`
}

// ArchiveOrgOption options for archiving an organization
type ArchiveOrgOption struct {
	// also remove all members except owners
	RemoveMembers bool `json:"remove_members"`
}
