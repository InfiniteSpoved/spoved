// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package convert

import (
	"context"

	perm_model "forgejo.org/models/perm"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	api "forgejo.org/modules/structs"
	"forgejo.org/services/accessreport"
)

func toAccessUnits(units map[unit.Type]perm_model.AccessMode) map[string]string {
	if units == nil {
		return nil
	}
	m := make(map[string]string, len(units))
	for tp, mode := range units {
		if u, ok := unit.Units[tp]; ok {
			m[u.NameKey] = mode.String()
		}
	}
	return m
}

// ToAccessRows converts access report rows
func ToAccessRows(ctx context.Context, rows []*accessreport.Row, doer *user_model.User) []*api.AccessRow {
	out := make([]*api.AccessRow, 0, len(rows))
	for _, r := range rows {
		ar := &api.AccessRow{Type: r.Type(), Repository: r.Repo.FullName(), Access: accessreport.Highest(r.Perm).String(), Units: map[string]string{}}
		for _, ru := range r.Repo.Units {
			if m := r.Perm.UnitAccessMode(ru.Type); m > perm_model.AccessModeNone {
				ar.Units[ru.Unit().NameKey] = m.String()
			}
		}
		switch {
		case r.User != nil:
			ar.User = ToUser(ctx, r.User, doer)
		case r.DeployKey != nil:
			ar.DeployKey = &api.AccessDeployKey{ID: r.DeployKey.ID, Name: r.DeployKey.Name, Fingerprint: r.DeployKey.Fingerprint, Expires: OptionalTime(r.DeployKey.ExpiresUnix)}
		case r.Team != nil:
			ar.Type = "team"
			ar.Team = &api.AccessTeam{ID: r.Team.ID, Name: r.Team.Name, NumMembers: r.Team.NumMembers}
		case r.Anonymous:
			ar.Type = "anyone"
		case r.Paths[0].Kind == accessreport.KindInstanceRoles:
			ar.Type = "instance_roles"
			ar.Access, ar.Units = "", nil
		default:
			ar.Type = "signed_in"
		}
		for _, p := range r.Paths {
			ap := &api.AccessPath{Kind: string(p.Kind), Access: p.Mode.String(), Units: toAccessUnits(p.Units), Applied: p.Applied}
			if p.Team != nil {
				ap.Team = p.Team.Name
			}
			for _, g := range p.Groups {
				ag := &api.AccessUserGroup{Name: g.Group.Name}
				if g.Source != nil {
					ag.LoginSource = g.Source.Name
				}
				ap.UserGroups = append(ap.UserGroups, ag)
			}
			ar.Paths = append(ar.Paths, ap)
		}
		out = append(out, ar)
	}
	return out
}

// ToUserAccess converts what gives a user access
func ToUserAccess(ctx context.Context, ua *accessreport.UserAccess, doer *user_model.User) *api.UserAccess {
	out := &api.UserAccess{
		SiteAdmin: ua.User.IsAdmin, SiteAdminVia: ua.SiteAdminVia,
		Auditor: ua.User.IsAuditor, AuditorVia: ua.AuditorVia,
		Orgs: []string{}, Teams: []string{}, UserGroups: []*api.AccessUserGroup{},
		Rows:        ToAccessRows(ctx, ua.Rows, doer),
		Credentials: []*api.AccessCredential{},
	}
	for _, o := range ua.Orgs {
		out.Orgs = append(out.Orgs, o.Name)
	}
	for _, t := range ua.Teams {
		out.Teams = append(out.Teams, t.Name)
	}
	for _, g := range ua.Groups {
		ag := &api.AccessUserGroup{Name: g.Group.Name}
		if g.Source != nil {
			ag.LoginSource = g.Source.Name
		}
		out.UserGroups = append(out.UserGroups, ag)
	}
	for _, k := range ua.PublicKeys {
		out.Credentials = append(out.Credentials, &api.AccessCredential{Kind: "ssh_key", Name: k.Name, Fingerprint: k.Fingerprint, Created: k.CreatedUnix.AsTime(), LastUsed: k.UpdatedUnix.AsTime(), Expires: OptionalTime(k.ExpiresUnix)})
	}
	for _, t := range ua.AccessTokens {
		out.Credentials = append(out.Credentials, &api.AccessCredential{Kind: "access_token", Name: t.Name, LastEight: t.TokenLastEight, Scopes: t.Scope.StringSlice(), Created: t.CreatedUnix.AsTime(), LastUsed: t.UpdatedUnix.AsTime(), Expires: OptionalTime(t.ExpiresUnix)})
	}
	for _, g := range ua.OAuth2Grants {
		out.Credentials = append(out.Credentials, &api.AccessCredential{Kind: "oauth2_grant", Name: g.Application.Name, Scopes: []string{g.Scope}, Created: g.CreatedUnix.AsTime(), LastUsed: g.UpdatedUnix.AsTime()})
	}
	return out
}
