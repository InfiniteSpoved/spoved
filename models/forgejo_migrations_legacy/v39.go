// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations_legacy

import (
	"context"
	"fmt"

	"forgejo.org/models/db"
	"forgejo.org/modules/keying"
	"forgejo.org/modules/log"
	"forgejo.org/modules/secret"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"

	"code.forgejo.org/xorm/xorm"
	"code.forgejo.org/xorm/xorm/schemas"
)

// v39Secret is the secret table as it was then, without the columns later migrations add.
type v39Secret struct {
	ID          int64
	OwnerID     int64
	RepoID      int64
	Name        string
	Data        []byte             `xorm:"BLOB"`
	CreatedUnix timeutil.TimeStamp `xorm:"created NOT NULL"`
}

func (v39Secret) TableName() string { return "secret" }

func (s *v39Secret) setData(data string) {
	s.Data = keying.ActionSecret.Encrypt([]byte(util.ReserveLineBreakForTextarea(data)), keying.ColumnAndID("data", s.ID))
}

func MigrateActionSecretsToKeying(x *xorm.Engine) error {
	return db.WithTx(db.DefaultContext, func(ctx context.Context) error {
		sess := db.GetEngine(ctx)

		switch x.Dialect().URI().DBType {
		case schemas.MYSQL:
			if _, err := sess.Exec("ALTER TABLE `secret` MODIFY `data` BLOB"); err != nil {
				return err
			}
		case schemas.SQLITE:
			if _, err := sess.Exec("ALTER TABLE `secret` RENAME COLUMN `data` TO `data_backup`"); err != nil {
				return err
			}
			if _, err := sess.Exec("ALTER TABLE `secret` ADD COLUMN `data` BLOB"); err != nil {
				return err
			}
			if _, err := sess.Exec("UPDATE `secret` SET `data` = `data_backup`"); err != nil {
				return err
			}
			if _, err := sess.Exec("ALTER TABLE `secret` DROP COLUMN `data_backup`"); err != nil {
				return err
			}
		case schemas.POSTGRES:
			if _, err := sess.Exec("ALTER TABLE `secret` ALTER COLUMN `data` SET DATA TYPE bytea USING data::text::bytea"); err != nil {
				return err
			}
		}

		oldEncryptionKey := setting.SecretKey

		messages := make([]string, 0, 100)
		ids := make([]int64, 0, 100)

		err := db.Iterate(ctx, nil, func(ctx context.Context, bean *v39Secret) error {
			secretBytes, err := secret.DecryptSecret(oldEncryptionKey, string(bean.Data))
			if err != nil {
				messages = append(messages, fmt.Sprintf("secret.id=%d, secret.name=%q, secret.repo_id=%d, secret.owner_id=%d: secret.DecryptSecret(): %v", bean.ID, bean.Name, bean.RepoID, bean.OwnerID, err))
				ids = append(ids, bean.ID)
				return nil
			}

			bean.setData(secretBytes)
			_, err = sess.Cols("data").ID(bean.ID).Update(bean)
			return err
		})

		if err == nil {
			if len(ids) > 0 {
				log.Error("Forgejo migration[37]: The following action secrets were found to be corrupted and removed from the database.")
				for _, message := range messages {
					log.Error("Forgejo migration[37]: %s", message)
				}

				_, err = sess.In("id", ids).NoAutoCondition().NoAutoTime().Delete(&v39Secret{})
			}
		}
		return err
	})
}
