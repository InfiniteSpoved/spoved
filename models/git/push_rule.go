// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package git

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"

	"forgejo.org/models/db"
	"forgejo.org/modules/timeutil"

	"github.com/gobwas/glob"
)

// PushRuleMaxPatternLength caps each regex and the banned file pattern list.
// Go's regexp is linear time, so a length cap is enough against ReDoS.
const PushRuleMaxPatternLength = 1024

// PushRule holds the push rules of a repository. The pre-receive hook checks them on new commits.
type PushRule struct {
	ID                         int64  `xorm:"pk autoincr"`
	RepoID                     int64  `xorm:"UNIQUE NOT NULL"`
	CommitMessageRegex         string `xorm:"TEXT"`
	CommitMessageBannedRegex   string `xorm:"TEXT"`
	BranchNameRegex            string `xorm:"TEXT"`
	RequireVerifiedAuthorEmail bool   `xorm:"NOT NULL DEFAULT false"`
	MaxFileSizeMB              int64  `xorm:"NOT NULL DEFAULT 0"`
	// semicolon separated globs, matched against the lower-case path and its basename
	BannedFilePatterns string `xorm:"TEXT"`

	CommitMessageRe       *regexp.Regexp `xorm:"-"`
	CommitMessageBannedRe *regexp.Regexp `xorm:"-"`
	BranchNameRe          *regexp.Regexp `xorm:"-"`
	BannedFileGlobs       []glob.Glob    `xorm:"-"`

	CreatedUnix timeutil.TimeStamp `xorm:"created"`
	UpdatedUnix timeutil.TimeStamp `xorm:"updated"`
}

func init() {
	db.RegisterModel(new(PushRule))
}

func compilePushRuleRegex(s string) (*regexp.Regexp, error) {
	if s == "" {
		return nil, nil
	}
	if len(s) > PushRuleMaxPatternLength {
		return nil, fmt.Errorf("regular expression is longer than %d characters", PushRuleMaxPatternLength)
	}
	return regexp.Compile(s)
}

// Compile validates the rule and compiles its patterns. Unlike protected file patterns, an invalid glob is an error.
func (r *PushRule) Compile() (err error) {
	if r.MaxFileSizeMB < 0 {
		return fmt.Errorf("maximum file size must not be negative")
	}
	if r.CommitMessageRe, err = compilePushRuleRegex(r.CommitMessageRegex); err != nil {
		return err
	}
	if r.CommitMessageBannedRe, err = compilePushRuleRegex(r.CommitMessageBannedRegex); err != nil {
		return err
	}
	if r.BranchNameRe, err = compilePushRuleRegex(r.BranchNameRegex); err != nil {
		return err
	}
	if len(r.BannedFilePatterns) > PushRuleMaxPatternLength {
		return fmt.Errorf("banned file patterns are longer than %d characters", PushRuleMaxPatternLength)
	}
	r.BannedFileGlobs = nil
	for expr := range strings.SplitSeq(strings.ToLower(r.BannedFilePatterns), ";") {
		if expr = strings.TrimSpace(expr); expr == "" {
			continue
		}
		g, err := glob.Compile(expr, '/')
		if err != nil {
			return fmt.Errorf("invalid glob %q: %w", expr, err)
		}
		r.BannedFileGlobs = append(r.BannedFileGlobs, g)
	}
	return nil
}

// IsBannedFile reports whether the path or its basename matches a banned glob. Call Compile first.
func (r *PushRule) IsBannedFile(filePath string) bool {
	lpath := strings.ToLower(filePath)
	for _, g := range r.BannedFileGlobs {
		if g.Match(lpath) || g.Match(path.Base(lpath)) {
			return true
		}
	}
	return false
}

// GetPushRule returns the push rule of the repository, or nil if it has none.
func GetPushRule(ctx context.Context, repoID int64) (*PushRule, error) {
	rule := &PushRule{RepoID: repoID}
	if has, err := db.GetEngine(ctx).Get(rule); err != nil || !has {
		return nil, err
	}
	return rule, nil
}

// UpsertPushRule creates or replaces the push rule of rule.RepoID.
func UpsertPushRule(ctx context.Context, rule *PushRule) error {
	existing, err := GetPushRule(ctx, rule.RepoID)
	if err != nil {
		return err
	}
	if existing == nil {
		_, err = db.GetEngine(ctx).Insert(rule)
		return err
	}
	rule.ID = existing.ID
	_, err = db.GetEngine(ctx).ID(rule.ID).AllCols().Update(rule)
	return err
}
