// Copyright 2017 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package swagger

import (
	api "forgejo.org/modules/structs"
)

// Organization
// swagger:response Organization
type swaggerResponseOrganization struct {
	// in:body
	Body api.Organization `json:"body"`
}

// OrgBranchWorkflow
// swagger:response OrgBranchWorkflow
type swaggerResponseOrgBranchWorkflow struct {
	// in:body
	Body api.OrgBranchWorkflow `json:"body"`
}

// OrgBranchingModel
// swagger:response OrgBranchingModel
type swaggerResponseOrgBranchingModel struct {
	// in:body
	Body api.OrgBranchingModel `json:"body"`
}

// OrganizationList
// swagger:response OrganizationList
type swaggerResponseOrganizationList struct {
	// in:body
	Body []api.Organization `json:"body"`

	// The total number of organizations
	TotalCount int64 `json:"X-Total-Count"`
}

// OrganizationListWithoutPagination - Organizations without pagination headers
// swagger:response OrganizationListWithoutPagination
type swaggerOrganizationListWithoutPagination struct {
	// in:body
	Body []api.Organization `json:"body"`
}

// Team
// swagger:response Team
type swaggerResponseTeam struct {
	// in:body
	Body api.Team `json:"body"`
}

// TeamList
// swagger:response TeamList
type swaggerResponseTeamList struct {
	// in:body
	Body []api.Team `json:"body"`

	// The total number of teams
	TotalCount int64 `json:"X-Total-Count"`
}

// TeamListWithoutPagination - Teams without pagination headers
// swagger:response TeamListWithoutPagination
type swaggerTeamListWithoutPagination struct {
	// in:body
	Body []api.Team `json:"body"`
}

// OrganizationPermissions
// swagger:response OrganizationPermissions
type swaggerResponseOrganizationPermissions struct {
	// in:body
	Body api.OrganizationPermissions `json:"body"`
}

// TeamInviteList
// swagger:response TeamInviteList
type swaggerTeamInviteList struct {
	// in:body
	Body []api.TeamInvite `json:"body"`

	// The total number of team invites
	TotalCount int64 `json:"X-Total-Count"`
}

// TeamInvite
// swagger:response TeamInvite
type swaggerResponseTeamInvite struct {
	// in:body
	Body api.TeamInvite `json:"body"`
}

// UserGroup
// swagger:response UserGroup
type swaggerResponseUserGroup struct {
	// in:body
	Body api.UserGroup `json:"body"`
}

// UserGroupList
// swagger:response UserGroupList
type swaggerResponseUserGroupList struct {
	// in:body
	Body []api.UserGroup `json:"body"`
}

// OrgBranchProtection
// swagger:response OrgBranchProtection
type swaggerResponseOrgBranchProtection struct {
	// in:body
	Body api.OrgBranchProtection `json:"body"`
}

// OrgBranchProtectionList
// swagger:response OrgBranchProtectionList
type swaggerResponseOrgBranchProtectionList struct {
	// in:body
	Body []api.OrgBranchProtection `json:"body"`
}

// AccessReportLineList
// swagger:response AccessReportLineList
type swaggerResponseAccessReportLineList struct {
	// in:body
	Body []api.AccessReportLine `json:"body"`
}

// Project
// swagger:response Project
type swaggerResponseProject struct {
	// in:body
	Body api.Project `json:"body"`
}

// ProjectList
// swagger:response ProjectList
type swaggerResponseProjectList struct {
	// in:body
	Body []api.Project `json:"body"`
}
