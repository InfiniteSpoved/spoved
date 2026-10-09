// Copyright 2014 The Gogs Authors. All rights reserved.
// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package org

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"forgejo.org/models"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	org_model "forgejo.org/models/organization"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/models/webhook"
	"forgejo.org/modules/base"
	"forgejo.org/modules/log"
	"forgejo.org/modules/optional"
	repo_module "forgejo.org/modules/repository"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/util"
	"forgejo.org/modules/web"
	shared_user "forgejo.org/routers/web/shared/user"
	user_setting "forgejo.org/routers/web/user/setting"
	"forgejo.org/services/context"
	"forgejo.org/services/forms"
	org_service "forgejo.org/services/org"
	repo_service "forgejo.org/services/repository"
	user_service "forgejo.org/services/user"
	usergroup_service "forgejo.org/services/usergroup"
	webhook_service "forgejo.org/services/webhook"
)

const (
	// tplSettingsOptions template path for render settings
	tplSettingsOptions base.TplName = "org/settings/options"
	// tplSettingsDelete template path for render delete repository
	tplSettingsDelete base.TplName = "org/settings/delete"
	// tplSettingsHooks template path for render hook settings
	tplSettingsHooks base.TplName = "org/settings/hooks"
	// tplSettingsLabels template path for render labels settings
	tplSettingsLabels base.TplName = "org/settings/labels"
)

// Settings render the main settings page
func Settings(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("org.settings")
	ctx.Data["PageIsOrgSettings"] = true
	ctx.Data["PageIsSettingsOptions"] = true
	ctx.Data["CurrentVisibility"] = ctx.Org.Organization.Visibility
	ctx.Data["RepoAdminChangeTeamAccess"] = ctx.Org.Organization.RepoAdminChangeTeamAccess
	ctx.Data["RequireTwoFactor"] = ctx.Org.Organization.RequireTwoFactor
	ctx.Data["ContextUser"] = ctx.ContextUser
	ctx.Data["CooldownPeriod"] = setting.Service.UsernameCooldownPeriod
	ctx.Data["MaxAvatarFileSize"] = setting.Avatar.MaxFileSize
	ctx.Data["MaxAvatarWidth"] = setting.Avatar.MaxWidth
	ctx.Data["MaxAvatarHeight"] = setting.Avatar.MaxHeight

	err := shared_user.LoadHeaderCount(ctx)
	if err != nil {
		ctx.ServerError("LoadHeaderCount", err)
		return
	}

	ctx.HTML(http.StatusOK, tplSettingsOptions)
}

// SettingsPost response for settings change submitted
func SettingsPost(ctx *context.Context) {
	form := web.GetForm(ctx).(*forms.UpdateOrgSettingForm)
	ctx.Data["Title"] = ctx.Tr("org.settings")
	ctx.Data["PageIsOrgSettings"] = true
	ctx.Data["PageIsSettingsOptions"] = true
	ctx.Data["CurrentVisibility"] = ctx.Org.Organization.Visibility
	ctx.Data["CooldownPeriod"] = setting.Service.UsernameCooldownPeriod
	ctx.Data["MaxAvatarFileSize"] = setting.Avatar.MaxFileSize
	ctx.Data["MaxAvatarWidth"] = setting.Avatar.MaxWidth
	ctx.Data["MaxAvatarHeight"] = setting.Avatar.MaxHeight

	if ctx.HasError() {
		ctx.HTML(http.StatusOK, tplSettingsOptions)
		return
	}

	org := ctx.Org.Organization

	// turning on the two-factor requirement needs the doer's own 2FA and a confirmation, before anything is saved;
	// any other change needs none, as in a flagged org a doer without 2FA isn't an acting owner and never gets here
	if form.RequireTwoFactorShown && form.RequireTwoFactor && !org.RequireTwoFactor && !confirmRequireTwoFactor(ctx, org, form) {
		return
	}

	if org.Name != form.Name {
		if err := user_service.RenameUser(ctx, org.AsUser(), form.Name); err != nil {
			if user_model.IsErrUserAlreadyExist(err) {
				ctx.Data["Err_Name"] = true
				ctx.RenderWithErr(ctx.Tr("form.username_been_taken"), tplSettingsOptions, &form)
			} else if user_model.IsErrCooldownPeriod(err) {
				ctx.Data["Err_UserName"] = true
				ctx.RenderWithErr(ctx.Locale.Tr("form.username_claiming_cooldown", err.(user_model.ErrCooldownPeriod).ExpireTime.Format(time.RFC1123Z)), tplSettingsOptions, form)
			} else if db.IsErrNameReserved(err) {
				ctx.Data["Err_Name"] = true
				ctx.RenderWithErr(ctx.Tr("repo.form.name_reserved", err.(db.ErrNameReserved).Name), tplSettingsOptions, &form)
			} else if db.IsErrNamePatternNotAllowed(err) {
				ctx.Data["Err_Name"] = true
				ctx.RenderWithErr(ctx.Tr("repo.form.name_pattern_not_allowed", err.(db.ErrNamePatternNotAllowed).Pattern), tplSettingsOptions, &form)
			} else {
				ctx.ServerError("RenameUser", err)
			}
			return
		}

		ctx.Org.OrgLink = setting.AppSubURL + "/org/" + url.PathEscape(org.Name)
	}

	if form.Email == "" {
		err := user_model.DeletePrimaryEmailAddressOfUser(ctx, org.ID)
		if err != nil {
			ctx.ServerError("DeletePrimaryEmailAddressOfUser", err)
			return
		}
	} else {
		if err := user_service.ReplacePrimaryEmailAddress(ctx, org.AsUser(), form.Email); err != nil {
			ctx.Data["Err_Email"] = true
			ctx.RenderWithErr(ctx.Tr("form.email_invalid"), tplSettingsOptions, &form)
			return
		}
	}

	opts := &user_service.UpdateOptions{
		FullName:                  optional.Some(form.FullName),
		Description:               optional.Some(form.Description),
		Website:                   optional.Some(form.Website),
		Location:                  optional.Some(form.Location),
		Visibility:                optional.Some(form.Visibility),
		RepoAdminChangeTeamAccess: optional.Some(form.RepoAdminChangeTeamAccess),
	}
	if form.RequireTwoFactorShown {
		opts.RequireTwoFactor = optional.Some(form.RequireTwoFactor)
	}
	if ctx.Doer.IsAdmin {
		opts.MaxRepoCreation = optional.Some(form.MaxRepoCreation)
	}
	opts.RestrictRepoVisibilityChange = optional.Some(form.RestrictRepoVisibilityChange)
	opts.PrivateForkPolicy = optional.Some(form.PrivateForkPolicy)

	visibilityChanged := org.Visibility != form.Visibility

	if err := user_service.UpdateUser(ctx, org.AsUser(), opts); err != nil {
		ctx.ServerError("UpdateUser", err)
		return
	}

	// update forks visibility
	if visibilityChanged {
		repos, _, err := repo_model.GetUserRepositories(ctx, &repo_model.SearchRepoOptions{
			Actor: org.AsUser(), Private: true, ListOptions: db.ListOptions{Page: 1, PageSize: org.NumRepos},
		})
		if err != nil {
			ctx.ServerError("GetRepositories", err)
			return
		}
		for _, repo := range repos {
			repo.OwnerName = org.Name
			if err := repo_service.UpdateRepository(ctx, repo, true); err != nil {
				ctx.ServerError("UpdateRepository", err)
				return
			}
		}
	}

	log.Trace("Organization setting updated: %s", org.Name)
	ctx.Flash.Success(ctx.Tr("org.settings.update_setting_success"))
	ctx.Redirect(ctx.Org.OrgLink + "/settings")
}

// confirmRequireTwoFactor reports whether the two-factor requirement may be turned on. Otherwise it renders the form
// again, as submitted: with an error if the doer has no 2FA, or with the members who lack 2FA and a confirmation.
func confirmRequireTwoFactor(ctx *context.Context, org *org_model.Organization, form *forms.UpdateOrgSettingForm) bool {
	if !ctx.Doer.IsAdmin {
		has, err := auth_model.HasTwoFactorByUID(ctx, ctx.Doer.ID)
		if err != nil {
			ctx.ServerError("HasTwoFactorByUID", err)
			return false
		}
		if !has {
			ctx.Flash.Error(ctx.Tr("org.settings.require_two_factor_doer_needs_2fa"), true)
			renderSubmittedSettings(ctx, org, form)
			return false
		}
	}
	if form.RequireTwoFactorConfirm {
		return true
	}

	members, _, err := org_model.FindOrgMembers(ctx, &org_model.FindOrgMembersOpts{Doer: ctx.Doer, IsDoerMember: true, OrgID: org.ID})
	if err != nil {
		ctx.ServerError("FindOrgMembers", err)
		return false
	}
	withTwoFactor, err := auth_model.UserIDsWithTwoFactor(ctx, members.GetUserIDs())
	if err != nil {
		ctx.ServerError("UserIDsWithTwoFactor", err)
		return false
	}
	ownerTeam, err := org.GetOwnerTeam(ctx)
	if err != nil {
		ctx.ServerError("GetOwnerTeam", err)
		return false
	}
	if err := ownerTeam.LoadMembers(ctx); err != nil {
		ctx.ServerError("LoadMembers", err)
		return false
	}
	owners := map[int64]bool{}
	for _, u := range ownerTeam.Members {
		owners[u.ID] = true
	}
	without := make([]*user_model.User, 0, len(members))
	for _, u := range members {
		if !withTwoFactor.Contains(u.ID) {
			without = append(without, u)
		}
	}
	ctx.Data["RequireTwoFactorConfirm"] = true
	ctx.Data["RequireTwoFactorMembers"] = without
	ctx.Data["RequireTwoFactorOwners"] = owners
	renderSubmittedSettings(ctx, org, form)
	return false
}

// renderSubmittedSettings renders the settings form with the submitted values, saving nothing.
func renderSubmittedSettings(ctx *context.Context, org *org_model.Organization, form *forms.UpdateOrgSettingForm) {
	shown := *org
	shown.Name, shown.FullName, shown.Email, shown.Description = form.Name, form.FullName, form.Email, form.Description
	shown.Website, shown.Location = form.Website, form.Location
	if ctx.Doer.IsAdmin {
		shown.MaxRepoCreation = form.MaxRepoCreation
	}
	ctx.Data["Org"] = &shown
	ctx.Data["CurrentVisibility"] = form.Visibility
	ctx.Data["RepoAdminChangeTeamAccess"] = form.RepoAdminChangeTeamAccess
	ctx.Data["RequireTwoFactor"] = form.RequireTwoFactor
	ctx.HTML(http.StatusOK, tplSettingsOptions)
}

// SettingsAvatar response for change avatar on settings page
func SettingsAvatar(ctx *context.Context) {
	form := web.GetForm(ctx).(*forms.AvatarForm)
	form.Source = forms.AvatarLocal
	if err := user_setting.UpdateAvatarSetting(ctx, form, ctx.Org.Organization.AsUser()); err != nil {
		ctx.Flash.Error(err.Error())
	} else {
		ctx.Flash.Success(ctx.Tr("org.settings.update_avatar_success"))
	}

	ctx.Redirect(ctx.Org.OrgLink + "/settings")
}

// SettingsDeleteAvatar response for delete avatar on settings page
func SettingsDeleteAvatar(ctx *context.Context) {
	if err := user_service.DeleteAvatar(ctx, ctx.Org.Organization.AsUser()); err != nil {
		ctx.Flash.Error(err.Error())
	}

	ctx.JSONRedirect(ctx.Org.OrgLink + "/settings")
}

// ArchivePost archives the organization.
func ArchivePost(ctx *context.Context) {
	if !ctx.Org.IsOwner {
		ctx.Error(http.StatusForbidden)
		return
	}
	if err := org_service.ArchiveOrganization(ctx, ctx.Org.Organization, ctx.Doer, ctx.FormBool("remove_members")); err != nil {
		ctx.Error(archiveErrorStatus(err), err.Error())
		return
	}
	ctx.Flash.Success(ctx.Tr("org.settings.archive.success"))
	ctx.Redirect(ctx.Org.OrgLink + "/settings/delete")
}

// UnarchivePost unarchives the organization.
func UnarchivePost(ctx *context.Context) {
	if !ctx.Org.IsOwner {
		ctx.Error(http.StatusForbidden)
		return
	}
	if err := org_service.UnarchiveOrganization(ctx, ctx.Org.Organization); err != nil {
		ctx.Error(archiveErrorStatus(err), err.Error())
		return
	}
	// the org is unarchived already; the next sync heals any membership this missed
	if err := usergroup_service.SyncOrgTeams(ctx, ctx.Org.Organization.ID); err != nil {
		log.Error("SyncOrgTeams(%d) after unarchive: %v", ctx.Org.Organization.ID, err)
	}
	ctx.Flash.Success(ctx.Tr("org.settings.unarchive.success"))
	ctx.Redirect(ctx.Org.OrgLink + "/settings/delete")
}

func archiveErrorStatus(err error) int {
	switch {
	case org_model.IsErrOrgArchived(err):
		return http.StatusForbidden
	case errors.Is(err, util.ErrInvalidArgument):
		return http.StatusUnprocessableEntity
	}
	return http.StatusInternalServerError
}

// SettingsDelete response for deleting an organization
func SettingsDelete(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("org.settings")
	ctx.Data["PageIsOrgSettings"] = true
	ctx.Data["PageIsSettingsDelete"] = true

	if ctx.Req.Method == "POST" {
		if ctx.Org.Organization.Name != ctx.FormString("org_name") {
			ctx.Data["Err_OrgName"] = true
			ctx.RenderWithErr(ctx.Tr("form.enterred_invalid_org_name"), tplSettingsDelete, nil)
			return
		}

		if err := org_service.DeleteOrTrashOrganization(ctx, ctx.Doer, ctx.Org.Organization); err != nil {
			if models.IsErrUserOwnRepos(err) {
				ctx.Flash.Error(ctx.Tr("form.org_still_own_repo"))
				ctx.Redirect(ctx.Org.OrgLink + "/settings/delete")
			} else if models.IsErrUserOwnPackages(err) {
				ctx.Flash.Error(ctx.Tr("form.org_still_own_packages"))
				ctx.Redirect(ctx.Org.OrgLink + "/settings/delete")
			} else {
				ctx.ServerError("DeleteOrganization", err)
			}
		} else {
			log.Trace("Organization deleted: %s", ctx.Org.Organization.Name)
			ctx.Redirect(setting.AppSubURL + "/")
		}
		return
	}

	ctx.Data["TrashRetentionDays"] = setting.Trash.RetentionDays
	ctx.Data["TrashRestoreHelpURL"] = setting.Trash.RestoreHelpURL

	err := shared_user.LoadHeaderCount(ctx)
	if err != nil {
		ctx.ServerError("LoadHeaderCount", err)
		return
	}

	ctx.HTML(http.StatusOK, tplSettingsDelete)
}

// Webhooks render webhook list page
func Webhooks(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("org.settings")
	ctx.Data["PageIsOrgSettings"] = true
	ctx.Data["PageIsSettingsHooks"] = true
	ctx.Data["BaseLink"] = ctx.Org.OrgLink + "/settings/hooks"
	ctx.Data["BaseLinkNew"] = ctx.Org.OrgLink + "/settings/hooks"
	ctx.Data["WebhookList"] = webhook_service.EnabledList(ctx)
	ctx.Data["DisabledWebhookTypes"] = webhook_service.DisabledTypes(ctx)
	ctx.Data["Description"] = ctx.Tr("org.settings.hooks_desc")

	ws, err := db.Find[webhook.Webhook](ctx, webhook.ListWebhookOptions{OwnerID: ctx.Org.Organization.ID})
	if err != nil {
		ctx.ServerError("ListWebhooksByOpts", err)
		return
	}

	err = shared_user.LoadHeaderCount(ctx)
	if err != nil {
		ctx.ServerError("LoadHeaderCount", err)
		return
	}

	ctx.Data["Webhooks"] = ws
	ctx.HTML(http.StatusOK, tplSettingsHooks)
}

// DeleteWebhook response for delete webhook
func DeleteWebhook(ctx *context.Context) {
	if err := webhook.DeleteWebhookByOwnerID(ctx, ctx.Org.Organization.ID, ctx.FormInt64("id")); err != nil {
		ctx.Flash.Error("DeleteWebhookByOwnerID: " + err.Error())
	} else {
		ctx.Flash.Success(ctx.Tr("repo.settings.webhook_deletion_success"))
	}

	ctx.JSONRedirect(ctx.Org.OrgLink + "/settings/hooks")
}

// Labels render organization labels page
func Labels(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("repo.labels")
	ctx.Data["PageIsOrgSettings"] = true
	ctx.Data["PageIsOrgSettingsLabels"] = true
	ctx.Data["LabelTemplateFiles"] = repo_module.LabelTemplateFiles

	err := shared_user.LoadHeaderCount(ctx)
	if err != nil {
		ctx.ServerError("LoadHeaderCount", err)
		return
	}

	ctx.HTML(http.StatusOK, tplSettingsLabels)
}
