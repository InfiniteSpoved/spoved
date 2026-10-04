// Copyright 2022 Gitea. All rights reserved.
// SPDX-License-Identifier: MIT

package pull

import (
	"context"
	"fmt"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/log"
	"forgejo.org/modules/timeutil"
)

// AutoMerge represents a pull request scheduled for merging when checks succeed
type AutoMerge struct {
	ID                     int64                 `xorm:"pk autoincr"`
	PullID                 int64                 `xorm:"UNIQUE"`
	DoerID                 int64                 `xorm:"INDEX NOT NULL"`
	Doer                   *user_model.User      `xorm:"-"`
	MergeStyle             repo_model.MergeStyle `xorm:"varchar(30)"`
	Message                string                `xorm:"LONGTEXT"`
	DeleteBranchAfterMerge bool                  `xorm:"NOT NULL DEFAULT false"`
	ScheduledAfterUnix     timeutil.TimeStamp    `xorm:"NOT NULL DEFAULT 0"` // 0 = merge as soon as possible
	HeadCommitID           string                `xorm:"VARCHAR(64)"`        // only merge this head; set for scheduled merges
	CreatedUnix            timeutil.TimeStamp    `xorm:"created"`
}

// TableName return database table name for xorm
func (AutoMerge) TableName() string {
	return "pull_auto_merge"
}

func init() {
	db.RegisterModel(new(AutoMerge))
}

// ErrAlreadyScheduledToAutoMerge represents a "PullRequestHasMerged"-error
type ErrAlreadyScheduledToAutoMerge struct {
	PullID int64
}

func (err ErrAlreadyScheduledToAutoMerge) Error() string {
	return fmt.Sprintf("pull request is already scheduled to auto merge when checks succeed [pull_id: %d]", err.PullID)
}

// IsErrAlreadyScheduledToAutoMerge checks if an error is a ErrAlreadyScheduledToAutoMerge.
func IsErrAlreadyScheduledToAutoMerge(err error) bool {
	_, ok := err.(ErrAlreadyScheduledToAutoMerge)
	return ok
}

// ScheduleAutoMerge schedules a pull request to be merged when all checks succeed
func ScheduleAutoMerge(ctx context.Context, doer *user_model.User, pullID int64, style repo_model.MergeStyle, message string, deleteBranch bool, scheduledAfter timeutil.TimeStamp, headCommitID string) error {
	// Check if we already have a merge scheduled for that pull request
	if exists, _, err := GetScheduledMergeByPullID(ctx, pullID); err != nil {
		return err
	} else if exists {
		return ErrAlreadyScheduledToAutoMerge{PullID: pullID}
	}

	scheduledPRM, err := db.GetEngine(ctx).Insert(&AutoMerge{
		DoerID:                 doer.ID,
		PullID:                 pullID,
		MergeStyle:             style,
		Message:                message,
		DeleteBranchAfterMerge: deleteBranch,
		ScheduledAfterUnix:     scheduledAfter,
		HeadCommitID:           headCommitID,
	})
	log.Trace("ScheduleAutoMerge %+v for PR %d", scheduledPRM, pullID)

	return err
}

// GetScheduledMergeByPullID gets a scheduled pull request merge by pull request id
func GetScheduledMergeByPullID(ctx context.Context, pullID int64) (bool, *AutoMerge, error) {
	scheduledPRM := &AutoMerge{}
	exists, err := db.GetEngine(ctx).Where("pull_id = ?", pullID).Get(scheduledPRM)
	if err != nil || !exists {
		return false, nil, err
	}

	doer, err := user_model.GetPossibleUserByID(ctx, scheduledPRM.DoerID)
	if err != nil {
		return false, nil, err
	}

	log.Trace("GetScheduledMergeByPullID found %+v for PR %d", scheduledPRM, pullID)

	scheduledPRM.Doer = doer
	return true, scheduledPRM, nil
}

// DeleteScheduledAutoMerge delete a scheduled pull request
func DeleteScheduledAutoMerge(ctx context.Context, pullID int64) error {
	exist, scheduledPRM, err := GetScheduledMergeByPullID(ctx, pullID)
	if err != nil {
		return err
	} else if !exist {
		return db.ErrNotExist{Resource: "auto_merge", ID: pullID}
	}

	log.Trace("DeleteScheduledAutoMerge %+v for PR %d", scheduledPRM, pullID)

	_, err = db.GetEngine(ctx).ID(scheduledPRM.ID).Delete(&AutoMerge{})
	return err
}

// TakeDueScheduledAutoMerges clears the time of the scheduled merges that are due and returns their pull IDs,
// so they become normal auto merges. A row is only returned by the caller that cleared it.
func TakeDueScheduledAutoMerges(ctx context.Context, now timeutil.TimeStamp) ([]int64, error) {
	var due []*AutoMerge
	if err := db.GetEngine(ctx).Where("scheduled_after_unix > 0 AND scheduled_after_unix <= ?", now).Find(&due); err != nil {
		return nil, err
	}
	pullIDs := make([]int64, 0, len(due))
	for _, am := range due {
		affected, err := db.GetEngine(ctx).Table("pull_auto_merge").
			Where("id = ? AND scheduled_after_unix = ?", am.ID, am.ScheduledAfterUnix).
			Update(map[string]any{"scheduled_after_unix": 0})
		if err != nil {
			return nil, err
		}
		if affected == 1 {
			pullIDs = append(pullIDs, am.PullID)
		}
	}
	return pullIDs, nil
}
