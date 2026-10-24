// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package git

import (
	"time"

	"forgejo.org/modules/util"
)

// Merge queue defaults, used where a rule stores 0
const (
	DefaultMergeQueueBuildConcurrency = 5
	DefaultMergeQueueMinGroupSize     = 1
	DefaultMergeQueueMaxGroupSize     = 5
	DefaultMergeQueueWaitMinutes      = 5
	DefaultMergeQueueCheckTimeout     = 60
	MaxMergeQueueSize                 = 100  // upper bound of the concurrency and group sizes
	MaxMergeQueueMinutes              = 1440 // upper bound of the wait time and the check timeout
)

// MergeQueueConfig is the merge queue setting of a rule with the defaults applied.
type MergeQueueConfig struct {
	BuildConcurrency int
	MinGroupSize     int
	MaxGroupSize     int
	WaitTime         time.Duration
	CheckTimeout     time.Duration
	MergeStyle       string // "" = the style chosen for the pull request
}

func orDefault(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// MergeQueueConfig returns the merge queue setting of the rule with the defaults applied.
func (protectBranch *ProtectedBranch) MergeQueueConfig() MergeQueueConfig {
	return MergeQueueConfig{
		BuildConcurrency: orDefault(protectBranch.MergeQueueBuildConcurrency, DefaultMergeQueueBuildConcurrency),
		MinGroupSize:     orDefault(protectBranch.MergeQueueMinGroupSize, DefaultMergeQueueMinGroupSize),
		MaxGroupSize:     orDefault(protectBranch.MergeQueueMaxGroupSize, DefaultMergeQueueMaxGroupSize),
		WaitTime:         time.Duration(orDefault(protectBranch.MergeQueueWaitMinutes, DefaultMergeQueueWaitMinutes)) * time.Minute,
		CheckTimeout:     time.Duration(orDefault(protectBranch.MergeQueueCheckTimeoutMinutes, DefaultMergeQueueCheckTimeout)) * time.Minute,
		MergeStyle:       protectBranch.MergeQueueMergeStyle,
	}
}

// MergeQueueRule returns the first of rules (as GetMatchedProtectedBranchRules returns them: repository, project,
// organization) that requires a merge queue, nil if none does.
func MergeQueueRule(rules []*ProtectedBranch) *ProtectedBranch {
	for _, pb := range rules {
		if pb.RequireMergeQueue {
			return pb
		}
	}
	return nil
}

// ErrInvalidMergeQueueConfig is returned for merge queue settings out of range
var ErrInvalidMergeQueueConfig = util.NewInvalidArgumentErrorf("merge queue settings out of range: concurrency and group sizes 0 to %d, the minimum group size at most the maximum, times 0 to %d minutes, merge style merge, rebase, rebase-merge, squash or empty", MaxMergeQueueSize, MaxMergeQueueMinutes)

// ValidateMergeQueue checks the merge queue settings of the rule; 0 means the default.
func (protectBranch *ProtectedBranch) ValidateMergeQueue() error {
	inRange := func(v, maxV int) bool { return v >= 0 && v <= maxV }
	switch protectBranch.MergeQueueMergeStyle {
	case "", "merge", "rebase", "rebase-merge", "squash":
	default:
		return ErrInvalidMergeQueueConfig
	}
	cfg := protectBranch.MergeQueueConfig()
	if !inRange(protectBranch.MergeQueueBuildConcurrency, MaxMergeQueueSize) || !inRange(protectBranch.MergeQueueMinGroupSize, MaxMergeQueueSize) ||
		!inRange(protectBranch.MergeQueueMaxGroupSize, MaxMergeQueueSize) || cfg.MinGroupSize > cfg.MaxGroupSize ||
		!inRange(protectBranch.MergeQueueWaitMinutes, MaxMergeQueueMinutes) || !inRange(protectBranch.MergeQueueCheckTimeoutMinutes, MaxMergeQueueMinutes) {
		return ErrInvalidMergeQueueConfig
	}
	return nil
}
