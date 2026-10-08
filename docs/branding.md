# CodeHat branding

CodeHat is a modified version of Forgejo. The interface, emails, logo and defaults say **CodeHat**; the code, the Go module path (`forgejo.org`), the `forgejo` binary and CLI, config sections, API paths and database stay Forgejo's, so upstream merges stay cheap. This page lists every place the rename touches, so an upstream merge can keep it.

## What stays "Forgejo"

Visible text keeps "Forgejo" where it names the upstream project or ecosystem, not the running product:

- the footer credit link "based on Forgejo" (`a.codehat-credit`, locale key `based_on_forgejo`);
- Forgejo Actions and Forgejo Runner, the `FORGEJO_` variable and secret prefixes. CodeHat does not build its own runner, so the runner setup pages (repo, user, org, admin) keep the command `forgejo-runner` and the name "Forgejo Runner" (exact phrases in `brandAllowed`) and link to https://code.forgejo.org/forgejo/runner/releases (key `actions.runners.runner_setup.runner_download`). The example connection name in the YAML is `codehat`;
- the "Help us translate Forgejo" line on the appearance page (`settings.language.localization_project`): it links to Forgejo's localization documentation (`brandAllowed`);
- the `forgejo` registry, remote and keyring names in the package snippets (`templates/package/content/{cargo,conan,cran,debian}.tmpl`): users copy them into configs, so they are identifiers;
- the "Forgejo" migration source and the `forgejo-*` theme names;
- the `MyForgejoOrganization` / `MyForgejoTeam*` group-map placeholders in the LDAP and OAuth2 auth-source forms (upstream templates kept);
- documentation links (`AppDocsURL`, "Forgejo documentation"), the translation-project line and the translators' strings `meta.last_line` and `test`;
- the API (`/api/forgejo/v1`, swagger titles), CLI output, log messages, git hook messages (`remote: Forgejo: …`), the git `user.name`, the `X-Mailer` header and the migration/LFS User-Agents.

`TestBranding` (`tests/integration/branding_test.go`) fails on any other visible "Forgejo" or "GitHat"; its `brandAllowed` list holds the exact phrases above that appear on the pages it checks (group-map placeholders, "Forgejo documentation", "Forgejo Runner", `forgejo-runner`, "Forgejo Actions", "Help us translate Forgejo"). The case-sensitive guard does not see lowercase names, so the package snippet names need no entry and the runner pages assert that `forgejo:` is absent.

## Strings: shadow keys

`modules/translation/i18n/localestore.go` looks a key up in `options/locale_next/` before `options/locale/`. So instead of editing upstream's `locale_en-US.ini`, CodeHat adds the **same key** with CodeHat wording at the top of `options/locale_next/locale_en-US.json`. The ini line, templates and Go call sites stay untouched. Other languages are untouched too.

Shadowed ini keys (also listed in `codehatShadowKeys`, `modules/translation/codehat_test.go`):

- `return_to_forgejo`
- `repo.settings.web_hook_name_forgejo`
- `startpage.platform_desc`
- `startpage.lightweight_desc`
- `startpage.license_desc`
- `install.docker_helper`
- `install.require_db_desc`
- `install.sqlite_helper`
- `install.reinstall_error`
- `install.reinstall_confirm_message`
- `install.reinstall_confirm_check_3`
- `install.run_user_helper`
- `install.http_port_helper`
- `install.smtp_from_helper`
- `install.install_btn_confirm`
- `install.sqlite3_not_available`
- `install.enable_update_checker_helper_forgejo`
- `auth.non_local_account`
- `settings.password_change_disabled`
- `settings.tokens_desc`
- `settings.oauth2_application_locked`
- `settings.authorized_oauth2_applications_description`
- `settings.manage_account_links_desc`
- `settings.remove_account_link_desc`
- `repo.mirror_use_ssh.helper`
- `repo.pulls.cmd_instruction_merge_desc`
- `repo.settings.trust_model.committer.long`
- `repo.settings.trust_model.committer.desc`
- `repo.settings.trust_model.collaboratorcommitter.desc`
- `repo.settings.hooks_desc`
- `repo.settings.add_webhook_desc`
- `repo.release.deletion_desc`
- `admin.users.allow_git_hook_tooltip`
- `admin.defaulthooks.desc`
- `admin.systemhooks.desc`
- `admin.auths.attribute_username_placeholder`
- `admin.config.app_ver`
- `admin.config.allow_only_internal_registration`
- `admin.self_check.database_collation_case_insensitive`

New key: `based_on_forgejo` (footer credit).

`locale_next` keys edited in place (they already lived in that file):

- `incorrect_root_url`
- `install.ssh_authorized_keys_unexpected_key`
- `admin.dashboard.new_major_version_hint`
- `admin.dashboard.new_minor_version_hint`
- `admin.dashboard.update_checker_error`
- `admin.dashboard.resync_all_sshkeys`
- `admin.dashboard.resync_all_sshprincipals`
- `settings.must_enable_2fa`
- `settings.authorized_integration.desc`
- `settings.authorized_integration.forgejo_actions_local.description`
- `error.must_enable_2fa`
- `packages.owner.settings.chef.keypair.description`
- `actions.runners.runner_details.labels_note`
- `actions.workflow.permissions_warning`
- `actions.runners.runner_setup.instruction_replace_connection_name`
- `actions.runners.runner_setup.instruction_advanced_configurations`
- `actions.variables.mutation.name_description`
- `actions.secrets.mutation.name_description`

New key: `actions.runners.runner_setup.runner_download` (runner setup pages).

`error.report_message_githat` (500 page) keeps its pre-rename key name; it shows no product name.

## Overridden templates

- `templates/base/footer_content.tmpl`: "Powered by CodeHat" links to the repository, plus the `based on Forgejo` credit link.
- `templates/home_forgejo.tmpl`: the `startpage.install_desc` and `startpage.license_desc` links point to the CodeHat repository.
- `templates/webhook/new/{slack,discord,packagist}.tmpl`: the username placeholder is "CodeHat".
- `templates/admin/dashboard.tmpl`: the update-checker hints link to the CodeHat releases.
- `templates/status/500.tmpl`: uses `error.report_message_githat` with `ISSUE_TRACKER_URL`.

## Defaults and assets

- `modules/setting/server.go`: `APP_NAME` defaults to `CodeHat`; `ISSUE_TRACKER_URL` to the CodeHat issues.
- `modules/setting/ui.go`: `[ui.meta]` `AUTHOR`, `DESCRIPTION`, `KEYWORDS`; `DEFAULT_THEME = company-auto`, and the company themes come first in `THEMES`.
- `templates/install.tmpl`: the domain and URL placeholders are `git.example.com`.
- `routers/install/install.go`: the installer's default site title is `CodeHat` and its default slogan `A self-hosted Git forge` (upstream: Forgejo's slogan).
- `services/mailer/mail.go`: the admin test email's subject and body.
- `modules/updatechecker`: the `CodeHat-update-checker` User-Agent; `services/cron` checks the CodeHat GitHub releases.
- `assets/logo.svg`, `assets/favicon.svg`: the CodeHat mark. `make generate-images` writes `public/assets/img/{logo,favicon}.{svg,png}`, `apple-touch-icon.png` and `avatar_default.png`; the PWA manifest uses `logo.png`/`logo.svg`.
- `custom/conf/app.example.ini` documents the same defaults; `tests/*.ini.tmpl` set `APP_NAME = CodeHat`.

## Repository URLs

These link to `github.com/GitHatOrg/GitHat` and must be updated after the GitHub org and repository rename (GitHub redirects the old URLs until then): the footer, the home page (`startpage.install_desc` links to the releases, container package and repository; `startpage.license_desc` to the repository and its issues), the admin dashboard update hints, `ISSUE_TRACKER_URL` and the update checker's `HTTP_ENDPOINT` defaults (`modules/setting/server.go`, `services/cron/tasks_extended.go`, `custom/conf/app.example.ini` and their tests), README links, and the ACR OIDC subject comment in `.github/workflows/release-image.yml` (the subject itself is set in Azure). The image path is derived from the repository name, so the workflows need no change.

## Merging upstream

1. Merge as usual. Locale conflicts: keep both sides.
2. Run `go test ./modules/translation/ -run TestCodeHatShadowKeys`. It fails when upstream renames or removes a shadowed ini key: move the shadow to the new key, update the list above and in the test.
3. New upstream strings that name the product show up as "Forgejo": `make 'test-sqlite#TestBranding'` catches them on the pages it covers. Shadow them as above.
4. Conflicts in the templates above: keep upstream's change and re-apply the CodeHat line.
5. If upstream changes `assets/logo.svg`, keep ours and rerun `make generate-images`.
