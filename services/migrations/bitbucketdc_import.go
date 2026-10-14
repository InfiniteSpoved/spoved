// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package migrations

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"unicode"

	"forgejo.org/models/organization"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	base "forgejo.org/modules/migration"
	"forgejo.org/modules/structs"
	"forgejo.org/modules/util"
	"forgejo.org/services/migrations/allowlist"
)

// BitbucketImportOptions are the options of an import of Bitbucket Data Center projects into an organization.
type BitbucketImportOptions struct {
	Server       string
	Username     string // the account of a user token; empty for a project or repository token
	Token        string
	Projects     []string // project keys; empty imports every project the token can read
	DryRun       bool
	PullRequests bool
}

// String never prints the token.
func (o BitbucketImportOptions) String() string {
	return fmt.Sprintf("{server: %s, projects: %v, dry run: %t}", o.Server, o.Projects, o.DryRun)
}

// GoString never prints the token.
func (o BitbucketImportOptions) GoString() string {
	return o.String()
}

// The statuses of a BitbucketImportItem.
const (
	BitbucketPlanned  = "planned"
	BitbucketReused   = "reused"
	BitbucketCreated  = "created"
	BitbucketConflict = "conflict"
	BitbucketFailed   = "failed"
)

// BitbucketImportItem is a project or repository of an import. Text from the Bitbucket server is stripped of control
// characters.
type BitbucketImportItem struct {
	Kind      string `json:"kind"`      // "project" or "repository"
	Bitbucket string `json:"bitbucket"` // KEY or KEY/slug
	Name      string `json:"name"`      // the path in spoved
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	OldHTTP   string `json:"old_http,omitempty"`
	OldSSH    string `json:"old_ssh,omitempty"` // display only: never fetched
	OldWeb    string `json:"old_web,omitempty"`
	NewHTTP   string `json:"new_http,omitempty"`
	NewSSH    string `json:"new_ssh,omitempty"`
	NewWeb    string `json:"new_web,omitempty"`

	key, slug, groupName, description string
	group                             *repo_model.RepoGroup
	repos                             []*BitbucketImportItem
}

func (item *BitbucketImportItem) set(status, reason string) {
	item.Status, item.Reason = status, util.StripControl(reason)
}

// BitbucketImportReport is the plan of an import, and once it ran, its outcome.
type BitbucketImportReport struct {
	Items   []*BitbucketImportItem `json:"items"`
	Blocked bool                   `json:"blocked"` // a conflict: nothing was written
}

// bitbucketImports holds the organizations an import runs into in this process. It never waits, so it can't be held
// across a transaction someone else waits on; on another pod, a concurrent import fails closed on the name claims and
// the unique key of the origin rows.
var bitbucketImports sync.Map

// ImportBitbucket plans the import of Bitbucket Data Center projects into the organization orgName as doerName, a site
// admin or an acting owner of the organization, and unless it is a dry run or the plan has a conflict, runs it. Every
// write goes through the normal services as the doer, and the first one happens only after the whole plan was checked.
func ImportBitbucket(ctx context.Context, doerName, orgName string, opts BitbucketImportOptions) (*BitbucketImportReport, error) {
	doer, err := user_model.GetUserByName(ctx, doerName)
	if err != nil {
		return nil, err
	}
	if !doer.IsActive || doer.ProhibitLogin {
		return nil, util.NewPermissionDeniedErrorf("%s can't sign in", doer.Name)
	}
	org, err := organization.GetOrgByName(ctx, orgName)
	if err != nil {
		return nil, err
	}
	if org.IsArchived {
		return nil, organization.ErrOrgArchived{Name: org.Name}
	}
	if !doer.IsAdmin {
		if owner, err := org.IsActingOwner(ctx, doer); err != nil {
			return nil, err
		} else if !owner {
			return nil, util.NewPermissionDeniedErrorf("%s is not an owner of %s", doer.Name, org.Name)
		}
	}
	server, err := parseBitbucketServer(opts.Server)
	if err != nil {
		return nil, err
	}
	if err := allowlist.IsMigrateURLAllowed(server.String(), doer); err != nil {
		return nil, err
	}

	if _, busy := bitbucketImports.LoadOrStore(org.ID, true); busy {
		return nil, fmt.Errorf("%w: an import into %s is already running", util.ErrAlreadyExist, org.Name)
	}
	defer bitbucketImports.Delete(org.ID)

	d := newBitbucketDataCenterDownloader(ctx, server, "", "", opts.Token)
	report, err := planBitbucketImport(ctx, d, org, opts.Projects)
	if err != nil || report.Blocked || opts.DryRun {
		return report, err
	}
	applyBitbucketImport(ctx, doer, org, d, report, opts)
	return report, nil
}

// parseBitbucketServer accepts a plain http(s) base URL and returns it with a lower-case scheme and host.
func parseBitbucketServer(server string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(server, "/"))
	if err != nil {
		return nil, util.NewInvalidArgumentErrorf("invalid server URL")
	}
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, util.NewInvalidArgumentErrorf("the server must be a plain http(s) URL")
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}, nil
}

type bitbucketProject struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type bitbucketRepository struct {
	Slug  string              `json:"slug"`
	Links bitbucketCloneLinks `json:"links"`
}

// bitbucketProjectName turns a Bitbucket project name into a project name: lower case, every run of other characters
// than letters, digits, "_" and "." one "-"; the lower-case key if nothing usable is left.
func bitbucketProjectName(name, key string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.') {
			b.WriteRune(r)
		} else if s := b.String(); s != "" && !strings.HasSuffix(s, "-") {
			b.WriteByte('-')
		}
	}
	if s := strings.Trim(b.String(), "-."); s != "" && repo_model.IsUsableGroupName(s) == nil {
		return s
	}
	return strings.ToLower(key)
}

// planBitbucketImport lists the projects and repositories to import and decides, without writing, what happens to
// each: created, reused (imported before into the same place) or a conflict, which blocks the whole import.
func planBitbucketImport(ctx context.Context, d *BitbucketDataCenterDownloader, org *organization.Organization, keys []string) (*BitbucketImportReport, error) {
	server := d.baseURL.String()
	projects, err := callAPIPaged[bitbucketProject](d, "projects")
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, key := range keys {
		wanted[strings.ToUpper(key)] = false
	}
	slices.SortFunc(projects, func(a, b bitbucketProject) int { return strings.Compare(a.Key, b.Key) })

	report := &BitbucketImportReport{}
	conflict := func(item *BitbucketImportItem, reason string) {
		item.set(BitbucketConflict, reason)
		report.Blocked = true
	}
	names, filter := map[string]bool{}, len(wanted) > 0
	for _, p := range projects {
		if _, ok := wanted[strings.ToUpper(p.Key)]; filter && !ok {
			continue
		}
		wanted[strings.ToUpper(p.Key)] = true
		item := &BitbucketImportItem{Kind: "project", Bitbucket: util.StripControl(p.Key), key: p.Key, description: p.Description, Status: BitbucketPlanned}
		report.Items = append(report.Items, item)
		if !repo_model.IsValidBitbucketName(p.Key) {
			conflict(item, "the project key isn't a plain name")
			continue
		}
		item.OldWeb = d.baseURL.JoinPath("projects", p.Key).String()

		origin, err := repo_model.GetBitbucketOriginByPath(ctx, server, strings.ToUpper(p.Key), "")
		if err != nil {
			return nil, err
		}
		if origin != nil {
			// imported before: reuse the project only where it was created
			group, err := repo_model.GetRepoGroupByID(ctx, org.ID, origin.GroupID)
			if origin.OwnerID != org.ID || errors.Is(err, repo_model.ErrRepoGroupNotExist) {
				conflict(item, "imported before into another organization or a deleted project")
				continue
			} else if err != nil {
				return nil, err
			}
			item.group, item.groupName = group, group.Name
			item.set(BitbucketReused, "")
		} else {
			item.groupName = bitbucketProjectName(p.Name, p.Key)
			if taken, err := repo_model.IsNameTaken(ctx, org.ID, 0, item.groupName); err != nil {
				return nil, err
			} else if taken || names[item.groupName] {
				conflict(item, "the name is taken in the organization")
			}
		}
		names[item.groupName] = true
		item.Name = org.Name + "/" + item.groupName

		repos, err := callAPIPaged[bitbucketRepository](d, "projects/"+p.Key+"/repos")
		if err != nil {
			return nil, err
		}
		slices.SortFunc(repos, func(a, b bitbucketRepository) int { return strings.Compare(a.Slug, b.Slug) })
		for _, r := range repos {
			ritem := &BitbucketImportItem{Kind: "repository", Bitbucket: util.StripControl(p.Key + "/" + r.Slug), key: p.Key, slug: r.Slug, Status: BitbucketPlanned}
			item.repos = append(item.repos, ritem)
			report.Items = append(report.Items, ritem)
			if !repo_model.IsValidBitbucketName(r.Slug) || repo_model.IsUsableRepoName(r.Slug) != nil {
				conflict(ritem, "the repository slug isn't a usable name")
				continue
			}
			ritem.Name = item.Name + "/" + r.Slug
			ritem.OldHTTP = d.cloneURL(p.Key, r.Slug)
			ritem.OldWeb = d.baseURL.JoinPath("projects", p.Key, "repos", r.Slug, "browse").String()
			ritem.OldSSH = fmt.Sprintf("ssh://git@%s:7999/%s/%s.git", d.baseURL.Hostname(), strings.ToLower(p.Key), r.Slug)
			for _, c := range r.Links.Clone {
				if c.Name == "ssh" {
					ritem.OldSSH = util.StripControl(c.Href) // display only
				}
			}

			origin, err := repo_model.GetBitbucketOriginByPath(ctx, server, strings.ToUpper(p.Key), strings.ToLower(r.Slug))
			if err != nil {
				return nil, err
			}
			if origin != nil {
				// imported before: reuse the repository only if it is still live where it was created
				repo, err := repo_model.GetRepositoryByIDIncludingTrashed(ctx, origin.RepoID)
				if err != nil && !repo_model.IsErrRepoNotExist(err) {
					return nil, err
				}
				if repo == nil || repo.IsTrashed() || repo.OwnerID != org.ID || item.group == nil || repo.GroupID != item.group.ID {
					conflict(ritem, "imported before, but since trashed, moved or transferred")
					continue
				}
				ritem.set(BitbucketReused, "")
				ritem.NewHTTP, ritem.NewSSH, ritem.NewWeb = repo.CloneLink().HTTPS, repo.CloneLink().SSH, repo.HTMLURL()
			} else if item.group != nil {
				if taken, err := repo_model.IsNameTaken(ctx, org.ID, item.group.ID, r.Slug); err != nil {
					return nil, err
				} else if taken {
					conflict(ritem, "the name is taken in the project")
				}
			}
		}
	}
	for _, key := range keys {
		if !wanted[strings.ToUpper(key)] {
			wanted[strings.ToUpper(key)] = true // listed once
			report.Items = append(report.Items, &BitbucketImportItem{Kind: "project", Bitbucket: util.StripControl(key), Status: BitbucketConflict, Reason: "not found on the server"})
			report.Blocked = true
		}
	}
	return report, nil
}

// applyBitbucketImport creates the planned projects and repositories. A repository that fails is reported and the
// others go on; a later run resumes, because only an imported repository has an origin row.
func applyBitbucketImport(ctx context.Context, doer *user_model.User, org *organization.Organization, d *BitbucketDataCenterDownloader, report *BitbucketImportReport, opts BitbucketImportOptions) {
	server := d.baseURL.String()
	failure := func(item *BitbucketImportItem, err error) {
		msg := util.SanitizeCredentialURLs(err.Error())
		if opts.Token != "" {
			msg = strings.ReplaceAll(msg, opts.Token, "***")
		}
		item.set(BitbucketFailed, msg)
	}
	for _, item := range report.Items {
		if item.Kind != "project" || item.Status == BitbucketConflict {
			continue
		}
		if item.Status == BitbucketPlanned {
			group, err := repo_model.CreateRepoGroup(ctx, org.AsUser(), item.groupName, item.description)
			if err != nil {
				failure(item, err)
			} else if err := repo_model.InsertBitbucketOrigin(ctx, &repo_model.BitbucketOrigin{OwnerID: org.ID, GroupID: group.ID, ProjectKey: strings.ToUpper(item.key), Server: server}); err != nil {
				failure(item, fmt.Errorf("the project was created, but recording its origin failed: delete it and rerun: %w", err))
			} else {
				item.group = group
				item.set(BitbucketCreated, "")
			}
		}
		for _, ritem := range item.repos {
			if ritem.Status != BitbucketPlanned {
				continue
			}
			if item.Status == BitbucketFailed {
				ritem.set(BitbucketFailed, "its project was not created")
				continue
			}
			repo, err := MigrateRepository(ctx, doer, org.Name, base.MigrateOptions{
				CloneAddr:      ritem.OldHTTP,
				AuthUsername:   opts.Username,
				AuthToken:      opts.Token,
				RepoName:       ritem.slug,
				GroupID:        item.group.ID,
				Private:        true,
				GitServiceType: structs.BitbucketDataCenterService,
				PullRequests:   opts.PullRequests,
				Comments:       opts.PullRequests,
			}, nil)
			if err != nil {
				failure(ritem, err)
				continue
			}
			ritem.NewHTTP, ritem.NewSSH, ritem.NewWeb = repo.CloneLink().HTTPS, repo.CloneLink().SSH, repo.HTMLURL()
			if err := repo_model.InsertBitbucketOrigin(ctx, &repo_model.BitbucketOrigin{RepoID: repo.ID, ProjectKey: strings.ToUpper(ritem.key), Slug: strings.ToLower(ritem.slug), Server: server}); err != nil {
				failure(ritem, fmt.Errorf("the repository was created, but recording its origin failed: delete it and rerun: %w", err))
				continue
			}
			ritem.set(BitbucketCreated, "")
		}
	}
}
