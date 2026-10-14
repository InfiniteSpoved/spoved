// Copyright 2024 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package quota

import (
	"context"

	"forgejo.org/models/db"
	"forgejo.org/modules/setting"
)

func init() {
	db.RegisterModel(new(Rule))
	db.RegisterModel(new(Group))
	db.RegisterModel(new(GroupRuleMapping))
	db.RegisterModel(new(GroupMapping))
}

// EvaluateForUser reports whether userID may store more data of subject: never while the size limit of the user's
// personal repositories is reached (independent of [quota]), else as the quota groups decide.
func EvaluateForUser(ctx context.Context, userID int64, subject LimitSubject) (bool, error) {
	if reached, err := personalRepoLimitReached(ctx, userID); err != nil || reached {
		return false, err
	}
	return EvaluateQuotaGroupsForUser(ctx, userID, subject)
}

// EvaluateQuotaGroupsForUser is EvaluateForUser without the personal repository size limit, for the pre-receive
// hook: the limit refuses git pushes before they start, and web deletes push through the hook.
func EvaluateQuotaGroupsForUser(ctx context.Context, userID int64, subject LimitSubject) (bool, error) {
	if !setting.Quota.Enabled {
		return true, nil
	}

	groups, err := GetGroupsForUser(ctx, userID)
	if err != nil {
		return false, err
	}

	used, err := GetUsedForUser(ctx, userID)
	if err != nil {
		return false, err
	}

	allow := groups.Evaluate(*used, subject)
	return allow, nil
}
