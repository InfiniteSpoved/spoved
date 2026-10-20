// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forms

import (
	"net/http"

	"forgejo.org/modules/web/middleware"
	"forgejo.org/services/context"

	"code.forgejo.org/go-chi/binding"
)

// BranchWorkflowForm is the form of the organization and repository branch workflow pages.
type BranchWorkflowForm struct {
	DeleteAfterMerge string `binding:"Required;In(inherit,off,default-on,always)"`
	Enforce          bool   // organizations only
}

// Validate validates the fields
func (f *BranchWorkflowForm) Validate(req *http.Request, errs binding.Errors) binding.Errors {
	ctx := context.GetValidateContext(req)
	return middleware.Validate(errs, ctx.Data, f, ctx.Locale)
}

// BranchingModelForm is the branching model section of the organization and repository branch workflow pages.
type BranchingModelForm struct {
	Inherit           bool // repositories of organizations only
	DevelopmentBranch string
	ReleasePrefixes   string // separated by commas
	EnforceModel      bool   // organizations only
}

// Validate validates the fields
func (f *BranchingModelForm) Validate(req *http.Request, errs binding.Errors) binding.Errors {
	ctx := context.GetValidateContext(req)
	return middleware.Validate(errs, ctx.Data, f, ctx.Locale)
}
