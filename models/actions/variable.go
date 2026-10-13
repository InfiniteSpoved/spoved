// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"context"
	"slices"
	"strings"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/log"
	"forgejo.org/modules/timeutil"

	"xorm.io/builder"
)

// ActionVariable represents a variable that can be used in actions
//
// It can be:
//  1. global variable, OwnerID is 0 and RepoID is 0
//  2. org/user level variable, OwnerID is org/user ID, GroupID and RepoID are 0
//  3. repo level variable, OwnerID is 0 and RepoID is repo ID
//  4. project level variable, OwnerID is the org ID, GroupID the project (repo_group) ID and RepoID is 0
//
// Please note that it's not acceptable to have both OwnerID and RepoID to be non-zero,
// or it will be complicated to find variables belonging to a specific owner.
// For example, conditions like `OwnerID = 1` will also return variable {OwnerID: 1, RepoID: 1},
// but it's a repo level variable, not an org/user level variable.
// To avoid this, make it clear with {OwnerID: 0, RepoID: 1} for repo level variables.
type ActionVariable struct {
	ID          int64              `xorm:"pk autoincr"`
	OwnerID     int64              `xorm:"UNIQUE(owner_repo_name)"`
	GroupID     int64              `xorm:"UNIQUE(owner_repo_name) NOT NULL DEFAULT 0"`
	RepoID      int64              `xorm:"INDEX UNIQUE(owner_repo_name)"`
	Name        string             `xorm:"UNIQUE(owner_repo_name) NOT NULL"`
	Data        string             `xorm:"LONGTEXT NOT NULL"`
	CreatedUnix timeutil.TimeStamp `xorm:"created NOT NULL"`
	UpdatedUnix timeutil.TimeStamp `xorm:"updated"`
}

func init() {
	db.RegisterModel(new(ActionVariable))
}

// lockGroup locks the project row (nothing for groupID 0), so that deleting the project (which deletes its variables)
// and adding, changing or deleting one run one after the other.
func lockGroup(ctx context.Context, ownerID, groupID int64) error {
	if groupID == 0 {
		return nil
	}
	_, err := repo_model.LockRepoGroup(ctx, ownerID, groupID)
	return err
}

func InsertVariable(ctx context.Context, ownerID, groupID, repoID int64, name, data string) (*ActionVariable, error) {
	if ownerID != 0 && repoID != 0 {
		// It's trying to create a variable that belongs to a repository, but OwnerID has been set accidentally.
		// Remove OwnerID to avoid confusion; it's not worth returning an error here.
		ownerID = 0
	}
	if repoID != 0 || ownerID == 0 {
		groupID = 0
	}

	variable := &ActionVariable{
		OwnerID: ownerID,
		GroupID: groupID,
		RepoID:  repoID,
		Name:    strings.ToUpper(name),
		Data:    data,
	}
	return variable, db.WithTx(ctx, func(ctx context.Context) error {
		if err := lockGroup(ctx, ownerID, groupID); err != nil {
			return err
		}
		return db.Insert(ctx, variable)
	})
}

type FindVariablesOpts struct {
	db.ListOptions
	RepoID  int64
	OwnerID int64 // it will be ignored if RepoID is set
	GroupID int64 // the project of OwnerID; 0 is the organization/user level itself. Ignored if RepoID is set
	Name    string
}

func (opts FindVariablesOpts) ToConds() builder.Cond {
	cond := builder.NewCond()
	// Since we now support instance-level variables,
	// there is no need to check for null values for `owner_id` and `repo_id`
	cond = cond.And(builder.Eq{"repo_id": opts.RepoID})
	if opts.RepoID != 0 { // if RepoID is set
		// ignore OwnerID and treat it as 0
		cond = cond.And(builder.Eq{"owner_id": 0, "group_id": 0})
	} else {
		cond = cond.And(builder.Eq{"owner_id": opts.OwnerID, "group_id": opts.GroupID})
	}

	if opts.Name != "" {
		cond = cond.And(builder.Eq{"name": strings.ToUpper(opts.Name)})
	}
	return cond
}

var _ db.FindOptionsOrder = FindVariablesOpts{}

// ToOrders implements db.FindOptionsOrder to have a stable sort order
func (opts FindVariablesOpts) ToOrders() string {
	// Sort by name. ID serves as a tie-breaker.
	return "name, id"
}

func FindVariables(ctx context.Context, opts FindVariablesOpts) ([]*ActionVariable, error) {
	return db.Find[ActionVariable](ctx, opts)
}

func UpdateVariable(ctx context.Context, variable *ActionVariable) (bool, error) {
	var count int64
	err := db.WithTx(ctx, func(ctx context.Context) (err error) {
		if err = lockGroup(ctx, variable.OwnerID, variable.GroupID); err != nil {
			return err
		}
		count, err = db.GetEngine(ctx).ID(variable.ID).Where("owner_id = ? AND group_id = ? AND repo_id = ?", variable.OwnerID, variable.GroupID, variable.RepoID).Cols("name", "data").
			Update(&ActionVariable{
				Name: variable.Name,
				Data: variable.Data,
			})
		return err
	})
	return count != 0, err
}

func DeleteVariable(ctx context.Context, variableID, ownerID, groupID, repoID int64) (bool, error) {
	var count int64
	err := db.WithTx(ctx, func(ctx context.Context) (err error) {
		if err = lockGroup(ctx, ownerID, groupID); err != nil {
			return err
		}
		count, err = db.GetEngine(ctx).Table("action_variable").Where("id = ? AND owner_id = ? AND group_id = ? AND repo_id = ?", variableID, ownerID, groupID, repoID).Delete()
		return err
	})
	return count != 0, err
}

func GetVariablesOfRun(ctx context.Context, run *ActionRun) (map[string]string, error) {
	variables := map[string]string{}

	if err := run.LoadRepo(ctx); err != nil {
		log.Error("LoadRepo: %v", err)
		return nil, err
	}

	// Global
	globalVariables, err := db.Find[ActionVariable](ctx, FindVariablesOpts{})
	if err != nil {
		log.Error("find global variables: %v", err)
		return nil, err
	}

	// Org / User level
	ownerVariables, err := db.Find[ActionVariable](ctx, FindVariablesOpts{OwnerID: run.Repo.OwnerID})
	if err != nil {
		log.Error("find variables of org: %d, error: %v", run.Repo.OwnerID, err)
		return nil, err
	}

	// Project level (the repository's current project)
	var groupVariables []*ActionVariable
	if run.Repo.GroupID != 0 {
		groupVariables, err = db.Find[ActionVariable](ctx, FindVariablesOpts{OwnerID: run.Repo.OwnerID, GroupID: run.Repo.GroupID})
		if err != nil {
			log.Error("find variables of project: %d, error: %v", run.Repo.GroupID, err)
			return nil, err
		}
	}

	// Repo level
	repoVariables, err := db.Find[ActionVariable](ctx, FindVariablesOpts{RepoID: run.RepoID})
	if err != nil {
		log.Error("find variables of repo: %d, error: %v", run.RepoID, err)
		return nil, err
	}

	// Level precedence: Repo > Project > Org / User > Global
	for _, v := range slices.Concat(globalVariables, ownerVariables, groupVariables, repoVariables) {
		variables[v.Name] = v.Data
	}

	return variables, nil
}
