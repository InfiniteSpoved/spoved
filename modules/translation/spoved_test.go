// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package translation

import (
	"os"
	"testing"

	"forgejo.org/modules/json"
	"forgejo.org/modules/test"
	"forgejo.org/modules/translation/i18n"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spovedShadowKeys are upstream locale_en-US.ini keys that options/locale_next/locale_en-US.json
// overrides with spoved wording (see docs/branding.md).
var spovedShadowKeys = []string{
	"return_to_forgejo",
	"repo.settings.web_hook_name_forgejo",
	"startpage.platform_desc",
	"startpage.lightweight_desc",
	"startpage.license_desc",
	"install.docker_helper",
	"install.require_db_desc",
	"install.sqlite_helper",
	"install.reinstall_error",
	"install.reinstall_confirm_message",
	"install.reinstall_confirm_check_3",
	"install.run_user_helper",
	"install.http_port_helper",
	"install.smtp_from_helper",
	"install.install_btn_confirm",
	"install.sqlite3_not_available",
	"install.enable_update_checker_helper_forgejo",
	"auth.non_local_account",
	"settings.password_change_disabled",
	"settings.tokens_desc",
	"settings.oauth2_application_locked",
	"settings.authorized_oauth2_applications_description",
	"settings.manage_account_links_desc",
	"settings.remove_account_link_desc",
	"repo.mirror_use_ssh.helper",
	"repo.pulls.cmd_instruction_merge_desc",
	"repo.settings.trust_model.committer.long",
	"repo.settings.trust_model.committer.desc",
	"repo.settings.trust_model.collaboratorcommitter.desc",
	"repo.settings.hooks_desc",
	"repo.settings.add_webhook_desc",
	"repo.release.deletion_desc",
	"admin.users.allow_git_hook_tooltip",
	"admin.defaulthooks.desc",
	"admin.systemhooks.desc",
	"admin.auths.attribute_username_placeholder",
	"admin.config.app_ver",
	"admin.config.allow_only_internal_registration",
	"admin.self_check.database_collation_case_insensitive",
}

func TestSpovedShadowKeys(t *testing.T) {
	ini, err := os.ReadFile("../../options/locale/locale_en-US.ini")
	require.NoError(t, err)
	next, err := os.ReadFile("../../options/locale_next/locale_en-US.json")
	require.NoError(t, err)

	upstream := i18n.NewLocaleStore()
	require.NoError(t, upstream.AddLocaleByIni("en-US", "English", PluralRules[0], UsedPluralForms[0], ini, nil))
	shadowed := i18n.NewLocaleStore()
	require.NoError(t, shadowed.AddLocaleByIni("en-US", "English", PluralRules[0], UsedPluralForms[0], ini, nil))
	require.NoError(t, shadowed.AddToLocaleFromJSON("en-US", next))
	upstreamEn, _ := upstream.Locale("en-US")
	en, _ := shadowed.Locale("en-US")

	for _, key := range spovedShadowKeys {
		assert.True(t, upstreamEn.HasKey(key), "%s is no longer in locale_en-US.ini: update the shadow key and docs/branding.md", key)
		value := en.TrString(key)
		assert.Contains(t, value, "spoved", key)
		assert.Empty(t, test.BrandMentions(value), key)
	}
}

func TestSpovedLocaleNextValues(t *testing.T) {
	next, err := os.ReadFile("../../options/locale_next/locale_en-US.json")
	require.NoError(t, err)
	var values map[string]any
	require.NoError(t, json.Unmarshal(next, &values))
	for key, v := range values {
		if s, ok := v.(string); ok {
			assert.NotRegexp(t, `CodeHat|GitHat|Spoved|SPOVED`, s, key)
		}
	}
}
