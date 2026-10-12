// Copyright 2014 The Gogs Authors. All rights reserved.
// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package forms

import (
	"net/http"

	"forgejo.org/modules/structs"
	"forgejo.org/modules/web/middleware"
	"forgejo.org/services/context"

	"code.forgejo.org/go-chi/binding"
)

// ________                            .__                __  .__
// \_____  \_______  _________    ____ |__|____________ _/  |_|__| ____   ____
//  /   |   \_  __ \/ ___\__  \  /    \|  \___   /\__  \\   __\  |/  _ \ /    \
// /    |    \  | \/ /_/  > __ \|   |  \  |/    /  / __ \|  | |  (  <_> )   |  \
// \_______  /__|  \___  (____  /___|  /__/_____ \(____  /__| |__|\____/|___|  /
//         \/     /_____/     \/     \/         \/     \/                    \/

// CreateOrgForm form for creating organization
type CreateOrgForm struct {
	OrgName                   string `binding:"Required;Username;MaxSize(40)" locale:"org.org_name_holder"`
	Visibility                structs.VisibleType
	RepoAdminChangeTeamAccess bool
}

// Validate validates the fields
func (f *CreateOrgForm) Validate(req *http.Request, errs binding.Errors) binding.Errors {
	ctx := context.GetValidateContext(req)
	return middleware.Validate(errs, ctx.Data, f, ctx.Locale)
}

// RepoGroupForm is the form to create a project or change its name and description
type RepoGroupForm struct {
	Name        string `binding:"Required;AlphaDashDot;MaxSize(100)" locale:"repo_group.name" preprocess:"TrimSpace"`
	Description string `binding:"MaxSize(2048)"`
}

// Validate validates the fields
func (f *RepoGroupForm) Validate(req *http.Request, errs binding.Errors) binding.Errors {
	ctx := context.GetValidateContext(req)
	return middleware.Validate(errs, ctx.Data, f, ctx.Locale)
}

// UpdateOrgSettingForm form for updating organization settings
type UpdateOrgSettingForm struct {
	Name                      string `binding:"Required;Username;MaxSize(40)" locale:"org.org_name_holder"`
	FullName                  string `binding:"MaxSize(100)"`
	Email                     string `binding:"MaxSize(255)"`
	Description               string `binding:"MaxSize(255)"`
	Website                   string `binding:"ValidSiteUrl;MaxSize(255)"`
	Location                  string `binding:"MaxSize(50)"`
	Visibility                structs.VisibleType
	MaxRepoCreation           int
	RepoAdminChangeTeamAccess bool
	RequireTwoFactor          bool
	RequireTwoFactorConfirm   bool
	RequireTwoFactorShown     bool // the form has the require_two_factor checkbox: a post without it keeps the setting

	// repository policies
	RestrictRepoVisibilityChange bool
	PrivateForkPolicy            int `binding:"Range(0,2)"`

	CategoryID    int64
	CategoryShown bool // the form has the category picker: a post without it keeps the category
}

// Validate validates the fields
func (f *UpdateOrgSettingForm) Validate(req *http.Request, errs binding.Errors) binding.Errors {
	ctx := context.GetValidateContext(req)
	return middleware.Validate(errs, ctx.Data, f, ctx.Locale)
}

// ___________
// \__    ___/___ _____    _____
//   |    |_/ __ \\__  \  /     \
//   |    |\  ___/ / __ \|  Y Y  \
//   |____| \___  >____  /__|_|  /
//              \/     \/      \/

// CreateTeamForm form for creating team
type CreateTeamForm struct {
	TeamName         string `binding:"Required;AlphaDashDot;MaxSize(255)"`
	Description      string `binding:"MaxSize(255)"`
	Permission       string
	RepoAccess       string
	CanCreateOrgRepo bool
}

// Validate validates the fields
func (f *CreateTeamForm) Validate(req *http.Request, errs binding.Errors) binding.Errors {
	ctx := context.GetValidateContext(req)
	return middleware.Validate(errs, ctx.Data, f, ctx.Locale)
}

// UserGroupForm form for creating or editing a user group
type UserGroupForm struct {
	Name                     string `binding:"Required;AlphaDashDot;MaxSize(255)" locale:"admin.user_groups.name"`
	Description              string `binding:"MaxSize(255)" locale:"admin.user_groups.description"`
	GrantsSiteAdmin          bool
	GrantsCreateOrganization bool
	GrantsAuditor            bool
	GrantsOrgAdmin           bool
	// ConfirmRoles lists, comma separated, the roles the admin was warned members lose.
	ConfirmRoles string
}

// Validate validates the fields
func (f *UserGroupForm) Validate(req *http.Request, errs binding.Errors) binding.Errors {
	ctx := context.GetValidateContext(req)
	return middleware.Validate(errs, ctx.Data, f, ctx.Locale)
}
