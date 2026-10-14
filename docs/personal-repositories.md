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
- An unknown value in app.ini stops the start; a stored value that can't be read counts as `disabled`.
- Every change is logged (`Personal repository policy changed from … to … by …`), and so is every refusal
  (`Personal repository policy … refused a repository of …`).
