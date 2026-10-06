// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package accessreport

import (
	"context"

	activities_model "forgejo.org/models/activities"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/timeutil"
)

// Person is what site admins see about a user in a report.
type Person struct {
	LastPush      timeutil.TimeStamp // to the repositories of the report
	TwoFactor     bool
	AccountSource string
}

// People returns the person columns of the users of the rows, with their last push to the repositories of the
// report. For site admins only: anyone can create an org and add any user to it.
func People(ctx context.Context, rows []*Row, repoIDs []int64) (map[int64]*Person, error) {
	users := user_model.UserList{}
	people := map[int64]*Person{}
	sourceIDs := []int64{}
	for _, r := range rows {
		if r.User != nil && people[r.User.ID] == nil {
			people[r.User.ID] = &Person{AccountSource: "local"}
			users = append(users, r.User)
			sourceIDs = append(sourceIDs, r.User.LoginSource)
		}
	}
	if len(users) == 0 {
		return people, nil
	}

	pushes := make([]struct {
		ActUserID int64
		Last      timeutil.TimeStamp
	}, 0, len(users))
	if err := db.GetEngine(ctx).Table("action").Select("act_user_id, MAX(created_unix) AS last").
		In("op_type", activities_model.ActionCommitRepo, activities_model.ActionPushTag).
		In("act_user_id", users.GetUserIDs()).In("repo_id", repoIDs).
		GroupBy("act_user_id").Find(&pushes); err != nil {
		return nil, err
	}
	for _, p := range pushes {
		people[p.ActUserID].LastPush = p.Last
	}
	for uid, ok := range users.GetTwoFaStatus(ctx) {
		people[uid].TwoFactor = ok
	}
	sources := map[int64]*auth_model.Source{}
	if err := db.GetEngine(ctx).In("id", sourceIDs).Find(&sources); err != nil {
		return nil, err
	}
	for _, u := range users {
		if s := sources[u.LoginSource]; s != nil {
			people[u.ID].AccountSource = s.TypeName() + ": " + s.Name
		}
	}
	return people, nil
}
