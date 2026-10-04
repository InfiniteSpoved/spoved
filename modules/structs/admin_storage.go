// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package structs

// StorageSummary is the storage used by all repositories of the instance, in bytes
type StorageSummary struct {
	RepoCount   int64 `json:"repo_count"`
	GitSize     int64 `json:"git_size"`
	LFSSize     int64 `json:"lfs_size"`
	TotalSize   int64 `json:"total_size"`
	AverageSize int64 `json:"average_size"`
}

// OwnerStorage is the storage used by the repositories of one owner, in bytes
type OwnerStorage struct {
	OwnerID   int64  `json:"owner_id"`
	OwnerName string `json:"owner_name"`
	// "user" or "org"
	OwnerType string `json:"owner_type"`
	RepoCount int64  `json:"repo_count"`
	GitSize   int64  `json:"git_size"`
	LFSSize   int64  `json:"lfs_size"`
	TotalSize int64  `json:"total_size"`
	// share of the instance's total size, in percent
	Percent float64 `json:"percent"`
}
