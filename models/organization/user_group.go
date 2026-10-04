// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package organization

import (
	"context"
	"fmt"
	"strings"

	"forgejo.org/models/db"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"

	"xorm.io/builder"
)

// UserGroup is an instance-wide group of users, managed by site administrators.
// Unlike a Team, it does not belong to an organization and carries no permissions
// on its own: organizations link it to one or more of their teams, and the
// members of the group become members of those teams.
type UserGroup struct {
	ID          int64  `xorm:"pk autoincr"`
	LowerName   string `xorm:"UNIQUE NOT NULL"`
	Name        string `xorm:"NOT NULL"`
	Description string
	CreatedUnix timeutil.TimeStamp `xorm:"INDEX created"`
	UpdatedUnix timeutil.TimeStamp `xorm:"INDEX updated"`

	NumMembers int `xorm:"-"`
	NumTeams   int `xorm:"-"`
}

// UserGroupUser represents the membership of a user in a user group.
type UserGroupUser struct {
	ID      int64 `xorm:"pk autoincr"`
	GroupID int64 `xorm:"UNIQUE(s) INDEX NOT NULL"`
	UID     int64 `xorm:"UNIQUE(s) INDEX NOT NULL"`
	// LoginSourceID is the login source that synced this membership, 0 if an admin added it.
	LoginSourceID int64 `xorm:"NOT NULL DEFAULT 0"`
}

// UserGroupTeam represents a user group linked to an organization team.
type UserGroupTeam struct {
	ID      int64 `xorm:"pk autoincr"`
	GroupID int64 `xorm:"UNIQUE(s) INDEX NOT NULL"`
	TeamID  int64 `xorm:"UNIQUE(s) INDEX NOT NULL"`
	OrgID   int64 `xorm:"INDEX NOT NULL"`
}

func init() {
	db.RegisterModel(new(UserGroup))
	db.RegisterModel(new(UserGroupUser))
	db.RegisterModel(new(UserGroupTeam))
}

// ErrUserGroupAlreadyExist represents a "UserGroupAlreadyExist" kind of error.
type ErrUserGroupAlreadyExist struct {
	Name string
}

// IsErrUserGroupAlreadyExist checks if an error is a ErrUserGroupAlreadyExist.
func IsErrUserGroupAlreadyExist(err error) bool {
	_, ok := err.(ErrUserGroupAlreadyExist)
	return ok
}

func (err ErrUserGroupAlreadyExist) Error() string {
	return fmt.Sprintf("user group already exists [name: %s]", err.Name)
}

func (err ErrUserGroupAlreadyExist) Unwrap() error {
	return util.ErrAlreadyExist
}

// ErrUserGroupNotExist represents a "UserGroupNotExist" kind of error.
type ErrUserGroupNotExist struct {
	ID   int64
	Name string
}

// IsErrUserGroupNotExist checks if an error is a ErrUserGroupNotExist.
func IsErrUserGroupNotExist(err error) bool {
	_, ok := err.(ErrUserGroupNotExist)
	return ok
}

func (err ErrUserGroupNotExist) Error() string {
	return fmt.Sprintf("user group does not exist [id: %d, name: %s]", err.ID, err.Name)
}

func (err ErrUserGroupNotExist) Unwrap() error {
	return util.ErrNotExist
}

// LoadCounts loads the number of members and linked teams of the group.
func (g *UserGroup) LoadCounts(ctx context.Context) (err error) {
	members, err := db.GetEngine(ctx).Where("group_id=?", g.ID).Count(new(UserGroupUser))
	if err != nil {
		return err
	}
	teams, err := db.GetEngine(ctx).Where("group_id=?", g.ID).Count(new(UserGroupTeam))
	if err != nil {
		return err
	}
	g.NumMembers, g.NumTeams = int(members), int(teams)
	return nil
}

// InsertUserGroup creates a new user group.
func InsertUserGroup(ctx context.Context, g *UserGroup) error {
	g.Name = strings.TrimSpace(g.Name)
	g.LowerName = strings.ToLower(g.Name)
	return db.WithTx(ctx, func(ctx context.Context) error {
		has, err := db.GetEngine(ctx).Exist(&UserGroup{LowerName: g.LowerName})
		if err != nil {
			return err
		} else if has {
			return ErrUserGroupAlreadyExist{Name: g.Name}
		}
		return db.Insert(ctx, g)
	})
}

// UpdateUserGroup updates the name and description of a user group.
func UpdateUserGroup(ctx context.Context, g *UserGroup) error {
	g.Name = strings.TrimSpace(g.Name)
	g.LowerName = strings.ToLower(g.Name)
	return db.WithTx(ctx, func(ctx context.Context) error {
		has, err := db.GetEngine(ctx).Where("lower_name=?", g.LowerName).And("id!=?", g.ID).Exist(new(UserGroup))
		if err != nil {
			return err
		} else if has {
			return ErrUserGroupAlreadyExist{Name: g.Name}
		}
		_, err = db.GetEngine(ctx).ID(g.ID).Cols("lower_name", "name", "description").Update(g)
		return err
	})
}

// GetUserGroupByID returns the user group with the given ID.
func GetUserGroupByID(ctx context.Context, id int64) (*UserGroup, error) {
	g := new(UserGroup)
	has, err := db.GetEngine(ctx).ID(id).Get(g)
	if err != nil {
		return nil, err
	} else if !has {
		return nil, ErrUserGroupNotExist{ID: id}
	}
	return g, nil
}

// GetUserGroupByName returns the user group with the given name (case-insensitive).
func GetUserGroupByName(ctx context.Context, name string) (*UserGroup, error) {
	g := &UserGroup{LowerName: strings.ToLower(strings.TrimSpace(name))}
	has, err := db.GetEngine(ctx).Get(g)
	if err != nil {
		return nil, err
	} else if !has {
		return nil, ErrUserGroupNotExist{Name: name}
	}
	return g, nil
}

// SearchUserGroupOptions holds the search options for user groups.
type SearchUserGroupOptions struct {
	db.ListOptions
	Keyword string
	// NotLinkedToTeamID excludes the groups already linked to this team.
	NotLinkedToTeamID int64
}

func (opts SearchUserGroupOptions) ToConds() builder.Cond {
	cond := builder.NewCond()
	if opts.Keyword != "" {
		cond = cond.And(builder.Like{"lower_name", strings.ToLower(opts.Keyword)})
	}
	if opts.NotLinkedToTeamID > 0 {
		cond = cond.And(builder.NotIn("id",
			builder.Select("group_id").From("user_group_team").Where(builder.Eq{"team_id": opts.NotLinkedToTeamID})))
	}
	return cond
}

func (opts SearchUserGroupOptions) ToOrders() string {
	return "lower_name ASC"
}

// SearchUserGroups returns user groups matching the given options and the total count.
func SearchUserGroups(ctx context.Context, opts *SearchUserGroupOptions) ([]*UserGroup, int64, error) {
	return db.FindAndCount[UserGroup](ctx, opts)
}

// GetUserGroupMembers returns the users of a user group, all of them when
// listOptions has no page size.
func GetUserGroupMembers(ctx context.Context, groupID int64, listOptions db.ListOptions) ([]*user_model.User, error) {
	users := make([]*user_model.User, 0, 10)
	sess := db.GetEngine(ctx).
		In("id", builder.Select("uid").From("user_group_user").Where(builder.Eq{"group_id": groupID})).
		OrderBy("lower_name")
	if listOptions.PageSize > 0 {
		sess = db.SetSessionPagination(sess, &listOptions)
	}
	return users, sess.Find(&users)
}

// GetUserGroupMemberIDs returns the IDs of the users of a user group.
func GetUserGroupMemberIDs(ctx context.Context, groupID int64) ([]int64, error) {
	ids := make([]int64, 0, 10)
	return ids, db.GetEngine(ctx).Table("user_group_user").Where("group_id=?", groupID).Cols("uid").Find(&ids)
}

// IsUserGroupMember returns true if the user is a member of the user group.
func IsUserGroupMember(ctx context.Context, groupID, uid int64) (bool, error) {
	return db.GetEngine(ctx).Exist(&UserGroupUser{GroupID: groupID, UID: uid})
}

// InsertUserGroupMember adds a user to a user group. loginSourceID is the
// login source syncing the membership, 0 if an admin adds it. It does not
// update team memberships.
func InsertUserGroupMember(ctx context.Context, groupID, uid, loginSourceID int64) error {
	return db.Insert(ctx, &UserGroupUser{GroupID: groupID, UID: uid, LoginSourceID: loginSourceID})
}

// DeleteSyncedUserGroupMember removes a user from a user group if the given
// login source synced the membership, and reports whether it did. It does not
// update team memberships.
func DeleteSyncedUserGroupMember(ctx context.Context, groupID, uid, loginSourceID int64) (bool, error) {
	n, err := db.GetEngine(ctx).Where("group_id=? AND uid=? AND login_source_id=?", groupID, uid, loginSourceID).Delete(new(UserGroupUser))
	return n > 0, err
}

// GetUserGroupMemberLoginSources returns the login source ID of each member of
// a user group that a login source synced, keyed by user ID.
func GetUserGroupMemberLoginSources(ctx context.Context, groupID int64) (map[int64]int64, error) {
	rows := make([]*UserGroupUser, 0, 10)
	if err := db.GetEngine(ctx).Where("group_id=? AND login_source_id>0", groupID).Find(&rows); err != nil {
		return nil, err
	}
	sources := make(map[int64]int64, len(rows))
	for _, r := range rows {
		sources[r.UID] = r.LoginSourceID
	}
	return sources, nil
}

// DeleteUserGroupMember removes a user from a user group. It does not update team memberships.
func DeleteUserGroupMember(ctx context.Context, groupID, uid int64) error {
	_, err := db.GetEngine(ctx).Delete(&UserGroupUser{GroupID: groupID, UID: uid})
	return err
}

// LinkedTeam is a team a user group is linked to, along with its organization.
type LinkedTeam struct {
	Team *Team
	Org  *Organization
}

// GetUserGroupTeams returns the teams a user group is linked to.
func GetUserGroupTeams(ctx context.Context, groupID int64) ([]*LinkedTeam, error) {
	teams := make([]*Team, 0, 10)
	if err := db.GetEngine(ctx).
		In("id", builder.Select("team_id").From("user_group_team").Where(builder.Eq{"group_id": groupID})).
		OrderBy("org_id, lower_name").
		Find(&teams); err != nil {
		return nil, err
	}
	orgs := make(map[int64]*Organization)
	linked := make([]*LinkedTeam, 0, len(teams))
	for _, t := range teams {
		org, ok := orgs[t.OrgID]
		if !ok {
			var err error
			if org, err = GetOrgByID(ctx, t.OrgID); err != nil {
				return nil, err
			}
			orgs[t.OrgID] = org
		}
		linked = append(linked, &LinkedTeam{Team: t, Org: org})
	}
	return linked, nil
}

// GetTeamUserGroups returns the user groups linked to a team.
func GetTeamUserGroups(ctx context.Context, teamID int64) ([]*UserGroup, error) {
	groups := make([]*UserGroup, 0, 5)
	return groups, db.GetEngine(ctx).
		In("id", builder.Select("group_id").From("user_group_team").Where(builder.Eq{"team_id": teamID})).
		OrderBy("lower_name").
		Find(&groups)
}

// LoadUserGroupsNumMembers sets NumMembers of the given groups in one query.
func LoadUserGroupsNumMembers(ctx context.Context, groups []*UserGroup) error {
	if len(groups) == 0 {
		return nil
	}
	ids := make([]int64, len(groups))
	for i, g := range groups {
		ids[i] = g.ID
	}
	counts, err := countGroupedBy(ctx, "user_group_user", "group_id", ids)
	if err != nil {
		return err
	}
	for _, g := range groups {
		g.NumMembers = counts[g.ID]
	}
	return nil
}

// LoadTeamsNumUserGroups sets NumUserGroups of the given teams in one query.
func LoadTeamsNumUserGroups(ctx context.Context, teams []*Team) error {
	if len(teams) == 0 {
		return nil
	}
	ids := make([]int64, len(teams))
	for i, t := range teams {
		ids[i] = t.ID
	}
	counts, err := countGroupedBy(ctx, "user_group_team", "team_id", ids)
	if err != nil {
		return err
	}
	for _, t := range teams {
		t.NumUserGroups = counts[t.ID]
	}
	return nil
}

// countGroupedBy counts the rows of table for each of the given values of col.
func countGroupedBy(ctx context.Context, table, col string, ids []int64) (map[int64]int, error) {
	rows := make([]struct {
		ID    int64 `xorm:"id"`
		Count int   `xorm:"cnt"`
	}, 0, len(ids))
	if err := db.GetEngine(ctx).Table(table).Select(col+" AS id, COUNT(*) AS cnt").
		In(col, ids).GroupBy(col).Find(&rows); err != nil {
		return nil, err
	}
	counts := make(map[int64]int, len(rows))
	for _, r := range rows {
		counts[r.ID] = r.Count
	}
	return counts, nil
}

// GetTeamMembersUserGroups returns, for each of the given users, the user
// groups linked to the team that the user is a member of, ordered by name.
func GetTeamMembersUserGroups(ctx context.Context, teamID int64, uids []int64) (map[int64][]*UserGroup, error) {
	if len(uids) == 0 {
		return nil, nil
	}
	rows := make([]struct {
		UID       int64 `xorm:"uid"`
		UserGroup `xorm:"extends"`
	}, 0, len(uids))
	if err := db.GetEngine(ctx).Table("user_group_user").
		Join("INNER", "user_group_team", "user_group_team.group_id = user_group_user.group_id").
		Join("INNER", "user_group", "user_group.id = user_group_user.group_id").
		Where("user_group_team.team_id = ?", teamID).
		In("user_group_user.uid", uids).
		Select("user_group_user.uid, user_group.*").
		OrderBy("user_group.lower_name").
		Find(&rows); err != nil {
		return nil, err
	}
	groups := make(map[int64][]*UserGroup, len(rows))
	for i := range rows {
		groups[rows[i].UID] = append(groups[rows[i].UID], &rows[i].UserGroup)
	}
	return groups, nil
}

// IsUserGroupLinkedToTeam returns true if the user group is linked to the team.
func IsUserGroupLinkedToTeam(ctx context.Context, groupID, teamID int64) (bool, error) {
	return db.GetEngine(ctx).Exist(&UserGroupTeam{GroupID: groupID, TeamID: teamID})
}

// InsertUserGroupTeam links a user group to a team. It does not update team memberships.
func InsertUserGroupTeam(ctx context.Context, groupID int64, team *Team) error {
	return db.Insert(ctx, &UserGroupTeam{GroupID: groupID, TeamID: team.ID, OrgID: team.OrgID})
}

// DeleteUserGroupTeam unlinks a user group from a team. It does not update team memberships.
func DeleteUserGroupTeam(ctx context.Context, groupID, teamID int64) error {
	_, err := db.GetEngine(ctx).Delete(&UserGroupTeam{GroupID: groupID, TeamID: teamID})
	return err
}

// IsTeamMemberViaUserGroup returns true if the user is a member of any user group linked to the team.
func IsTeamMemberViaUserGroup(ctx context.Context, teamID, uid int64) (bool, error) {
	return db.GetEngine(ctx).Table("user_group_user").
		Join("INNER", "user_group_team", "user_group_team.group_id = user_group_user.group_id").
		Where("user_group_team.team_id=?", teamID).
		And("user_group_user.uid=?", uid).
		Exist()
}

// GetTeamUser returns the membership of a user in a team, or nil if there is none.
func GetTeamUser(ctx context.Context, teamID, uid int64) (*TeamUser, error) {
	tu := new(TeamUser)
	has, err := db.GetEngine(ctx).Where("team_id=?", teamID).And("uid=?", uid).Get(tu)
	if err != nil || !has {
		return nil, err
	}
	return tu, nil
}
