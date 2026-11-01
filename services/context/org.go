// Copyright 2014 The Gogs Authors. All rights reserved.
// Copyright 2020 The Gitea Authors. All rights reserved.
// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package context

import (
	"net/http"
	"slices"
	"strings"

	"forgejo.org/models/organization"
	"forgejo.org/models/perm"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/markup"
	"forgejo.org/modules/markup/markdown"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/structs"
	apiv1_permissions "forgejo.org/routers/api/v1/permissions"
	"forgejo.org/services/auth"
	dependency_service "forgejo.org/services/dependency"
	redirect_service "forgejo.org/services/redirect"
	vulnerability_service "forgejo.org/services/vulnerability"

	"github.com/go-chi/chi/v5"
)

// IsArchivedOrgWrite reports whether req writes to the archived organization owner. Routes listed in exempt
// ("METHOD /route/{pattern}", as the router registered them) stay open; matching the route pattern means a
// differently spelled path cannot slip past or into the exemption.
func IsArchivedOrgWrite(req *http.Request, owner *user_model.User, exempt ...string) bool {
	if !owner.IsOrganization() || !owner.IsArchived {
		return false
	}
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return !slices.Contains(exempt, req.Method+" "+chi.RouteContext(req.Context()).RoutePattern())
}

// ArchivedOrgOpenRoutes are the org and repository routes that stay writable while the organization is archived.
var ArchivedOrgOpenRoutes = append([]string{
	"POST /org/{org}/settings/unarchive",
	"POST /org/{org}/settings/delete",
	"POST /org/{org}/security/vulnerabilities/export", // a download is audited, it changes nothing
	"POST /org/{org}/security/dependencies/export",
	"POST /org/{org}/security/secrets/export",
	"POST /api/v1/orgs/{org}/unarchive",
	"DELETE /api/v1/orgs/{org}",
}, apiv1_permissions.CredentialRevokeRoutes...)

// secretAlertsExportRoute is the download of the organization's secret alert list. An auditor posts it as a read:
// it changes nothing and is audited, as admin.AuditExportReq on the instance page.
const secretAlertsExportRoute = "/org/{org}/security/secrets/export"

// isAuditorSecretExport tells whether the request is an auditor's download of the secret alert list, with their own
// credential.
func (ctx *Context) isAuditorSecretExport() bool {
	return ctx.IsSigned && ctx.Doer.IsAuditor && auth.IsHeldByUser(ctx.Authentication) &&
		ctx.Req.Method == http.MethodPost && chi.RouteContext(ctx.Req.Context()).RoutePattern() == secretAlertsExportRoute
}

// setSecretAlertsAccess decides who opens the organization's list of secret alerts (see Organization.SecretAlerts).
// A member blocked by the organization's two-factor requirement is a stranger there, as for its repositories.
func (ctx *Context) setSecretAlertsAccess(org *organization.Organization) {
	ctx.Data["EnableSecretAlerts"] = false
	if !ctx.IsSigned {
		return
	}
	switch {
	case ctx.Org.IsOwner: // a site admin too
		ctx.Org.SecretAlerts = true
	case vulnerability_service.AuditorView(ctx, ctx.Doer, org.AsUser(), ctx.IsReadOnlyMethod() || ctx.isAuditorSecretExport(), auth.IsHeldByUser(ctx.Authentication)):
		ctx.Org.SecretAlerts = true
	case ctx.Org.IsMember && ctx.Data["TwoFactorBlockedMember"] != true:
		ctx.Org.SecretAlerts, ctx.Org.SecretAlertsNarrow = true, true
	}
	ctx.Data["EnableSecretAlerts"] = ctx.Org.SecretAlerts
}

// Organization contains organization context
type Organization struct {
	IsOwner          bool
	IsMember         bool
	IsTeamMember     bool // Is member of team.
	IsTeamAdmin      bool // In owner team or team that has admin permission level.
	Organization     *organization.Organization
	OrgLink          string
	CanCreateOrgRepo bool
	// SecretAlerts says the viewer may open the organization's list of secret alerts (a banner while secret scanning
	// is off): owners, site admins, auditors (read-only, and the audited download) and members. SecretAlertsNarrow is a member who is none of the others:
	// the list holds only the repositories where they can write the code.
	SecretAlerts, SecretAlertsNarrow bool

	Team  *organization.Team
	Teams []*organization.Team
}

func (org *Organization) CanWriteUnit(ctx *Context, unitType unit.Type) bool {
	return org.Organization.UnitPermission(ctx, ctx.Doer, unitType) >= perm.AccessModeWrite
}

func (org *Organization) CanReadUnit(ctx *Context, unitType unit.Type) bool {
	return org.Organization.UnitPermission(ctx, ctx.Doer, unitType) >= perm.AccessModeRead
}

func GetOrganizationByParams(ctx *Context) {
	orgName := ctx.Params(":org")

	var err error

	ctx.Org.Organization, err = organization.GetOrgByName(ctx, orgName)
	if err != nil {
		if organization.IsErrOrgNotExist(err) {
			redirectUserID, err := redirect_service.LookupUserRedirect(ctx, ctx.Doer, orgName)
			if err == nil {
				RedirectToUser(ctx.Base, orgName, redirectUserID)
			} else if user_model.IsErrUserRedirectNotExist(err) {
				ctx.NotFound("GetUserByName", err)
			} else {
				ctx.ServerError("LookupRedirect", err)
			}
		} else {
			ctx.ServerError("GetUserByName", err)
		}
		return
	}
}

func ensureIsOrg(ctx *Context) bool {
	switch {
	// Getting Organization from params
	case ctx.ContextUser == nil:
		if ctx.Org.Organization == nil {
			GetOrganizationByParams(ctx)
		}
		return !ctx.Written()
	// Getting Organization from ContextUser
	case ctx.ContextUser.IsOrganization():
		if ctx.Org == nil {
			ctx.Org = &Organization{}
		}
		ctx.Org.Organization = (*organization.Organization)(ctx.ContextUser)
		return true
	}
	// ContextUser is an individual User
	return false
}

func EnsureOrg() func(*Context) {
	return func(ctx *Context) {
		ensureIsOrg(ctx)
	}
}

// HandleOrgAssignment handles organization assignment
func HandleOrgAssignment(ctx *Context, args ...bool) {
	var (
		requireMember     bool
		requireOwner      bool
		requireTeamMember bool
		requireTeamAdmin  bool
	)
	if len(args) >= 1 {
		requireMember = args[0]
	}
	if len(args) >= 2 {
		requireOwner = args[1]
	}
	if len(args) >= 3 {
		requireTeamMember = args[2]
	}
	if len(args) >= 4 {
		requireTeamAdmin = args[3]
	}

	var err error

	if !ensureIsOrg(ctx) {
		return
	}
	org := ctx.Org.Organization

	// Handle Visibility
	if org.Visibility != structs.VisibleTypePublic && !ctx.IsSigned {
		// We must be signed in to see limited or private organizations
		ctx.NotFound("OrgAssignment", err)
		return
	}

	// The auditor role shows private organizations read-only, but never makes the user a member.
	if org.Visibility == structs.VisibleTypePrivate && !ctx.IsAuditorView() && !ctx.isAuditorSecretExport() {
		requireMember = true
	} else if ctx.IsSigned && ctx.Doer.IsRestricted {
		requireMember = true
	}

	ctx.ContextUser = org.AsUser()
	ctx.Data["Org"] = org

	// Admin has super access.
	if ctx.IsSigned && ctx.Doer.IsAdmin {
		ctx.Org.IsOwner = true
		ctx.Org.IsMember = true
		ctx.Org.IsTeamMember = true
		ctx.Org.IsTeamAdmin = true
		ctx.Org.CanCreateOrgRepo = true
	} else if ctx.IsSigned {
		ctx.Org.IsOwner, err = org.IsActingOwner(ctx, ctx.Doer)
		if err != nil {
			ctx.ServerError("IsActingOwner", err)
			return
		}

		if ctx.Org.IsOwner {
			ctx.Org.IsMember = true
			ctx.Org.IsTeamMember = true
			ctx.Org.IsTeamAdmin = true
			ctx.Org.CanCreateOrgRepo = true
		} else {
			ctx.Org.IsMember, err = org.IsOrgMember(ctx, ctx.Doer.ID)
			if err != nil {
				ctx.ServerError("IsOrgMember", err)
				return
			}
			ctx.Org.CanCreateOrgRepo, err = org.CanCreateOrgRepo(ctx, ctx.Doer.ID)
			if err != nil {
				ctx.ServerError("CanCreateOrgRepo", err)
				return
			}
		}
	} else {
		// Fake data.
		ctx.Data["SignedUser"] = &user_model.User{}
	}
	if (requireMember && !ctx.Org.IsMember) ||
		(requireOwner && !ctx.Org.IsOwner) {
		ctx.NotFound("OrgAssignment", err)
		return
	}
	if IsArchivedOrgWrite(ctx.Req, org.AsUser(), ArchivedOrgOpenRoutes...) {
		ctx.Error(http.StatusForbidden, ctx.Locale.TrString("org.archived.readonly"))
		return
	}
	ctx.Data["IsOrganizationOwner"] = ctx.Org.IsOwner
	if ctx.IsSigned && ctx.Org.IsMember && !ctx.Doer.IsAdmin {
		blocked, err := ctx.Doer.TwoFactorBlocked(ctx, org.AsUser())
		if err != nil {
			ctx.ServerError("TwoFactorBlocked", err)
			return
		}
		ctx.Data["TwoFactorBlockedMember"] = blocked
	}
	ctx.Data["IsOrganizationMember"] = ctx.Org.IsMember
	ctx.Data["IsPackageEnabled"] = setting.Packages.Enabled
	ctx.Data["EnableActions"] = repo_model.ActionsAvailable(ctx, ctx.ContextUser)
	ctx.setSecretAlertsAccess(org)
	ctx.Data["EnableSecurity"] = ctx.Org.IsOwner && dependency_service.OrgEnabled(ctx, org.ID) // the Security tab: owners, while the dependency graph is on
	ctx.Data["IsRepoIndexerEnabled"] = setting.Indexer.RepoIndexerEnabled
	ctx.Data["IsModerationEnabled"] = setting.Moderation.Enabled
	ctx.Data["IsPublicMember"] = func(uid int64) bool {
		is, _ := organization.IsPublicMembership(ctx, ctx.Org.Organization.ID, uid)
		return is
	}
	ctx.Data["CanCreateOrgRepo"] = ctx.Org.CanCreateOrgRepo

	ctx.Org.OrgLink = org.AsUser().OrganisationLink()
	ctx.Data["OrgLink"] = ctx.Org.OrgLink

	// Member
	opts := &organization.FindOrgMembersOpts{
		Doer:         ctx.Doer,
		OrgID:        org.ID,
		IsDoerMember: ctx.Org.IsMember || ctx.IsAuditorView(), // the auditor role lists concealed members too
	}
	ctx.Data["NumMembers"], err = organization.CountOrgMembers(ctx, opts)
	if err != nil {
		ctx.ServerError("CountOrgMembers", err)
		return
	}

	// Team.
	shouldSeeAllTeams := false
	if ctx.Org.IsMember {
		if ctx.Org.IsOwner {
			shouldSeeAllTeams = true
		} else {
			teams, err := org.GetUserTeams(ctx, ctx.Doer.ID)
			if err != nil {
				ctx.ServerError("GetUserTeams", err)
				return
			}
			for _, team := range teams {
				if team.IncludesAllRepositories && team.AccessMode >= perm.AccessModeAdmin {
					shouldSeeAllTeams = true
					break
				}
			}
		}
		ctx.Org.Teams, err = org.LoadTeams(ctx)
		if err != nil {
			ctx.ServerError("LoadTeams", err)
			return
		}
		ctx.Data["NumTeams"] = len(ctx.Org.Teams)
	}

	teamName := ctx.Params(":team")
	if len(teamName) > 0 {
		teamExists := false
		for _, team := range ctx.Org.Teams {
			if team.LowerName == strings.ToLower(teamName) {
				teamExists = true
				ctx.Org.Team = team
				ctx.Org.IsTeamMember = team.IsMember(ctx, ctx.Doer.ID)
				ctx.Data["Team"] = ctx.Org.Team
				break
			}
		}

		if !teamExists {
			ctx.NotFound("OrgAssignment", err)
			return
		}

		ctx.Data["IsTeamMember"] = ctx.Org.IsTeamMember
		if requireTeamMember && !ctx.Org.IsTeamMember && !shouldSeeAllTeams {
			ctx.NotFound("OrgAssignment", err)
			return
		}

		ctx.Org.IsTeamAdmin = ctx.Org.Team.IsOwnerTeam() || ctx.Org.Team.AccessMode >= perm.AccessModeAdmin
		if ctx.Org.IsTeamAdmin && ctx.IsSigned {
			// a member the organization's two-factor requirement blocks can't manage teams
			blocked, err := ctx.Doer.TwoFactorBlocked(ctx, org.AsUser())
			if err != nil {
				ctx.ServerError("TwoFactorBlocked", err)
				return
			}
			ctx.Org.IsTeamAdmin = !blocked
		}
		ctx.Data["IsTeamAdmin"] = ctx.Org.IsTeamAdmin
		if requireTeamAdmin && !ctx.Org.IsTeamAdmin {
			ctx.NotFound("OrgAssignment", err)
			return
		}
	}
	ctx.Data["ContextUser"] = ctx.ContextUser

	ctx.Data["CanReadProjects"] = ctx.Org.CanReadUnit(ctx, unit.TypeProjects)
	ctx.Data["CanReadPackages"] = ctx.Org.CanReadUnit(ctx, unit.TypePackages)
	ctx.Data["CanReadCode"] = ctx.Org.CanReadUnit(ctx, unit.TypeCode)

	domains, err := organization.GetVerifiedDomains(ctx, org.ID)
	if err != nil {
		ctx.ServerError("GetVerifiedDomains", err)
		return
	}
	ctx.Data["VerifiedDomains"] = domains[:min(len(domains), 3)]

	ctx.Data["IsFollowing"] = ctx.Doer != nil && user_model.IsFollowing(ctx, ctx.Doer.ID, ctx.ContextUser.ID)
	if len(ctx.ContextUser.Description) != 0 {
		content, err := markdown.RenderString(&markup.RenderContext{
			Metas: map[string]string{"mode": "document"},
			Ctx:   ctx,
		}, ctx.ContextUser.Description)
		if err != nil {
			ctx.ServerError("RenderString", err)
			return
		}
		ctx.Data["RenderedDescription"] = content
	}
}

// OrgAssignment returns a middleware to handle organization assignment
func OrgAssignment(args ...bool) func(ctx *Context) {
	return func(ctx *Context) {
		HandleOrgAssignment(ctx, args...)
	}
}
