// Copyright 2017 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package user

import (
	"context"
	"fmt"

	audit_model "forgejo.org/models/audit"
	"forgejo.org/models/db"
	"forgejo.org/modules/util"

	"xorm.io/builder"
)

// ErrOpenIDNotExist openid is not known
var ErrOpenIDNotExist = util.NewNotExistErrorf("OpenID is unknown")

// UserOpenID is the list of all OpenID identities of a user.
// Since this is a middle table, name it OpenID is not suitable, so we ignore the lint here
type UserOpenID struct { //revive:disable-line:exported
	ID   int64  `xorm:"pk autoincr"`
	UID  int64  `xorm:"INDEX NOT NULL"`
	URI  string `xorm:"UNIQUE NOT NULL"`
	Show bool   `xorm:"DEFAULT false"`
}

func init() {
	db.RegisterModel(new(UserOpenID))
}

// GetUserOpenIDs returns all openid addresses that belongs to given user.
func GetUserOpenIDs(ctx context.Context, uid int64) ([]*UserOpenID, error) {
	openids := make([]*UserOpenID, 0, 5)
	if err := db.GetEngine(ctx).
		Where("uid=?", uid).
		Asc("id").
		Find(&openids); err != nil {
		return nil, err
	}

	return openids, nil
}

// IsOpenIDUsed returns true if the openid has been used.
func IsOpenIDUsed(ctx context.Context, uri string) (bool, error) {
	if len(uri) == 0 {
		return true, nil
	}

	return db.GetEngine(ctx).Get(&UserOpenID{URI: uri})
}

// ErrOpenIDAlreadyUsed represents a "OpenIDAlreadyUsed" kind of error.
type ErrOpenIDAlreadyUsed struct {
	OpenID string
}

// IsErrOpenIDAlreadyUsed checks if an error is a ErrOpenIDAlreadyUsed.
func IsErrOpenIDAlreadyUsed(err error) bool {
	_, ok := err.(ErrOpenIDAlreadyUsed)
	return ok
}

func (err ErrOpenIDAlreadyUsed) Error() string {
	return fmt.Sprintf("OpenID already in use [oid: %s]", err.OpenID)
}

func (err ErrOpenIDAlreadyUsed) Unwrap() error {
	return util.ErrAlreadyExist
}

// AddUserOpenID adds an pre-verified/normalized OpenID URI to given user.
// NOTE: make sure openid.URI is normalized already
func AddUserOpenID(ctx context.Context, openid *UserOpenID) error {
	used, err := IsOpenIDUsed(ctx, openid.URI)
	if err != nil {
		return err
	} else if used {
		return ErrOpenIDAlreadyUsed{openid.URI}
	}

	return db.WithTx(ctx, func(ctx context.Context) error {
		if err := db.Insert(ctx, openid); err != nil {
			return err
		}
		return recordOpenID(ctx, "account.openid.linked", openid)
	})
}

func recordOpenID(ctx context.Context, action string, o *UserOpenID) error {
	return audit_model.RecordAccount(ctx, action, o.UID, "openid", o.ID, o.URI, map[string]any{"uri": o.URI})
}

// DeleteUserOpenID deletes an openid address of given user.
func DeleteUserOpenID(ctx context.Context, openid *UserOpenID) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		// ask to check UID
		cond := builder.Eq{"uid": openid.UID}
		if openid.ID > 0 {
			cond["id"] = openid.ID
		} else {
			cond["openid"] = openid.URI
		}
		found := make([]*UserOpenID, 0, 1)
		if err := db.GetEngine(ctx).Where(cond).Find(&found); err != nil {
			return err
		}
		deleted, err := db.GetEngine(ctx).Where(cond).Delete(&UserOpenID{})
		if err != nil {
			return err
		} else if deleted != 1 {
			return ErrOpenIDNotExist
		}
		return recordOpenID(ctx, "account.openid.unlinked", found[0])
	})
}

// ToggleUserOpenIDVisibility toggles visibility of an openid address of given user.
func ToggleUserOpenIDVisibility(ctx context.Context, userID, userOpenID int64) (err error) {
	_, err = db.GetEngine(ctx).Exec("update `user_open_id` set `show` = not `show` where `uid` = ? and `id` = ?", userID, userOpenID)
	return err
}
