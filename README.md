# spoved

**spoved: a self-hosted Git forge, based on Forgejo.**

spoved is a self-hosted Git forge for companies that need to pass security and compliance audits. It is built with **SOC 2** and **ISO/IEC 27001** controls in mind: who can access what, how credentials are managed, how changes reach protected branches, and how the platform is operated.

spoved started as a fork of [Forgejo](https://forgejo.org) and keeps everything Forgejo offers: Git hosting, issues, pull requests, wikis, project boards, Actions (GitHub-compatible CI), a package registry for about 20 formats, LDAP and OAuth2/OIDC sign-in, and more. On top of that it adds the features listed below.

> spoved helps you meet SOC 2 and ISO 27001 controls; it does not make an organization compliant or certified by itself. Certification covers your organization, processes and evidence, not a single tool.

## Why a separate project

spoved was inspired by teams moving off **Bitbucket Data Center**, and by features from **GitLab Premium/Ultimate**, **Azure DevOps** and **GitHub Enterprise** that self-hosted forges lack: directory-synced groups, read-only auditor access, push rules, credential expiry, rate limiting and admin reporting.

spoved's goals have diverged from Forgejo's:

- **Compliance-first.** Features are chosen for audit and enterprise needs (access reviews, least privilege, credential policy, change control), not general-purpose or community hosting.
- **AI-assisted development.** spoved is built with AI coding agents (Claude Code) working alongside people, with review, security review and testing on every change. Forgejo doesn't accept AI-generated contributions, so spoved's changes are not offered back upstream.
- **Independent roadmap.** spoved still merges Forgejo's changes regularly to keep its fixes and security updates.

## Credits

spoved is a fork of **[Forgejo](https://codeberg.org/forgejo/forgejo)**, its predecessor, which is itself a fork of [Gitea](https://gitea.com) and [Gogs](https://gogs.io). Almost all of spoved's code is their work. Thank you to the Forgejo, Gitea and Gogs contributors. See [NOTICE](NOTICE).

spoved is a modified version of Forgejo, changed by the spoved contributors since 2026 (see the git history for each change). It is not affiliated with or endorsed by Forgejo or Codeberg e.V. "Forgejo" is a trademark of Codeberg e.V. and names the Forgejo project only.

## Features spoved adds

Each feature links to the pull request that added it.

### Identity and access

| Feature | What it does |
|---|---|
| **User groups shared across organizations** ([#3](https://github.com/InfiniteSpoved/spoved/pull/3), API [#9](https://github.com/InfiniteSpoved/spoved/pull/9)) | Instance-wide groups, managed by site admins, that org owners link to their teams. One group gives the same people access across many organizations, while each org keeps control of the permissions. |
| **Groups synced from LDAP / OIDC** ([#48](https://github.com/InfiniteSpoved/spoved/pull/48)) | Directory groups (Active Directory, Entra ID claims) map to user groups. Membership syncs at sign-in and during LDAP sync, and only memberships added by the directory are ever removed. |
| **Team pages show where access comes from** ([#51](https://github.com/InfiniteSpoved/spoved/pull/51), [#53](https://github.com/InfiniteSpoved/spoved/pull/53)) | Each linked group's member count, and which group each member comes from. |
| **Roles through groups** ([#54](https://github.com/InfiniteSpoved/spoved/pull/54)) | A group can make its members site administrators or allow them to create organizations. The last site admin can't be removed. |
| **Auditor (read-all) role** ([#118](https://github.com/InfiniteSpoved/spoved/pull/118)) | Members of an auditor group can read every repository and private organization, and nothing more. Writes are refused everywhere (web, API, git, SSH, LFS). Read-only access for security and compliance reviewers without admin rights. |
| **No organization creation by default** ([#44](https://github.com/InfiniteSpoved/spoved/pull/44)) | New users can't create organizations unless an admin allows it. |

### Credentials

| Feature | What it does |
|---|---|
| **Expiry for SSH keys, deploy keys and access tokens** ([#115](https://github.com/InfiniteSpoved/spoved/pull/115)) | Optional expiry dates. Expired credentials are refused everywhere. Admins can require expiry and set maximum lifetimes: `[security] SSH_KEY_*`, `DEPLOY_KEY_*` and `ACCESS_TOKEN_*` with `_EXPIRY_REQUIRED` and `_MAX_LIFETIME_DAYS`. |
| **Specific deploy keys in branch and tag protection** ([#50](https://github.com/InfiniteSpoved/spoved/pull/50)) | Allow chosen deploy keys, not all of them, to push to protected branches and tags. Deploy-key pushes to protected tags are decided by the key, never by the repository owner's identity. |

### Change control

| Feature | What it does |
|---|---|
| **Push rules** ([#116](https://github.com/InfiniteSpoved/spoved/pull/116)) | Require commit message and branch name patterns, author emails that match the pusher, and maximum file sizes; block banned file names. |
| **Delete and force-push allow-lists** ([#52](https://github.com/InfiniteSpoved/spoved/pull/52)) | Protected branches can allow chosen users, teams or deploy keys to delete them or force-push; everyone else stays blocked. The default branch can never be deleted. |
| **Scheduled auto-merge** ([#110](https://github.com/InfiniteSpoved/spoved/pull/110)) | Merge an approved pull request at a chosen time (e.g. after hours), tied to the reviewed commit so later pushes can't slip in unreviewed. |
| **Revert a merged pull request** ([#90](https://github.com/InfiniteSpoved/spoved/pull/90), [#135](https://github.com/InfiniteSpoved/spoved/pull/135)) | One button creates a pull request that reverts the merge, for every merge style. Both pull requests link to each other in the timeline. |
| **Generated release notes** ([#108](https://github.com/InfiniteSpoved/spoved/pull/108)) | Draft release notes from the pull requests merged since the previous tag, grouped by label (`.forgejo/release.yml` or `.github/release.yml`). |
| **Organization and instance-wide templates** ([#99](https://github.com/InfiniteSpoved/spoved/pull/99)) | Issue and pull request templates fall back to the organization's `.forgejo` repository, then to an instance repository (`[repository] DEFAULT_TEMPLATES_REPO`), so one checklist applies everywhere. |

### Operations and administration

| Feature | What it does |
|---|---|
| **Rate limiting** ([#133](https://github.com/InfiniteSpoved/spoved/pull/133)) | Limits for API, git, web and sign-in traffic per user and per IP, with standard 429 responses. Strict limits on failed sign-ins. Off until enabled in `[rate_limit]`. |
| **Restrict who can register CI runners** ([#106](https://github.com/InfiniteSpoved/spoved/pull/106)) | `[actions] RUNNER_REGISTRATION = admin`, `admin_and_org_owners` or `everyone`, so jobs and their secrets only run on approved machines. |
| **Actions run retention** ([#107](https://github.com/InfiniteSpoved/spoved/pull/107)) | `[actions] RUN_RETENTION_DAYS` deletes old CI runs, keeping the latest run of each workflow. |
| **Storage report** ([#109](https://github.com/InfiniteSpoved/spoved/pull/109), API [#141](https://github.com/InfiniteSpoved/spoved/pull/141)) | Site administration → Storage: git and LFS size in total, per owner and per repository, with a CSV export of the per-owner table and admin API endpoints. |
| **Announcement banners** ([#49](https://github.com/InfiniteSpoved/spoved/pull/49)) | Site-wide banners (info, warning, critical) with schedules, shown to everyone, signed-in users or admins, for maintenance and policy notices. |

### Interface

| Feature | What it does |
|---|---|
| **Company design tokens and themes** ([#104](https://github.com/InfiniteSpoved/spoved/pull/104)) | Light, dark, auto and high-contrast themes built on design tokens, with WCAG contrast checked in tests. Enable them with `[ui] THEMES` and `DEFAULT_THEME`. |
| **draw.io diagrams** ([#103](https://github.com/InfiniteSpoved/spoved/pull/103)) | `.drawio` files render in the file view, sandboxed, with no requests outside the instance. `[markup] DRAWIO_ENABLED` turns it off. |

### Development and quality

- CI on every pull request runs the lints, frontend checks, unit, migration, integration and end-to-end tests ([#11](https://github.com/InfiniteSpoved/spoved/pull/11), [#36](https://github.com/InfiniteSpoved/spoved/pull/36), [#27](https://github.com/InfiniteSpoved/spoved/pull/27), [#146](https://github.com/InfiniteSpoved/spoved/pull/146)).
- Fix for a cancellation race in git attribute checks ([#42](https://github.com/InfiniteSpoved/spoved/pull/42)).

Most new features are **off or unchanged by default** and are switched on in `app.ini`, so upgrading doesn't change behavior until you configure them.

## Roadmap

Planned work is tracked in [issues](https://github.com/InfiniteSpoved/spoved/issues), labelled by priority (`P0` to `P4`). Highlights include an audit log, SAML and SCIM, IP allowlisting, organization-level branch protection and push rules, approval rules and code owners, fine-grained token scopes, a trash bin for deleted repositories and organizations, incremental backups, high availability on Kubernetes (AKS), and a refreshed interface.

## Naming

The interface, emails, logo and default settings say spoved, and a "based on Forgejo" link in the footer credits the upstream project. Set `APP_NAME` in `app.ini` to show your own site name. [`docs/branding.md`](docs/branding.md) lists what the rename changes and how to keep it across upstream merges.

## Documentation

Forgejo's [documentation](https://forgejo.org/docs/latest/) applies to everything spoved inherits. spoved's additions are described in their pull requests and in [`docs/`](docs).

## Contributing

Changes go through pull requests into `develop` on this repository. See [`CONTRIBUTING.md`](CONTRIBUTING.md) for the workflow, local test steps and conventions.

## License

spoved is distributed under the terms of the [GPL version 3.0](LICENSE) or any later version, the same license as Forgejo. Forgejo versions before v9.0 were distributed under the MIT license. Copyright notices of the original authors are kept in the source files.
