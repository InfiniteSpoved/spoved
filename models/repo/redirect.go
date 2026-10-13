// Copyright 2017 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"context"
	"fmt"
	"strings"

	"forgejo.org/models/db"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/util"
)

// ErrRedirectNotExist represents a "RedirectNotExist" kind of error.
type ErrRedirectNotExist struct {
	OwnerID           int64
	RepoName          string
	MissingPermission bool
}

// IsErrRedirectNotExist check if an error is an ErrRepoRedirectNotExist.
func IsErrRedirectNotExist(err error) bool {
	_, ok := err.(ErrRedirectNotExist)
	return ok
}

func (err ErrRedirectNotExist) Error() string {
	return fmt.Sprintf("repository redirect does not exist [uid: %d, name: %s]", err.OwnerID, err.RepoName)
}

func (err ErrRedirectNotExist) Unwrap() error {
	return util.ErrNotExist
}

// Redirect represents that a repo name should be redirected to another. GroupID is the project of the old path,
// 0 for the root of the owner.
type Redirect struct {
	ID             int64  `xorm:"pk autoincr"`
	OwnerID        int64  `xorm:"UNIQUE(s)"`
	GroupID        int64  `xorm:"UNIQUE(s) NOT NULL DEFAULT 0"`
	LowerName      string `xorm:"UNIQUE(s) INDEX NOT NULL"`
	RedirectRepoID int64  // repoID to redirect to
}

// TableName represents real table name in database
func (Redirect) TableName() string {
	return "repo_redirect"
}

// RepoGroupRedirect redirects the old name of a renamed project to the project.
type RepoGroupRedirect struct { //revive:disable-line:exported
	ID              int64  `xorm:"pk autoincr"`
	OwnerID         int64  `xorm:"UNIQUE(s) NOT NULL"`
	LowerName       string `xorm:"UNIQUE(s) NOT NULL"`
	RedirectGroupID int64  `xorm:"INDEX NOT NULL"`
}

func init() {
	db.RegisterModel(new(Redirect))
	db.RegisterModel(new(RepoGroupRedirect))
}

// GetRedirect returns the repository that the name redirects to, in the project groupID (0: the owner's root).
func GetRedirect(ctx context.Context, ownerID, groupID int64, repoName string) (int64, error) {
	repoName = strings.ToLower(repoName)
	redirect := new(Redirect)
	if has, err := db.GetEngine(ctx).Where("owner_id = ? AND group_id = ? AND lower_name = ?", ownerID, groupID, repoName).Get(redirect); err != nil {
		return 0, err
	} else if !has {
		return 0, ErrRedirectNotExist{OwnerID: ownerID, RepoName: repoName}
	}
	return redirect.RedirectRepoID, nil
}

// NewRedirect makes the old path of a repository, in the project groupID (0: the owner's root), redirect to it.
func NewRedirect(ctx context.Context, ownerID, groupID, repoID int64, oldRepoName string) error {
	oldRepoName = strings.ToLower(oldRepoName)
	if _, err := db.GetEngine(ctx).Where("owner_id = ? AND group_id = ? AND lower_name = ?", ownerID, groupID, oldRepoName).Delete(new(Redirect)); err != nil {
		return err
	}
	return db.Insert(ctx, &Redirect{
		OwnerID:        ownerID,
		GroupID:        groupID,
		LowerName:      oldRepoName,
		RedirectRepoID: repoID,
	})
}

// TakeRedirectPath is called by every path that puts the repository repoID (0: a new one) at name in the project
// groupID (0: the root) of owner, in its transaction, and removes the redirects there. In an organization a redirect
// to another repository, or at the root the redirect of a renamed project, refuses like a taken name: an owner
// removes it first on the Redirects page, so that old links and clones don't silently reach a new repository.
func TakeRedirectPath(ctx context.Context, owner *user_model.User, groupID int64, name string, repoID int64) error {
	lowerName := strings.ToLower(name)
	if owner.IsOrganization() {
		has, err := db.GetEngine(ctx).Where("owner_id = ? AND group_id = ? AND lower_name = ? AND redirect_repo_id <> ?", owner.ID, groupID, lowerName, repoID).Exist(new(Redirect))
		if err == nil && !has && groupID == 0 {
			has, err = db.GetEngine(ctx).Where("owner_id = ? AND lower_name = ?", owner.ID, lowerName).Exist(new(RepoGroupRedirect))
		}
		if err != nil {
			return err
		} else if has {
			return ErrRepoAlreadyExist{Uname: owner.Name, Name: name}
		}
	}
	_, err := db.GetEngine(ctx).Where("owner_id = ? AND group_id = ? AND lower_name = ?", owner.ID, groupID, lowerName).Delete(new(Redirect))
	return err
}

// takeGroupRedirectPath is TakeRedirectPath for the project groupID (0: a new one) taking name.
func takeGroupRedirectPath(ctx context.Context, owner *user_model.User, name string, groupID int64) error {
	lowerName := strings.ToLower(name)
	has, err := db.GetEngine(ctx).Where("owner_id = ? AND group_id = 0 AND lower_name = ?", owner.ID, lowerName).Exist(new(Redirect))
	if err == nil && !has {
		has, err = db.GetEngine(ctx).Where("owner_id = ? AND lower_name = ? AND redirect_group_id <> ?", owner.ID, lowerName, groupID).Exist(new(RepoGroupRedirect))
	}
	if err != nil {
		return err
	} else if has {
		return ErrRepoAlreadyExist{Uname: owner.Name, Name: name}
	}
	_, err = db.GetEngine(ctx).Where("owner_id = ? AND lower_name = ?", owner.ID, lowerName).Delete(new(RepoGroupRedirect))
	return err
}

// GetRepoGroupRedirect returns the project that the old project name redirects to.
func GetRepoGroupRedirect(ctx context.Context, ownerID int64, name string) (int64, error) {
	redirect := new(RepoGroupRedirect)
	if has, err := db.GetEngine(ctx).Where("owner_id = ? AND lower_name = ?", ownerID, strings.ToLower(name)).Get(redirect); err != nil {
		return 0, err
	} else if !has {
		return 0, ErrRepoGroupNotExist
	}
	return redirect.RedirectGroupID, nil
}

// FindRedirects returns the repository and project redirects of an owner, by old name.
func FindRedirects(ctx context.Context, ownerID int64) ([]*Redirect, []*RepoGroupRedirect, error) {
	var repoRedirects []*Redirect
	var groupRedirects []*RepoGroupRedirect
	if err := db.GetEngine(ctx).Where("owner_id = ?", ownerID).OrderBy("group_id, lower_name").Find(&repoRedirects); err != nil {
		return nil, nil, err
	}
	return repoRedirects, groupRedirects, db.GetEngine(ctx).Where("owner_id = ?", ownerID).OrderBy("lower_name").Find(&groupRedirects)
}

// DeleteOwnerRedirect deletes a repository redirect (or a project redirect if group is true) of the owner by ID; one
// of another owner is not found.
func DeleteOwnerRedirect(ctx context.Context, ownerID, id int64, group bool) error {
	var bean any = new(Redirect)
	if group {
		bean = new(RepoGroupRedirect)
	}
	n, err := db.GetEngine(ctx).Where("owner_id = ? AND id = ?", ownerID, id).Delete(bean)
	if err == nil && n == 0 {
		err = util.ErrNotExist
	}
	return err
}
