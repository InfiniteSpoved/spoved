// Copyright 2014 The Gogs Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package forms

import (
	"net/http"

	"forgejo.org/modules/structs"
	"forgejo.org/modules/web/middleware"
	"forgejo.org/services/context"

	"code.forgejo.org/go-chi/binding"
)

// AdminCreateUserForm form for admin to create user
type AdminCreateUserForm struct {
	LoginType          string `binding:"Required"`
	LoginName          string
	UserName           string `binding:"Required;Username;MaxSize(40)"`
	Email              string `binding:"Required;EmailForAdmin;MaxSize(254)"`
	Password           string `binding:"MaxSize(255)"`
	SendNotify         bool
	MustChangePassword bool
	Visibility         structs.VisibleType
}

// Validate validates form fields
func (f *AdminCreateUserForm) Validate(req *http.Request, errs binding.Errors) binding.Errors {
	ctx := context.GetValidateContext(req)
	return middleware.Validate(errs, ctx.Data, f, ctx.Locale)
}

// AdminEditUserForm form for admin to create user
type AdminEditUserForm struct {
	LoginType                 string `binding:"Required"`
	UserName                  string `binding:"Username;MaxSize(40)"`
	LoginName                 string
	FullName                  string `binding:"MaxSize(100)"`
	Email                     string `binding:"Required;EmailForAdmin;MaxSize(254)"`
	Password                  string `binding:"MaxSize(255)"`
	Website                   string `binding:"ValidSiteUrl;MaxSize(255)"`
	Location                  string `binding:"MaxSize(50)"`
	Language                  string `binding:"MaxSize(5)"`
	Pronouns                  string `binding:"MaxSize(50)"`
	MaxRepoCreation           int
	PersonalRepoSizeLimitMode string `binding:"In(,site,custom,unlimited)"`
	PersonalRepoSizeLimit     int64  // MiB, for the custom mode
	Active                    bool
	Admin                     bool
	Restricted                bool
	AllowGitHook              bool
	AllowImportLocal          bool
	AllowCreateOrganization   bool
	ProhibitLogin             bool
	Reset2FA                  bool `form:"reset_2fa"`
	Visibility                structs.VisibleType
	HideEmail                 bool `form:"hide_email"`
}

// Validate validates form fields
func (f *AdminEditUserForm) Validate(req *http.Request, errs binding.Errors) binding.Errors {
	ctx := context.GetValidateContext(req)
	return middleware.Validate(errs, ctx.Data, f, ctx.Locale)
}

// AdminDashboardForm form for admin dashboard operations
type AdminDashboardForm struct {
	Op   string `binding:"required"`
	From string
}

// Validate validates form fields
func (f *AdminDashboardForm) Validate(req *http.Request, errs binding.Errors) binding.Errors {
	ctx := context.GetValidateContext(req)
	return middleware.Validate(errs, ctx.Data, f, ctx.Locale)
}

// OrgCategoryForm form for creating or editing an organization category
type OrgCategoryForm struct {
	Name        string `binding:"Required;MaxSize(100)" locale:"admin.org_categories.name"`
	Description string `binding:"MaxSize(255)" locale:"admin.org_categories.description"`
	Color       string `binding:"Required;MaxSize(7)" locale:"admin.org_categories.color"`
	Position    int    `binding:"Range(0,1000000)" locale:"admin.org_categories.position"`
}

// Validate validates the fields
func (f *OrgCategoryForm) Validate(req *http.Request, errs binding.Errors) binding.Errors {
	ctx := context.GetValidateContext(req)
	return middleware.Validate(errs, ctx.Data, f, ctx.Locale)
}

// AnnouncementForm form for creating or editing an announcement
type AnnouncementForm struct {
	Message     string `binding:"Required;MaxSize(65535)" locale:"admin.announcements.message"`
	Type        string `binding:"Required;In(info,warning,critical)" locale:"admin.announcements.type"`
	Audience    string `binding:"Required;In(everyone,signed_in,admins)" locale:"admin.announcements.audience"`
	Dismissible bool
	StartTime   string `locale:"admin.announcements.start_time"`
	EndTime     string `locale:"admin.announcements.end_time"`
}

// Validate validates the fields
func (f *AnnouncementForm) Validate(req *http.Request, errs binding.Errors) binding.Errors {
	ctx := context.GetValidateContext(req)
	return middleware.Validate(errs, ctx.Data, f, ctx.Locale)
}
