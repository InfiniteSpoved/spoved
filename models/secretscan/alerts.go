// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretscan

import (
	"context"
	"errors"
	"slices"
	"strings"

	"forgejo.org/models/db"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"

	"xorm.io/builder"
)

// ClosedStates are the states an alert is closed with; its reason is the same word.
var ClosedStates = []string{StateRevoked, StateFalsePos, StateUsedInTests, StateWontFix}

// MaxCommentLen is the longest comment of a closed alert, in bytes.
const MaxCommentLen = 1000

// MaxFilterLen is the longest secret type or rule filter.
const MaxFilterLen = 64

// ErrAlertNotExist is an alert that is not in the repository.
var ErrAlertNotExist = util.NewNotExistErrorf("the secret alert does not exist")

// ErrAlertState is a change that the current state of the alert doesn't allow (it changed meanwhile).
var ErrAlertState = errors.New("the secret alert changed")

// GetAlert returns the alert with this number in the repository.
func GetAlert(ctx context.Context, repoID, index int64) (*Alert, error) {
	a := &Alert{}
	has, err := db.GetEngine(ctx).Where("repo_id = ? AND `index` = ?", repoID, index).Get(a)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, ErrAlertNotExist
	}
	return a, nil
}

// AlertSearchOptions selects the alerts of one repository, newest first.
type AlertSearchOptions struct {
	db.ListOptions
	RepoID      int64
	State       string   // open or resolved
	Resolutions []string // closed states
	SecretTypes []string // rule IDs
}

func (o *AlertSearchOptions) ToConds() builder.Cond {
	cond := builder.NewCond().And(builder.Eq{"repo_id": o.RepoID})
	switch o.State {
	case StateOpen:
		cond = cond.And(builder.Eq{"state": StateOpen})
	case "resolved":
		cond = cond.And(builder.In("state", ClosedStates))
	}
	if len(o.Resolutions) > 0 {
		cond = cond.And(builder.In("state", o.Resolutions))
	}
	if len(o.SecretTypes) > 0 {
		cond = cond.And(builder.In("rule_id", o.SecretTypes))
	}
	return cond
}

func (o *AlertSearchOptions) ToOrders() string { return "id DESC" }

// ParseFilters checks the state and the comma lists of the list filters.
func (o *AlertSearchOptions) ParseFilters(state, resolution, secretType string) error {
	if state != "" && state != StateOpen && state != "resolved" {
		return util.NewInvalidArgumentErrorf("state must be open or resolved")
	}
	if len(resolution) > MaxFilterLen*8 || len(secretType) > MaxFilterLen*8 {
		return util.NewInvalidArgumentErrorf("the filter is too long")
	}
	o.State = state
	for r := range strings.SplitSeq(resolution, ",") {
		if r = strings.TrimSpace(r); r != "" && !slices.Contains(ClosedStates, r) {
			return util.NewInvalidArgumentErrorf("unknown resolution")
		} else if r != "" {
			o.Resolutions = append(o.Resolutions, r)
		}
	}
	for t := range strings.SplitSeq(secretType, ",") {
		if t = strings.TrimSpace(t); len(t) > MaxFilterLen {
			return util.NewInvalidArgumentErrorf("the filter is too long")
		} else if t != "" {
			o.SecretTypes = append(o.SecretTypes, t)
		}
	}
	return nil
}

// LocationSearchOptions pages the places of one alert, in the order they were found.
type LocationSearchOptions struct {
	db.ListOptions
	RepoID, AlertID int64
	NoWiki          bool // leave out the places of the wiki
}

func (o *LocationSearchOptions) ToConds() builder.Cond {
	cond := builder.NewCond().And(builder.Eq{"repo_id": o.RepoID, "alert_id": o.AlertID})
	if o.NoWiki {
		cond = cond.And(builder.Neq{"kind": KindWiki})
	}
	return cond
}

func (o *LocationSearchOptions) ToOrders() string { return "id" }

// CountCodeLocations counts the stored places of the alerts that are not in the wiki, in one query.
func CountCodeLocations(ctx context.Context, repoID int64, alertIDs []int64) (map[int64]int, error) {
	var rows []struct {
		AlertID int64
		N       int
	}
	err := db.GetEngine(ctx).Table("secret_scanning_location").Select("alert_id, COUNT(*) AS n").
		Where(builder.Eq{"repo_id": repoID}.And(builder.In("alert_id", alertIDs)).And(builder.Neq{"kind": KindWiki})).GroupBy("alert_id").Find(&rows)
	out := make(map[int64]int, len(rows))
	for _, r := range rows {
		out[r.AlertID] = r.N
	}
	return out, err
}

// FindLocations returns up to limit places of an alert in the order they were found.
func FindLocations(ctx context.Context, repoID, alertID int64, limit int) ([]*Location, error) {
	var locs []*Location
	return locs, db.GetEngine(ctx).Where("repo_id = ? AND alert_id = ?", repoID, alertID).OrderBy("id").Limit(limit).Find(&locs)
}

// CloseAlert closes an open alert as state with the comment; the UPDATE is conditional on the alert being open, so
// of two concurrent closes one gets ErrAlertState. before is the alert as it was.
func CloseAlert(ctx context.Context, repoID, index int64, state, comment string, byID int64) (before *Alert, err error) {
	if before, err = GetAlert(ctx, repoID, index); err != nil {
		return nil, err
	}
	n, err := db.GetEngine(ctx).Where("repo_id = ? AND `index` = ? AND state = ?", repoID, index, StateOpen).
		Cols("state", "reason", "comment", "closed_by_id", "closed_unix").
		Update(&Alert{State: state, Reason: state, Comment: comment, ClosedByID: byID, ClosedUnix: timeutil.TimeStampNow()})
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return before, ErrAlertState
	}
	return before, nil
}

// ReopenAlert reopens a closed alert and clears what closing set. An alert that is open is left as it is
// (changed is false).
func ReopenAlert(ctx context.Context, repoID, index int64) (before *Alert, changed bool, err error) {
	if before, err = GetAlert(ctx, repoID, index); err != nil {
		return nil, false, err
	}
	if before.State == StateOpen {
		return before, false, nil
	}
	n, err := db.GetEngine(ctx).Where("repo_id = ? AND `index` = ? AND state = ?", repoID, index, before.State).
		Cols("state", "reason", "comment", "closed_by_id", "closed_unix").Update(&Alert{State: StateOpen})
	if err != nil {
		return nil, false, err
	}
	if n != 1 {
		return before, false, ErrAlertState
	}
	return before, true, nil
}
