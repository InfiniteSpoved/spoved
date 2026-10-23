// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations

import (
	"code.forgejo.org/xorm/xorm"
)

func init() {
	registerMigration(&Migration{
		Description: "add who created an organization, project or repository access token",
		Upgrade:     addAccessTokenCreator,
	})
}

type v18zzfAccessToken struct {
	ID        int64 `xorm:"pk autoincr"`
	CreatorID int64 `xorm:"NOT NULL DEFAULT 0"`
}

func (v18zzfAccessToken) TableName() string {
	return "access_token"
}

func addAccessTokenCreator(x *xorm.Engine) error {
	return x.Sync(new(v18zzfAccessToken))
}
