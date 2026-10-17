// SPDX-License-Identifier: MIT

package actions

import (
	"context"
	"crypto/subtle"
	"fmt"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	"forgejo.org/modules/util"

	gouuid "github.com/google/uuid"
)

func RegisterRunner(ctx context.Context, ownerID, repoID int64, token string, labels *[]string, name, version string, ephemeral bool) (*ActionRunner, error) {
	uuid, err := gouuid.FromBytes([]byte(token[:16]))
	if err != nil {
		return nil, fmt.Errorf("gouuid.FromBytes %v", err)
	}
	uuidString := uuid.String()

	name, _ = util.SplitStringAtByteN(name, 255)
	var runner ActionRunner
	return &runner, db.WithTx(ctx, func(ctx context.Context) error {
		has, err := db.GetEngine(ctx).Where("uuid=?", uuidString).Get(&runner)
		if err != nil {
			return fmt.Errorf("GetRunner %v", err)
		}
		if !has {
			// The runner does not exist yet, create it
			runner = ActionRunner{
				UUID: uuidString, Name: name, OwnerID: ownerID, RepoID: repoID, Version: version, Ephemeral: ephemeral,
				AgentLabels: []string{},
			}
			if labels != nil {
				runner.AgentLabels = *labels
			}
			if err := runner.UpdateSecret(token); err != nil {
				return fmt.Errorf("can't set new runner's secret: %w", err)
			}
			if err := CreateRunner(ctx, &runner); err != nil {
				return fmt.Errorf("can't create new runner %w", err)
			}
			return nil
		}

		// The runner exists, check if the rest of the token has changed.
		mustUpdateSecret := subtle.ConstantTimeCompare(
			[]byte(runner.TokenHash),
			[]byte(auth_model.HashToken(token, runner.TokenSalt)),
		) != 1
		_, _, oldLevel, err := RunnerScope(ctx, runner.OwnerID, runner.RepoID)
		if err != nil {
			return err
		}
		scopeChanged := runner.OwnerID != ownerID || runner.RepoID != repoID

		// Update the existing runner
		cols := []string{"name", "owner_id", "repo_id", "version", "ephemeral"}
		runner.Name = name
		runner.OwnerID = ownerID
		runner.RepoID = repoID
		runner.Version = version
		runner.Ephemeral = ephemeral
		if labels != nil {
			runner.AgentLabels = *labels
			cols = append(cols, "agent_labels")
		}
		if mustUpdateSecret {
			if err := runner.UpdateSecret(token); err != nil {
				return fmt.Errorf("can't change runner's secret: %w", err)
			}
			cols = append(cols, "token_hash", "token_salt")
		}

		if err := UpdateRunner(ctx, &runner, cols...); err != nil {
			return fmt.Errorf("can't update the runner %+v %w", runner, err)
		}

		data := map[string]any{}
		if scopeChanged {
			_, _, newLevel, err := RunnerScope(ctx, ownerID, repoID)
			if err != nil {
				return err
			}
			data["before"], data["after"] = map[string]any{"level": oldLevel}, map[string]any{"level": newLevel}
		}
		if mustUpdateSecret {
			data["token_reset"] = true
		}
		if len(data) == 0 {
			return nil
		}
		return recordRunner(ctx, "credential.runner.updated", &runner, data)
	})
}
