// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package forms

import (
	"net/http"

	"forgejo.org/modules/web/middleware"
	"forgejo.org/services/context"

	"code.forgejo.org/go-chi/binding"
)

// ProtectTagForm form for changing protected tag settings
type ProtectTagForm struct {
	NamePattern    string `binding:"Required;GlobOrRegexPattern"`
	AllowlistUsers string
	AllowlistTeams string
	// public key IDs of the allowed deploy keys
	AllowlistDeployKeyIDs string `form:"allowlist_deploy_key_ids"`
}

// Validate validates the fields
func (f *ProtectTagForm) Validate(req *http.Request, errs binding.Errors) binding.Errors {
	ctx := context.GetValidateContext(req)
	return middleware.Validate(errs, ctx.Data, f, ctx.Locale)
}

// PushRuleForm is the repository push rule form. git_model.PushRule.Compile validates it.
type PushRuleForm struct {
	CommitMessageRegex         string
	CommitMessageBannedRegex   string
	BranchNameRegex            string
	RequireVerifiedAuthorEmail bool
	MaxFileSizeMB              int64 `form:"max_file_size_mb"`
	BannedFilePatterns         string
}

// Validate validates the fields
func (f *PushRuleForm) Validate(req *http.Request, errs binding.Errors) binding.Errors {
	ctx := context.GetValidateContext(req)
	return middleware.Validate(errs, ctx.Data, f, ctx.Locale)
}
