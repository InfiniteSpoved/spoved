# Actions in projects

A repository in a project (`org/project/repo`) runs Actions like any other repository of its organization. What differs:

## Secrets and variables

Org owners add **secrets** and **variables** to a project under *Project settings → Actions secrets / Actions variables*. A job gets, closest level first:

1. the repository's own,
2. the project of its repository,
3. the organization's (or the user's),
4. the instance's (variables only).

A name defined at a closer level replaces the farther one. A project's values reach only the repositories in it *now*: when a repository moves to another project, its next job gets the new project's values; a job that already started keeps what it was given. Fork pull requests get no secrets, as with organization secrets (`pull_request_target` runs in the base repository and gets them). Secret values are never shown again after saving; leave the value blank when editing to keep it.

An organization owner cannot stop a project from overriding an organization secret: the project page marks the names it overrides.

## Names in the Actions context

| | root repository | repository in a project |
| --- | --- | --- |
| `github.repository` | `dept/api` | `dept/payments/api` |
| `github.repository_owner` | `dept` | `dept` |
| `github.namespace_path` | `dept` | `dept/payments` |
| `github.repository_group` | empty | `payments` |
| `github.repository_leaf` | `api` | `api` |
| `github.forgejo_repository_path` | `dept/api` | `dept/payments/api` |

`github.repository` has three segments for a repository in a project. Actions that split it into owner and repo (everything using `context.repo`) do not understand that; use `github.repository_leaf` and `github.namespace_path`, or the environment of your own scripts. Reusable workflows and actions hosted in a project (`uses: dept/payments/api/...`) are not resolved; keep them in a repository at the root of the organization.

## OpenID Connect

The ID token's `repository` claim is the full path, and it carries `namespace_path`. The default `sub` is `repo:<owner>-<owner id>/<repository>-<repository id>:…` with the repository's own name only, so renaming a project or moving the repository does not change it (the legacy format follows the full path). Names change when repositories move; write trust policies on `repository_id` and `repository_owner_id`, not on `sub` patterns such as `repo:dept/*`, which also match repositories in projects.
