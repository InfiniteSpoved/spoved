// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package migrations

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"forgejo.org/models"
	asymkey_model "forgejo.org/models/asymkey"
	"forgejo.org/models/db"
	"forgejo.org/models/organization"
	"forgejo.org/models/perm"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	repo_module "forgejo.org/modules/repository"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"
	"forgejo.org/services/authz"
	repo_service "forgejo.org/services/repository"
	"forgejo.org/services/usergroup"
)

// bitbucketPermission is what an item of kind team, member, group, collaborator or key needs to be applied.
type bitbucketPermission struct {
	perms     []*BitbucketImportItem // of a project: everything to apply after its repositories, in order
	parent    *BitbucketImportItem   // the project or repository of a team, collaborator or key; the team of a member or group
	mode      perm.AccessMode
	user      *user_model.User
	userGroup *organization.UserGroup
	team      *organization.Team
	content   string // the public key
	expires   timeutil.TimeStamp
}

type bitbucketUserGrant struct {
	User struct {
		Name         string `json:"name"`
		EmailAddress string `json:"emailAddress"`
		Active       bool   `json:"active"`
	} `json:"user"`
	Permission string `json:"permission"`
}

type bitbucketGroupGrant struct {
	Group struct {
		Name string `json:"name"`
	} `json:"group"`
	Permission string `json:"permission"`
}

type bitbucketAccessKey struct {
	Key struct {
		ID          int64  `json:"id"`
		Text        string `json:"text"`
		Label       string `json:"label"`
		CreatedDate int64  `json:"createdDate"` // milliseconds
		ExpiryDays  int64  `json:"expiryDays"`
	} `json:"key"`
	Permission string `json:"permission"`
}

// bitbucketAccess maps a Bitbucket permission to the access it gets: read stays read, and admin becomes write because
// imported teams carry Code and pull requests only. Anything else gets nothing.
func bitbucketAccess(permission string) (perm.AccessMode, string) {
	switch permission {
	case "PROJECT_READ", "REPO_READ":
		return perm.AccessModeRead, ""
	case "PROJECT_WRITE", "REPO_WRITE":
		return perm.AccessModeWrite, ""
	case "PROJECT_ADMIN", "REPO_ADMIN":
		return perm.AccessModeWrite, "admin mapped to write"
	}
	return perm.AccessModeNone, ""
}

// bitbucketKeyExpiry is the earliest of the expiries that are set (not 0), or 0 if none is.
func bitbucketKeyExpiry(expiries ...timeutil.TimeStamp) timeutil.TimeStamp {
	var earliest timeutil.TimeStamp
	for _, e := range expiries {
		if e != 0 && (earliest == 0 || e < earliest) {
			earliest = e
		}
	}
	return earliest
}

// bitbucketUnmatched is the reason code of a Bitbucket user without a spoved user.
type bitbucketUnmatched string

func (e bitbucketUnmatched) Error() string { return string(e) }

// matchBitbucketUser finds the spoved user of a Bitbucket user: the login is the Bitbucket name, and the Bitbucket
// email is an activated email of that same user, who is an active individual allowed to sign in. Otherwise it returns
// a bitbucketUnmatched reason code; the email is never reported.
func matchBitbucketUser(ctx context.Context, g bitbucketUserGrant) (*user_model.User, error) {
	if !g.User.Active {
		return nil, bitbucketUnmatched("bb_inactive")
	}
	u, err := user_model.GetUserByName(ctx, g.User.Name)
	if user_model.IsErrUserNotExist(err) || (err == nil && !u.IsIndividual()) {
		return nil, bitbucketUnmatched("no_user")
	} else if err != nil {
		return nil, err
	}
	if !u.IsActive || u.ProhibitLogin {
		return nil, bitbucketUnmatched("inactive")
	}
	if g.User.EmailAddress == "" {
		return nil, bitbucketUnmatched("email_mismatch")
	}
	email, err := user_model.GetEmailAddressByEmail(ctx, g.User.EmailAddress)
	if user_model.IsErrEmailAddressNotExist(err) || (err == nil && email.UID != u.ID) {
		return nil, bitbucketUnmatched("email_mismatch")
	} else if err != nil {
		return nil, err
	}
	if !email.IsActivated {
		return nil, bitbucketUnmatched("email_not_activated")
	}
	return u, nil
}

// bitbucketTeamShape reports whether an existing team has exactly the shape the importer gives a team: the mode on
// Code and pull requests and nothing else, and linked to the project (groupID) or holding only the repository (repoID).
func bitbucketTeamShape(ctx context.Context, team *organization.Team, mode perm.AccessMode, groupID, repoID int64) (bool, error) {
	if team.AccessMode != mode || team.IncludesAllRepositories || team.CanCreateOrgRepo {
		return false, nil
	}
	if err := team.LoadUnits(ctx); err != nil {
		return false, err
	}
	found := 0
	for _, u := range team.Units {
		switch {
		case u.Type == unit.TypeCode || u.Type == unit.TypePullRequests:
			if u.AccessMode != mode {
				return false, nil
			}
			found++
		case u.AccessMode != perm.AccessModeNone:
			return false, nil
		}
	}
	if found != 2 {
		return false, nil
	}
	var groupIDs, repoIDs []int64
	if err := db.GetEngine(ctx).Table("team_group").Where("team_id = ?", team.ID).Cols("group_id").Find(&groupIDs); err != nil {
		return false, err
	}
	if err := db.GetEngine(ctx).Table("team_repo").Where("team_id = ? AND via_group = ?", team.ID, false).Cols("repo_id").Find(&repoIDs); err != nil {
		return false, err
	}
	if groupID != 0 {
		return slices.Equal(groupIDs, []int64{groupID}) && len(repoIDs) == 0, nil
	}
	return len(groupIDs) == 0 && slices.Equal(repoIDs, []int64{repoID}), nil
}

// bitbucketPlanner plans the permissions and keys of one project.
type bitbucketPlanner struct {
	ctx       context.Context
	d         *BitbucketDataCenterDownloader
	org       *organization.Organization
	report    *BitbucketImportReport
	project   *BitbucketImportItem
	keyExpiry timeutil.TimeStamp
	teams     map[string]*BitbucketImportItem
}

func (p *bitbucketPlanner) add(item *BitbucketImportItem, status, reason string) {
	item.set(status, reason)
	if status == BitbucketConflict {
		p.report.Blocked = true
	}
	p.project.perms = append(p.project.perms, item)
	p.report.Items = append(p.report.Items, item)
}

func (p *bitbucketPlanner) unmapped(owner *BitbucketImportItem, what string) {
	p.add(&BitbucketImportItem{Kind: "permission", Bitbucket: util.StripControl(what), Target: owner.Name}, BitbucketSkipped, "unmapped")
}

// planBitbucketPermissions plans, without writing, the teams, members, user group links, collaborators and deploy
// keys of a project and its repositories.
func planBitbucketPermissions(ctx context.Context, d *BitbucketDataCenterDownloader, org *organization.Organization, report *BitbucketImportReport, project *BitbucketImportItem, keyExpiry timeutil.TimeStamp) error {
	p := &bitbucketPlanner{ctx: ctx, d: d, org: org, report: report, project: project, keyExpiry: keyExpiry, teams: map[string]*BitbucketImportItem{}}
	if err := p.plan(project, "api/1.0/projects/"+project.key); err != nil {
		return err
	}
	for _, mode := range []string{"PROJECT_READ", "PROJECT_WRITE"} {
		var all struct {
			Permitted bool `json:"permitted"`
		}
		if err := d.callAPI("api/1.0/projects/"+project.key+"/permissions/"+mode+"/all", nil, &all); err != nil {
			return err
		}
		if all.Permitted {
			p.unmapped(project, project.key+" default "+mode)
		}
	}
	for _, repo := range project.repos {
		if repo.Status == BitbucketConflict {
			continue
		}
		if err := p.plan(repo, "api/1.0/projects/"+project.key+"/repos/"+repo.slug); err != nil {
			return err
		}
	}
	return p.planKeys(project, "keys/1.0/projects/"+project.key+"/ssh")
}

// plan plans the user and group grants of a project or repository, and its repository keys.
func (p *bitbucketPlanner) plan(owner *BitbucketImportItem, endpoint string) error {
	if owner.public {
		p.unmapped(owner, owner.Bitbucket+" public")
	}
	users, err := callAPIPaged[bitbucketUserGrant](p.d, endpoint+"/permissions/users")
	if err != nil {
		return err
	}
	groups, err := callAPIPaged[bitbucketGroupGrant](p.d, endpoint+"/permissions/groups")
	if err != nil {
		return err
	}
	for _, g := range users {
		mode, note := bitbucketAccess(g.Permission)
		if mode == perm.AccessModeNone {
			p.unmapped(owner, g.User.Name+" "+g.Permission)
			continue
		}
		if err := p.planUser(owner, g, mode, note); err != nil {
			return err
		}
	}
	for _, g := range groups {
		mode, note := bitbucketAccess(g.Permission)
		if mode == perm.AccessModeNone {
			p.unmapped(owner, g.Group.Name+" "+g.Permission)
			continue
		}
		team, err := p.planTeam(owner, mode)
		if err != nil {
			return err
		}
		item := &BitbucketImportItem{Kind: "group", Bitbucket: util.StripControl(g.Group.Name), Target: team.Name, Access: mode.String(), bitbucketPermission: bitbucketPermission{parent: team}}
		group, err := organization.GetUserGroupByName(p.ctx, g.Group.Name)
		if organization.IsErrUserGroupNotExist(err) || (err == nil && group.Name != g.Group.Name) {
			p.add(item, BitbucketSkipped, "no user group")
			continue
		} else if err != nil {
			return err
		}
		item.Name, item.userGroup = group.Name, group
		status := BitbucketPlanned
		if team.team != nil {
			if linked, err := organization.IsUserGroupLinkedToTeam(p.ctx, group.ID, team.team.ID); err != nil {
				return err
			} else if linked {
				status = BitbucketReused
			}
		}
		p.add(item, status, note)
	}
	if owner.Kind == "repository" {
		return p.planKeys(owner, "keys/1.0/projects/"+p.project.key+"/repos/"+owner.slug+"/ssh")
	}
	return nil
}

// planTeam plans the team of a project (linked to it) or of a repository (holding only it) at a mode, once.
func (p *bitbucketPlanner) planTeam(owner *BitbucketImportItem, mode perm.AccessMode) (*BitbucketImportItem, error) {
	name := p.project.groupName
	if owner != p.project {
		name += "-" + owner.slug
	}
	name += "-" + mode.String()
	if team, ok := p.teams[name]; ok {
		return team, nil
	}
	item := &BitbucketImportItem{Kind: "team", Name: name, Target: owner.Name, Access: mode.String(), bitbucketPermission: bitbucketPermission{parent: owner, mode: mode}}
	p.teams[name] = item
	team, err := organization.GetTeam(p.ctx, p.org.ID, name)
	if organization.IsErrTeamNotExist(err) {
		if len(name) > 255 {
			p.add(item, BitbucketConflict, "the team name is too long")
		} else {
			p.add(item, BitbucketPlanned, "")
		}
		return item, nil
	} else if err != nil {
		return nil, err
	}
	// reuse only a team of exactly the importer's shape, so that adding members never widens its access
	same := false
	if owner.group != nil {
		same, err = bitbucketTeamShape(p.ctx, team, mode, owner.group.ID, 0)
	} else if owner.repo != nil {
		same, err = bitbucketTeamShape(p.ctx, team, mode, 0, owner.repo.ID)
	}
	if err != nil {
		return nil, err
	}
	if !same {
		p.add(item, BitbucketConflict, "a team of this name exists and isn't one this import created")
		return item, nil
	}
	item.team = team
	p.add(item, BitbucketReused, "")
	return item, nil
}

// planUser plans a member of a project team, or a collaborator of a repository.
func (p *bitbucketPlanner) planUser(owner *BitbucketImportItem, g bitbucketUserGrant, mode perm.AccessMode, note string) error {
	item := &BitbucketImportItem{Kind: "collaborator", Bitbucket: util.StripControl(g.User.Name), Target: owner.Name, Access: mode.String(), bitbucketPermission: bitbucketPermission{parent: owner, mode: mode}}
	var team *BitbucketImportItem
	if owner == p.project {
		var err error
		if team, err = p.planTeam(owner, mode); err != nil {
			return err
		}
		item.Kind, item.Target, item.parent = "member", team.Name, team
	}
	u, err := matchBitbucketUser(p.ctx, g)
	if unmatched, ok := errors.AsType[bitbucketUnmatched](err); ok {
		p.add(item, BitbucketSkipped, string(unmatched))
		return nil
	} else if err != nil {
		return err
	}
	item.Name, item.user = u.Name, u
	if blocked, err := u.TwoFactorBlocked(p.ctx, p.org.AsUser()); err != nil {
		return err
	} else if blocked {
		note = strings.TrimPrefix(note+"; blocked until two-factor is enabled", "; ")
	}

	if team != nil {
		if member, err := organization.IsOrganizationMember(p.ctx, p.org.ID, u.ID); err != nil {
			return err
		} else if !member {
			if err := organization.CheckVerifiedDomainGate(p.ctx, p.org.ID, u.ID); organization.IsErrUserNoVerifiedDomainEmail(err) {
				p.add(item, BitbucketSkipped, "no verified-domain email")
				return nil
			} else if err != nil {
				return err
			}
		}
		status := BitbucketPlanned
		if team.team != nil {
			if member, err := organization.IsTeamMember(p.ctx, p.org.ID, team.team.ID, u.ID); err != nil {
				return err
			} else if member {
				status = BitbucketReused
			}
		}
		p.add(item, status, note)
		return nil
	}

	if owner, err := organization.IsOrganizationOwner(p.ctx, p.org.ID, u.ID); err != nil {
		return err
	} else if owner {
		p.add(item, BitbucketSkipped, "an owner of the organization")
		return nil
	}
	if user_model.IsBlocked(p.ctx, p.org.ID, u.ID) || user_model.IsBlocked(p.ctx, u.ID, p.org.ID) {
		p.add(item, BitbucketSkipped, "blocked")
		return nil
	}
	status := BitbucketPlanned
	if owner.repo != nil {
		c := &repo_model.Collaboration{RepoID: owner.repo.ID, UserID: u.ID}
		if has, err := db.GetEngine(p.ctx).Get(c); err != nil {
			return err
		} else if has && c.Mode == mode {
			status = BitbucketReused
		} else if has {
			p.add(item, BitbucketSkipped, "an existing collaborator, kept as is")
			return nil
		}
	}
	p.add(item, status, note)
	return nil
}

// planKeys plans the access keys of a project or repository as deploy keys, each with an expiry.
func (p *bitbucketPlanner) planKeys(owner *BitbucketImportItem, endpoint string) error {
	keys, err := callAPIPaged[bitbucketAccessKey](p.d, endpoint)
	if err != nil {
		return err
	}
	for _, k := range keys {
		item := &BitbucketImportItem{Kind: "key", Bitbucket: util.StripControl(fmt.Sprintf("%s key %d", owner.Bitbucket, k.Key.ID)), Target: owner.Name, bitbucketPermission: bitbucketPermission{parent: owner}}
		item.Name = strings.TrimSpace(util.StripControl(k.Key.Label))
		if item.Name == "" {
			item.Name = fmt.Sprintf("bitbucket-%d", k.Key.ID)
		}
		switch k.Permission {
		case "PROJECT_READ", "REPO_READ":
			item.mode = perm.AccessModeRead
		case "PROJECT_WRITE", "REPO_WRITE":
			item.mode = perm.AccessModeWrite
		default:
			p.add(item, BitbucketSkipped, "unmapped")
			continue
		}
		item.Access = item.mode.String()
		if setting.SSH.Disabled {
			p.add(item, BitbucketSkipped, "SSH is disabled")
			continue
		}
		if item.content, err = asymkey_model.CheckPublicKeyString(k.Key.Text); err != nil {
			p.add(item, BitbucketSkipped, "not a valid public key")
			continue
		}
		var bitbucket timeutil.TimeStamp
		if k.Key.CreatedDate > 0 && k.Key.ExpiryDays > 0 {
			bitbucket = timeutil.TimeStamp(k.Key.CreatedDate/1000 + k.Key.ExpiryDays*86400)
			if bitbucket <= timeutil.TimeStampNow() {
				p.add(item, BitbucketSkipped, "expired in Bitbucket")
				continue
			}
		}
		item.expires = bitbucketKeyExpiry(bitbucket, authz.MaxExpiry(setting.DeployKeyExpiry), p.keyExpiry)
		if item.expires == 0 {
			p.add(item, BitbucketConflict, "no expiry: pass --key-expiry")
			continue
		}
		if err := authz.ValidateExpiry(setting.DeployKeyExpiry, item.expires); err != nil {
			p.add(item, BitbucketConflict, err.Error())
			continue
		}
		item.Expires = item.expires.FormatDate()

		fingerprint, err := asymkey_model.CalcFingerprint(item.content)
		if err != nil {
			p.add(item, BitbucketSkipped, "not a valid public key")
			continue
		}
		if pkey, err := asymkey_model.SearchPublicKeyByContentExact(p.ctx, item.content); err == nil && pkey.Type != asymkey_model.KeyTypeDeploy {
			p.add(item, BitbucketSkipped, "used as a user key")
			continue
		} else if err != nil && !asymkey_model.IsErrKeyNotExist(err) {
			return err
		}
		var exists bool
		if owner.group != nil && owner == p.project {
			exists, err = db.GetEngine(p.ctx).Exist(&asymkey_model.OrgDeployKey{OrgID: p.org.ID, GroupID: owner.group.ID, Fingerprint: fingerprint})
		} else if owner.repo != nil {
			exists, err = db.GetEngine(p.ctx).Exist(&asymkey_model.DeployKey{RepoID: owner.repo.ID, Fingerprint: fingerprint})
		}
		if err != nil {
			return err
		}
		if exists {
			p.add(item, BitbucketReused, "")
		} else {
			p.add(item, BitbucketPlanned, "")
		}
	}
	return nil
}

// applyBitbucketPermissions applies the planned permissions and keys of a project once its repositories were
// imported. Each item is applied on its own: one that fails is reported and the others go on.
func applyBitbucketPermissions(ctx context.Context, doer *user_model.User, org *organization.Organization, project *BitbucketImportItem, failure func(*BitbucketImportItem, error)) {
	for _, item := range project.perms {
		if item.Status != BitbucketPlanned {
			continue
		}
		if ctx.Err() != nil {
			item.set(BitbucketFailed, "the import was cancelled")
			continue
		}
		parent := item.parent
		var owner *BitbucketImportItem // the project or repository the grant needs
		switch item.Kind {
		case "team", "collaborator", "key":
			owner = parent
		}
		if owner != nil && owner.group == nil && owner.repo == nil {
			item.set(BitbucketSkipped, "its "+owner.Kind+" was not imported")
			continue
		}
		if (item.Kind == "member" || item.Kind == "group") && parent.team == nil {
			item.set(BitbucketSkipped, "its team was not created")
			continue
		}

		var err error
		status := BitbucketCreated
		switch item.Kind {
		case "team":
			team := &organization.Team{OrgID: org.ID, Name: item.Name, AccessMode: item.mode, Units: []*organization.TeamUnit{
				{OrgID: org.ID, Type: unit.TypeCode, AccessMode: item.mode},
				{OrgID: org.ID, Type: unit.TypePullRequests, AccessMode: item.mode},
			}}
			err = db.WithTx(ctx, func(ctx context.Context) error {
				if err := models.NewTeam(ctx, team); err != nil {
					return err
				}
				if owner.Kind == "project" {
					return repo_service.LinkTeamToRepoGroup(ctx, doer, org, team.ID, owner.group.ID)
				}
				return models.AddRepository(ctx, team, owner.repo)
			})
			if err == nil {
				item.team = team
			}
		case "member":
			err = models.AddTeamMemberByCooptation(ctx, parent.team, item.user.ID, doer.ID)
			if organization.IsErrUserNoVerifiedDomainEmail(err) {
				status, err = BitbucketSkipped, nil
				item.Reason = "no verified-domain email"
			}
		case "group":
			err = usergroup.LinkTeam(ctx, item.userGroup, parent.team)
			if errors.Is(err, usergroup.ErrAlreadyLinked) {
				status, err = BitbucketReused, nil
			}
		case "collaborator":
			// in one transaction: AddCollaborator grants write before the mode is set
			err = db.WithTx(ctx, func(ctx context.Context) error {
				if has, err := repo_model.IsCollaborator(ctx, owner.repo.ID, item.user.ID); err != nil || has {
					status, item.Reason = BitbucketSkipped, "an existing collaborator, kept as is"
					return err
				}
				if err := repo_module.AddCollaborator(ctx, owner.repo, item.user); err != nil {
					return err
				}
				return repo_module.ChangeCollaborationAccessMode(ctx, owner.repo, item.user.ID, item.mode)
			})
			if errors.Is(err, user_model.ErrBlockedByUser) {
				status, err = BitbucketSkipped, nil
			}
		case "key":
			readOnly := item.mode == perm.AccessModeRead
			if owner.Kind == "project" {
				_, err = asymkey_model.AddOrgDeployKey(ctx, org.ID, owner.group.ID, item.Name, item.content, readOnly, false, nil, item.expires)
			} else {
				_, err = asymkey_model.AddDeployKey(ctx, owner.repo.ID, item.Name, item.content, readOnly, item.expires)
			}
			switch {
			case asymkey_model.IsErrDeployKeyAlreadyExist(err):
				status, err = BitbucketReused, nil
			case asymkey_model.IsErrKeyAlreadyExist(err):
				status, err = BitbucketSkipped, nil
				item.Reason = "used as a user key"
			case asymkey_model.IsErrDeployKeyNameAlreadyUsed(err), asymkey_model.IsErrKeyNameAlreadyUsed(err):
				status, err = BitbucketSkipped, nil
				item.Reason = "the name is used"
			}
		}
		if err != nil {
			failure(item, err)
			continue
		}
		item.Status = status
	}
}
