// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package structs

import "time"

// TrashItem is a deleted repository in the trash, restorable by site admins until it is purged
type TrashItem struct {
	// the kind of item, "repo"
	Type string `json:"type"`
	ID   int64  `json:"id"`
	// the owner and name the repository had when it was deleted
	FullName  string `json:"full_name"`
	DeletedBy string `json:"deleted_by"`
	// when the repository was moved to the trash
	Deleted time.Time `json:"deleted"`
	// days left before it is deleted permanently
	DaysRemaining int `json:"days_remaining"`
	// size of the repository in bytes
	Size int64 `json:"size"`
	// true while a permanent deletion is in progress
	Purging bool `json:"purging"`
}
