// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	audit_model "forgejo.org/models/audit"
	"forgejo.org/models/db"
	"forgejo.org/modules/git"
	giturl "forgejo.org/modules/git/url"
	"forgejo.org/modules/keying"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"

	"xorm.io/builder"
)

// ErrPushMirrorNotExist mirror does not exist error
var ErrPushMirrorNotExist = util.NewNotExistErrorf("PushMirror does not exist")

// PushMirror represents mirror information of a repository.
type PushMirror struct {
	ID            int64       `xorm:"pk autoincr"`
	RepoID        int64       `xorm:"INDEX"`
	Repo          *Repository `xorm:"-"`
	RemoteName    string
	RemoteAddress string `xorm:"VARCHAR(2048)"`
	BranchFilter  string `xorm:"VARCHAR(2048)"`

	// A keypair formatted in OpenSSH format.
	PublicKey  string `xorm:"VARCHAR(100)"`
	PrivateKey []byte `xorm:"BLOB"`

	SyncOnCommit   bool `xorm:"NOT NULL DEFAULT true"`
	Interval       time.Duration
	CreatedUnix    timeutil.TimeStamp `xorm:"created"`
	LastUpdateUnix timeutil.TimeStamp `xorm:"INDEX last_update"`
	LastError      string             `xorm:"text"`
}

type PushMirrorOptions struct {
	db.ListOptions
	ID         int64
	RepoID     int64
	RemoteName string
}

func (opts PushMirrorOptions) ToConds() builder.Cond {
	cond := builder.NewCond()
	if opts.RepoID > 0 {
		cond = cond.And(builder.Eq{"repo_id": opts.RepoID})
	}
	if opts.RemoteName != "" {
		cond = cond.And(builder.Eq{"remote_name": opts.RemoteName})
	}
	if opts.ID > 0 {
		cond = cond.And(builder.Eq{"id": opts.ID})
	}
	return cond
}

func init() {
	db.RegisterModel(new(PushMirror))
}

// GetRepository returns the path of the repository.
func (m *PushMirror) GetRepository(ctx context.Context) *Repository {
	if m.Repo != nil {
		return m.Repo
	}
	var err error
	m.Repo, err = GetRepositoryByID(ctx, m.RepoID)
	if err != nil {
		log.Error("getRepositoryByID[%d]: %v", m.ID, err)
	}
	return m.Repo
}

// GetRemoteName returns the name of the remote.
func (m *PushMirror) GetRemoteName() string {
	return m.RemoteName
}

// GetPublicKey returns a sanitized version of the public key.
// This should only be used when displaying the public key to the user, not for actual code.
func (m *PushMirror) GetPublicKey() string {
	return strings.TrimSuffix(m.PublicKey, "\n")
}

// SetPrivatekey encrypts the given private key and store it in the database.
// The ID of the push mirror must be known, so this should be done after the
// push mirror is inserted.
func (m *PushMirror) SetPrivatekey(ctx context.Context, privateKey []byte) error {
	m.PrivateKey = keying.PushMirror.Encrypt(privateKey, keying.ColumnAndID("private_key", m.ID))

	_, err := db.GetEngine(ctx).ID(m.ID).Cols("private_key").Update(m)
	return err
}

// Privatekey retrieves the encrypted private key and decrypts it.
func (m *PushMirror) Privatekey() ([]byte, error) {
	return keying.PushMirror.Decrypt(m.PrivateKey, keying.ColumnAndID("private_key", m.ID))
}

// UpdatePushMirror updates the push-mirror
func UpdatePushMirror(ctx context.Context, m *PushMirror) error {
	_, err := db.GetEngine(ctx).ID(m.ID).AllCols().Update(m)
	return err
}

// UpdatePushMirrorSettings writes the cols (interval, branch_filter) of m that its owner sets, and records
// repo.push_mirror.updated with the settings that differ from the stored row.
func UpdatePushMirrorSettings(ctx context.Context, m *PushMirror, cols ...string) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		stored := new(PushMirror)
		if has, err := db.GetEngine(ctx).ID(m.ID).Get(stored); err != nil {
			return err
		} else if has {
			if changes := audit_model.Changes(stored.AuditFields(), m.AuditFields()); changes != nil {
				if err := recordPushMirror(ctx, "repo.push_mirror.updated", m, changes); err != nil {
					return err
				}
			}
		}
		_, err := db.GetEngine(ctx).ID(m.ID).Cols(cols...).Update(m)
		return err
	})
}

// InsertPushMirror inserts m and records repo.push_mirror.created; credentials says whether its address carried user
// info, which RemoteAddress no longer holds.
func InsertPushMirror(ctx context.Context, m *PushMirror, credentials bool) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		if err := db.Insert(ctx, m); err != nil {
			return err
		}
		data := m.AuditFields()
		data["credentials_set"] = credentials
		return recordPushMirror(ctx, "repo.push_mirror.created", m, data)
	})
}

// RecordPushMirrorSync records repo.push_mirror.sync_requested, a sync someone asked for.
func RecordPushMirrorSync(ctx context.Context, m *PushMirror) error {
	return recordPushMirror(ctx, "repo.push_mirror.sync_requested", m, nil)
}

var DeletePushMirrors = deletePushMirrors

// deletePushMirrors deletes the push mirrors of opts, recording repo.push_mirror.deleted for each.
func deletePushMirrors(ctx context.Context, opts PushMirrorOptions) error {
	if opts.RepoID <= 0 {
		return util.NewInvalidArgumentErrorf("repoID required and must be set")
	}
	return db.WithTx(ctx, func(ctx context.Context) error {
		mirrors, err := db.Find[PushMirror](ctx, opts)
		if err != nil {
			return err
		}
		for _, m := range mirrors {
			if err := recordPushMirror(ctx, "repo.push_mirror.deleted", m, m.AuditFields()); err != nil {
				return err
			}
		}
		_, err = db.Delete[PushMirror](ctx, opts)
		return err
	})
}

// GetPushMirrorsByRepoID returns push-mirror information of a repository.
func GetPushMirrorsByRepoID(ctx context.Context, repoID int64, listOptions db.ListOptions) ([]*PushMirror, int64, error) {
	sess := db.GetEngine(ctx).Where("repo_id = ?", repoID)
	if listOptions.Page != 0 {
		sess = db.SetSessionPagination(sess, &listOptions)
		mirrors := make([]*PushMirror, 0, listOptions.PageSize)
		count, err := sess.FindAndCount(&mirrors)
		return mirrors, count, err
	}
	mirrors := make([]*PushMirror, 0, 10)
	count, err := sess.FindAndCount(&mirrors)
	return mirrors, count, err
}

// GetPushMirrorsSyncedOnCommit returns push-mirrors for this repo that should be updated by new commits
func GetPushMirrorsSyncedOnCommit(ctx context.Context, repoID int64) ([]*PushMirror, error) {
	mirrors := make([]*PushMirror, 0, 10)
	return mirrors, db.GetEngine(ctx).
		Where("repo_id = ? AND sync_on_commit = ?", repoID, true).
		Find(&mirrors)
}

// PushMirrorsIterate iterates all push-mirror repositories.
func PushMirrorsIterate(ctx context.Context, limit int, f func(idx int, bean any) error) error {
	sess := db.GetEngine(ctx).
		Table("push_mirror").
		Join("INNER", "`repository`", "`repository`.id = `push_mirror`.repo_id").
		Where("`push_mirror`.last_update + (`push_mirror`.`interval` / ?) <= ?", time.Second, time.Now().Unix()).
		And("`push_mirror`.`interval` != 0").
		And("`repository`.is_archived = ?", false).
		And("`repository`.trashed_unix = 0").
		OrderBy("last_update ASC")
	if limit > 0 {
		sess = sess.Limit(limit)
	}
	return sess.Iterate(new(PushMirror), f)
}

// GetPushMirrorRemoteAddress returns the address of associated with a repository's given remote.
func GetPushMirrorRemoteAddress(ownerName, repoName, remoteName string) (string, error) {
	repoPath := filepath.Join(setting.RepoRootPath, strings.ToLower(ownerName), strings.ToLower(repoName)+".git")

	remoteURL, err := git.GetRemoteAddress(context.Background(), repoPath, remoteName)
	if err != nil {
		return "", fmt.Errorf("get remote %s's address of %s/%s failed: %v", remoteName, ownerName, repoName, err)
	}

	u, err := giturl.Parse(remoteURL)
	if err != nil {
		return "", err
	}
	u.User = nil

	return u.String(), nil
}
