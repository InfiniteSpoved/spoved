# Audit log

**Site administration > Security > Audit log** (`/admin/audit`) lists the audit log of the whole instance, newest
first. Site administrators who can sign in read it, and so do auditors (the auditor role of a user group) with their
own session or access token; OAuth2 apps and authorized integrations can't use the auditor's access.

## Filters

The page, `GET /api/v1/admin/audit` and `spoved admin audit export` take the same parameters, so a page URL is a
link to the same evidence in the API. All filters combine with AND.

| Parameter | Selects |
|---|---|
| `category` | `changes` (default), `signins`, `activity`, `all`, or a comma-separated list of categories |
| `org` | entries that happened in the organization, its projects and repositories (name or ID) |
| `project` | entries in the project (ID; with `org`, it must be a project of that organization) |
| `repo` | the repository's whole history, across transfers and moves (`owner/[project/]name` or ID) |
| `user` | what the user did, also as the admin behind a sudo, and what was done to their account (login or ID) |
| `credential_type`, `credential`, `credential_owner` | by credential type (comma-separated), one credential (`type:id`), or the user or organization owning it |
| `action`, `result`, `ip`, `request_id` | an action key or a prefix (`credential.*`), `success`/`failure`/`refused`, the client IP, one request |
| `since`, `before` | a time range in RFC 3339 |
| `target_type`, `target_id` | the object an entry is about |
| `before_id`, `after_id` | paging: older or newer entries than this ID |

Names are looked up as they are now, so a renamed or trashed object isn't found by its name. A value of only digits is
an ID and is used as it is, so entries of deleted and trashed objects stay reachable; the links on the page use IDs.
An unknown name is an error, never an empty list.

## Export

`?format=csv` or `?format=json` on the page or the API exports every matching entry, oldest first, with every column.
CSV cells that a spreadsheet would run as a formula start with `'`. Each export is itself recorded (`audit.exported`,
with the filter), and its response carries a manifest:

- `X-Audit-Filter`, `X-Audit-Generated-At`, `X-Audit-Truncated`;
- when the export stopped at 100,000 rows, `X-Audit-Exported-Through` and a `Link: <…>; rel="next"` with the same
  filter plus `since` and `after_id`, which continues it.

`spoved admin audit export --since … --until … [filters] --format csv|json [--output FILE]` has no row limit, writes
the same rows as the web export for the same filter, and prints the manifest to stderr. `--output` creates a new file
with mode 0600.

Exports use the heavy rate limit class; the page and the API list use the normal one. Both work in read-only
maintenance mode, and recording continues.

Viewing the page or listing entries through the API isn't recorded yet; that comes with read logging.
