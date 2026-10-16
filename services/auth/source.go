// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package auth

import (
	"context"

	"forgejo.org/models/auth"
	"forgejo.org/models/db"
	"forgejo.org/models/organization"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/optional"
)

// DeleteSource deletes a AuthSource record in DB.
func DeleteSource(ctx context.Context, source *auth.Source) error {
	count, err := db.GetEngine(ctx).Count(&user_model.User{LoginSource: source.ID})
	if err != nil {
		return err
	} else if count > 0 {
		return auth.ErrSourceInUse{
			ID: source.ID,
		}
	}

	count, err = db.GetEngine(ctx).Count(&user_model.ExternalLoginUser{LoginSourceID: source.ID})
	if err != nil {
		return err
	} else if count > 0 {
		return auth.ErrSourceInUse{
			ID: source.ID,
		}
	}

	if err := db.WithTx(ctx, func(ctx context.Context) error {
		tokens, err := db.GetEngine(ctx).Where("login_source_id = ?", source.ID).Delete(new(auth.AuthorizationToken))
		if err != nil {
			return err
		}
		members, err := db.GetEngine(ctx).
			Where("created_by_login_source_id = ?", source.ID).
			Cols("created_by_login_source_id").
			Update(organization.TeamUser{CreatedByLoginSourceID: optional.None[int64]()})
		if err != nil {
			return err
		}
		if _, err := db.GetEngine(ctx).ID(source.ID).Delete(new(auth.Source)); err != nil {
			return err
		}
		return auth.RecordSource(ctx, "admin.login_source.deleted", source, map[string]any{
			"counts": map[string]int64{"authorization_tokens": tokens, "team_memberships_unlinked": members},
		})
	}); err != nil {
		return err
	}

	if registerableSource, ok := source.Cfg.(auth.RegisterableSource); ok {
		return registerableSource.UnregisterSource()
	}
	return nil
}
