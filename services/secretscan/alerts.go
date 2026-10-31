// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretscan

import (
	"context"
	"errors"
	"slices"
	"sync"

	audit_model "forgejo.org/models/audit"
	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	secretscan_model "forgejo.org/models/secretscan"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/secretdetection"
	"forgejo.org/modules/util"
)

// ErrConflict is a change of an alert that its state doesn't allow any more (it changed meanwhile): the caller
// reloads and answers 409.
var ErrConflict = errors.New("the alert changed")

// ErrArchived is a change in an archived repository.
var ErrArchived = errors.New("the repository is archived")

// ErrCommentSecret is a comment the detector finds a secret in. Alert comments are stored, shown and audited, so
// they must not become a second place for the secret.
var ErrCommentSecret = util.NewInvalidArgumentErrorf("the comment looks like a secret")

// commentDetector is the detector of every rule, the generic ones too, for the comment of an alert.
var commentDetector = sync.OnceValues(func() (*secretdetection.Detector, error) {
	return secretdetection.NewDetector(secretdetection.BuiltinRules())
})

// Close closes an open alert of the repository as revoked, false_positive or used_in_tests (the reason is the
// state), with an optional comment of at most 1,000 bytes. The audit entry is written in the same transaction. A
// bad state or comment is util.ErrInvalidArgument, an unknown alert util.ErrNotExist, an alert that isn't open
// ErrConflict, an archived repository ErrArchived.
func Close(ctx context.Context, repo *repo_model.Repository, doer *user_model.User, index int64, state, comment string) (*secretscan_model.Alert, error) {
	if !slices.Contains(secretscan_model.ClosedStates, state) {
		return nil, util.NewInvalidArgumentErrorf("state must be one of %v, or open", secretscan_model.ClosedStates)
	}
	comment = util.StripControl(comment)
	if len(comment) > secretscan_model.MaxCommentLen {
		return nil, util.NewInvalidArgumentErrorf("the comment is longer than %d bytes", secretscan_model.MaxCommentLen)
	}
	if comment != "" {
		d, err := commentDetector()
		if err != nil {
			return nil, err
		}
		if found, _ := d.Scan([]byte(comment)); len(found) > 0 {
			return nil, ErrCommentSecret
		}
	}
	return changeAlert(ctx, repo, "", func(ctx context.Context) (*secretscan_model.Alert, bool, error) {
		before, err := secretscan_model.CloseAlert(ctx, repo.ID, index, state, comment, doer.ID)
		return before, err == nil, err
	}, index, state, comment)
}

// Reopen reopens a closed alert; an open one is returned as it is.
func Reopen(ctx context.Context, repo *repo_model.Repository, index int64) (*secretscan_model.Alert, error) {
	return changeAlert(ctx, repo, "reopen", func(ctx context.Context) (*secretscan_model.Alert, bool, error) {
		return secretscan_model.ReopenAlert(ctx, repo.ID, index)
	}, index, secretscan_model.StateOpen, "")
}

func changeAlert(ctx context.Context, repo *repo_model.Repository, kind string, do func(context.Context) (*secretscan_model.Alert, bool, error), index int64, state, comment string) (*secretscan_model.Alert, error) {
	if repo.IsArchived {
		return nil, ErrArchived
	}
	var after *secretscan_model.Alert
	err := db.WithTx(ctx, func(ctx context.Context) error {
		before, changed, err := do(ctx)
		if err != nil {
			return err
		}
		if after, err = secretscan_model.GetAlert(ctx, repo.ID, index); err != nil {
			return err
		}
		if !changed {
			return nil
		}
		data := map[string]any{
			"index": before.Index, "rule": before.RuleID, "fingerprint": before.Fingerprint[:min(12, len(before.Fingerprint))],
			"previous_state": before.State, "state": state, "comment": comment,
		}
		if locs, err := secretscan_model.FindLocations(ctx, repo.ID, before.ID, 1); err != nil {
			return err
		} else if len(locs) > 0 {
			data["path"] = locs[0].Path
		}
		// the action keys are literals: TestActionCatalog finds them in the code
		ev := audit_model.Event{TargetType: "secret_scanning_alert", TargetID: before.ID, TargetName: before.RuleID, Data: data}
		if kind == "reopen" {
			ev.Action = "repo.secret_alert.reopened"
		} else {
			ev.Action = "repo.secret_alert.closed"
		}
		return repo_model.RecordRepoTarget(ctx, repo, ev)
	})
	switch {
	case errors.Is(err, secretscan_model.ErrAlertState):
		return nil, ErrConflict
	case err != nil:
		return nil, err
	}
	return after, nil
}
