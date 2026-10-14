# Personal repositories

Site administrators decide which repositories users may keep in their own namespace in
**Site administration > Personal repositories**. The default comes from `[repository] PERSONAL_REPOS` in app.ini;
the page stores the setting in the database, where it takes effect at once on every server.

| Policy | New personal repositories | Making a personal repository public |
|---|---|---|
| `allowed` (default) | public or private | allowed |
| `private_only` | private only | refused |
| `disabled` | refused | refused |

- Only individual users are restricted. Organizations, their repositories and their projects never are.
- Site administrators who can sign in are exempt, both as the acting user and as the owner. A site administrator can
  still create a repository for a user (`POST /api/v1/admin/users/{username}/repos`).
- The policy applies everywhere a repository can appear in a user's namespace: creating one (web, API), generating
  from a template, forking, migrating or mirroring, push-to-create, adopting files on disk, transfers to the user
  (also when a pending transfer is accepted) and restoring a repository from the trash to the user.
- A fork has the visibility of its source, so under `private_only` a public repository can't be forked into a user's
  namespace; fork it into an organization instead. When an organization repository is made public, its forks in
  users' namespaces stay private while the policy refuses public personal repositories.
- Existing repositories are never changed or deleted. The page lists the users whose repositories break the current
  policy (public repositories under `private_only`, any repository under `disabled`; repositories in the trash are not
  counted), so they can be moved to an organization.
- Bot accounts are restricted like users, but the list doesn't show them.
- Restoring a repository from the trash to its own owner is not checked: it is existing data, so a public repository
  comes back public under `private_only`. A restore to another owner is checked for that owner.
- The cargo package registry keeps its index in a public repository `_cargo-index` in the owner's namespace. Under
  `private_only` or `disabled` a non-admin user can't create it, so initialising the index in the package settings
  fails with the generic message (uploads only update an existing index); use an organization for cargo packages.
- An unknown value in app.ini stops the start; a stored value that can't be read counts as `disabled`.
- Every change is logged (`Personal repository policy changed from … to … by …`), and so is every refusal
  (`Personal repository policy … refused a repository of …`).

## Size limit

The same page sets the size limit of each user's personal repositories, in MiB; 0 turns it off. The default comes from
`[repository] PERSONAL_REPOS_SIZE_LIMIT` (bytes or a size such as `50 MiB`). Site admins can give a user an override in
the user's admin page (MiB) or with `PATCH /api/v1/admin/users/{username}` (`personal_repo_size_limit`, bytes): 0 uses
the site limit, -1 means unlimited, any other value replaces it (higher or lower). Overrides apply only while the site
limit is on. Organizations and site admins who can sign in are never limited.

- **Usage** counts every repository the user owns, those in the trash included: git data and wiki as last measured,
  and LFS objects as stored now. It is shown on the user's *Settings > Repositories* page and in the admin user list.
- **At or over the limit the user's repositories are locked for writes.** Every git push is refused before it starts,
  over HTTP and SSH, a push that only deletes a ref too, and so are wiki pushes, LFS uploads, push-to-create, new
  repositories (create, template, fork, migration, transfer, adopt, trash restore), web and API file changes,
  branches, tags and releases, release and issue attachments, package uploads, Actions artifacts, pull mirror syncs,
  syncing a fork, updating a pull request branch in the user's repository, and pull requests from another repository
  into the user's repository.
- **Only deletions made in the web UI stay allowed:** a repository (it moves to the trash; emptying the trash frees
  its space), branches, tags, files (only on the current branch, with the default commit message), releases,
  attachments, LFS objects (repository settings) and wiki pages. Restoring a deleted branch is refused while locked.
  After a delete in a locked namespace the repository runs `git gc` at once, so the space counts right away; space
  that other refs or history still reach is not freed (deleting a file keeps it in history).
- Writes are allowed again as soon as the usage is below the limit.
- The size of a repository is measured again after each push, in the background. A push that starts below the limit
  is accepted even if it crosses it, and so are pushes that run at the same time, so the usage can end up somewhat
  above the limit; the next write is refused. The limit is not a hard cap on disk space.
- The refusal says that the limit is reached and shows the usage and the limit; someone else than the owner sees no
  numbers. The admin page lists the users at or over their limit.
- Changes of the limit and of an override, and every refusal, are logged.
