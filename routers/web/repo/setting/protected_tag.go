// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	git_model "forgejo.org/models/git"
	"forgejo.org/models/organization"
	"forgejo.org/models/perm"
	access_model "forgejo.org/models/perm/access"
	"forgejo.org/modules/base"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/web"
	"forgejo.org/services/context"
	"forgejo.org/services/forms"
)

const (
	tplTags base.TplName = "repo/settings/tags"
)

// Tags render the page to protect tags
func ProtectedTags(ctx *context.Context) {
	if setTagsContext(ctx) != nil {
		return
	}

	ctx.HTML(http.StatusOK, tplTags)
}

// NewProtectedTagPost handles creation of a protect tag
func NewProtectedTagPost(ctx *context.Context) {
	if setTagsContext(ctx) != nil {
		return
	}

	if ctx.HasError() {
		ctx.HTML(http.StatusOK, tplTags)
		return
	}

	repo := ctx.Repo.Repository
	form := web.GetForm(ctx).(*forms.ProtectTagForm)

	pt := &git_model.ProtectedTag{
		RepoID:      repo.ID,
		NamePattern: strings.TrimSpace(form.NamePattern),
	}

	if strings.TrimSpace(form.AllowlistUsers) != "" {
		pt.AllowlistUserIDs, _ = base.StringsToInt64s(strings.Split(form.AllowlistUsers, ","))
	}
	if strings.TrimSpace(form.AllowlistTeams) != "" {
		pt.AllowlistTeamIDs, _ = base.StringsToInt64s(strings.Split(form.AllowlistTeams, ","))
	}
	if !setAllowlistDeployKeyIDs(ctx, pt, form.AllowlistDeployKeyIDs) {
		return
	}

	if err := git_model.InsertProtectedTag(ctx, pt); err != nil {
		ctx.ServerError("InsertProtectedTag", err)
		return
	}

	ctx.Flash.Success(ctx.Tr("repo.settings.update_settings_success"))
	ctx.Redirect(setting.AppSubURL + ctx.Req.URL.EscapedPath())
}

// EditProtectedTag render the page to edit a protect tag
func EditProtectedTag(ctx *context.Context) {
	if setTagsContext(ctx) != nil {
		return
	}

	ctx.Data["PageIsEditProtectedTag"] = true

	pt := selectProtectedTagByContext(ctx)
	if pt == nil {
		return
	}

	ctx.Data["name_pattern"] = pt.NamePattern
	ctx.Data["allowlist_users"] = joinIDs(userIDsOnly(pt.AllowlistUserIDs))
	ctx.Data["allowlist_teams"] = strings.Join(base.Int64sToStrings(pt.AllowlistTeamIDs), ",")
	ctx.Data["allowlist_deploy_key_ids"] = joinIDs(append(slices.Clone(pt.AllowlistDeployKeyIDs), tokenIDsOnly(pt.AllowlistUserIDs)...))

	ctx.HTML(http.StatusOK, tplTags)
}

// EditProtectedTagPost handles creation of a protect tag
func EditProtectedTagPost(ctx *context.Context) {
	if setTagsContext(ctx) != nil {
		return
	}

	ctx.Data["PageIsEditProtectedTag"] = true

	if ctx.HasError() {
		ctx.HTML(http.StatusOK, tplTags)
		return
	}

	pt := selectProtectedTagByContext(ctx)
	if pt == nil {
		return
	}

	form := web.GetForm(ctx).(*forms.ProtectTagForm)

	pt.NamePattern = strings.TrimSpace(form.NamePattern)
	pt.AllowlistUserIDs, _ = base.StringsToInt64s(strings.Split(form.AllowlistUsers, ","))
	pt.AllowlistTeamIDs, _ = base.StringsToInt64s(strings.Split(form.AllowlistTeams, ","))
	if !setAllowlistDeployKeyIDs(ctx, pt, form.AllowlistDeployKeyIDs) {
		return
	}

	if err := git_model.UpdateProtectedTag(ctx, pt); err != nil {
		ctx.ServerError("UpdateProtectedTag", err)
		return
	}

	ctx.Flash.Success(ctx.Tr("repo.settings.update_settings_success"))
	ctx.Redirect(ctx.Repo.Repository.Link() + "/settings/tags")
}

// DeleteProtectedTagPost handles deletion of a protected tag
func DeleteProtectedTagPost(ctx *context.Context) {
	pt := selectProtectedTagByContext(ctx)
	if pt == nil {
		return
	}

	if err := git_model.DeleteProtectedTag(ctx, pt); err != nil {
		ctx.ServerError("DeleteProtectedTag", err)
		return
	}

	ctx.Flash.Success(ctx.Tr("repo.settings.update_settings_success"))
	ctx.Redirect(ctx.Repo.Repository.Link() + "/settings/tags")
}

func setTagsContext(ctx *context.Context) error {
	ctx.Data["Title"] = ctx.Tr("repo.settings.tags")
	ctx.Data["PageIsSettingsTags"] = true

	protectedTags, err := git_model.GetProtectedTags(ctx, ctx.Repo.Repository.ID)
	if err != nil {
		ctx.ServerError("GetProtectedTags", err)
		return err
	}
	ctx.Data["ProtectedTags"] = protectedTags

	users, err := access_model.GetRepoReaders(ctx, ctx.Repo.Repository)
	if err != nil {
		ctx.ServerError("Repo.Repository.GetReaders", err)
		return err
	}
	ctx.Data["Users"] = users

	deployKeys, err := getWriteDeployKeys(ctx)
	if err != nil {
		ctx.ServerError("getWriteDeployKeys", err)
		return err
	}
	ctx.Data["DeployKeys"] = deployKeys
	tokens, err := getWriteTokens(ctx)
	if err != nil {
		ctx.ServerError("getWriteTokens", err)
		return err
	}
	ctx.Data["AllowlistTokens"] = tokens

	if ctx.Repo.Owner.IsOrganization() {
		teams, err := organization.OrgFromUser(ctx.Repo.Owner).TeamsWithAccessToRepo(ctx, ctx.Repo.Repository.ID, perm.AccessModeRead)
		if err != nil {
			ctx.ServerError("Repo.Owner.TeamsWithAccessToRepo", err)
			return err
		}
		ctx.Data["Teams"] = teams
	}

	return nil
}

// setAllowlistDeployKeyIDs sets the deploy keys and tokens allowed by the protected tag from its picker, keeping only
// the write deploy keys and tokens that reach the repository; tokens join the users (call it after setting them). It
// returns false if an error response was written.
func setAllowlistDeployKeyIDs(ctx *context.Context, pt *git_model.ProtectedTag, keyIDs string) bool {
	ids, _ := base.StringsToInt64s(strings.Split(keyIDs, ","))
	var err error
	pt.AllowlistDeployKeyIDs, err = git_model.FilterWriteDeployKeyIDs(ctx, ctx.Repo.Repository, keyIDsOnly(ids))
	if err != nil {
		ctx.ServerError("FilterWriteDeployKeyIDs", err)
		return false
	}
	tokens, err := getWriteTokens(ctx)
	if err != nil {
		ctx.ServerError("getWriteTokens", err)
		return false
	}
	pt.AllowlistUserIDs = userIDsOnly(pt.AllowlistUserIDs)
	for _, id := range tokenIDsOnly(ids) {
		if slices.ContainsFunc(tokens, func(t *allowlistToken) bool { return t.BotID == id }) {
			pt.AllowlistUserIDs = append(pt.AllowlistUserIDs, id)
		}
	}
	return true
}

func selectProtectedTagByContext(ctx *context.Context) *git_model.ProtectedTag {
	id := ctx.FormInt64("id")
	if id == 0 {
		id = ctx.ParamsInt64(":id")
	}

	tag, err := git_model.GetProtectedTagByID(ctx, id)
	if err != nil {
		ctx.ServerError("GetProtectedTagByID", err)
		return nil
	}

	if tag != nil && tag.RepoID == ctx.Repo.Repository.ID {
		return tag
	}

	ctx.NotFound("", fmt.Errorf("ProtectedTag[%v] not associated to repository %v", id, ctx.Repo.Repository))

	return nil
}
