// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretscan

import (
	"context"
	"strconv"
	"strings"

	"forgejo.org/models/db"
	"forgejo.org/modules/json"
	"forgejo.org/modules/secretdetection"
	"forgejo.org/modules/timeutil"

	"xorm.io/builder"
)

// CustomRulePrefix starts the rule ID of a custom pattern: "custom-<id>", stored in Alert.RuleID like a built-in ID.
const CustomRulePrefix = "custom-"

// CustomPattern is a rule an organization (OwnerID is its ID) or the site (OwnerID 0) wrote. Rows are never removed:
// a delete sets DeletedUnix, so an alert of the rule keeps a label. The name is unique among an owner's live rows
// (a tombstone has its own time). The pattern is a rule, not a secret.
type CustomPattern struct {
	ID          int64              `xorm:"pk autoincr"`
	OwnerID     int64              `xorm:"UNIQUE(owner_name_deleted) NOT NULL DEFAULT 0"`
	Name        string             `xorm:"VARCHAR(64) UNIQUE(owner_name_deleted) NOT NULL"`
	Pattern     string             `xorm:"TEXT NOT NULL"`
	Keywords    string             `xorm:"TEXT NOT NULL"` // a JSON list
	Enabled     bool               `xorm:"NOT NULL DEFAULT true"`
	DeletedUnix timeutil.TimeStamp `xorm:"UNIQUE(owner_name_deleted) NOT NULL DEFAULT 0"`
	CreatedByID int64              `xorm:"NOT NULL DEFAULT 0"`
	CreatedUnix timeutil.TimeStamp `xorm:"created"`
	UpdatedUnix timeutil.TimeStamp `xorm:"updated"`
}

func (*CustomPattern) TableName() string { return "secret_custom_pattern" }

func init() {
	db.RegisterModel(new(CustomPattern))
}

// RuleID is the rule ID of the pattern's alerts.
func (p *CustomPattern) RuleID() string { return CustomRuleID(p.ID) }

// CustomRuleID is the rule ID of the custom pattern with this ID.
func CustomRuleID(id int64) string { return CustomRulePrefix + strconv.FormatInt(id, 10) }

// ParseCustomRuleID returns the pattern ID of a custom rule ID.
func ParseCustomRuleID(ruleID string) (int64, bool) {
	rest, ok := strings.CutPrefix(ruleID, CustomRulePrefix)
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	return id, err == nil && id > 0
}

// KeywordList is the keywords of the pattern; a stored value that can't be read gives none (the rule is then refused).
func (p *CustomPattern) KeywordList() []string {
	var k []string
	_ = json.Unmarshal([]byte(p.Keywords), &k)
	return k
}

// EncodeKeywords is the stored form of a keyword list.
func EncodeKeywords(keywords []string) string {
	b, _ := json.Marshal(keywords)
	return string(b)
}

// GetCustomPattern returns a pattern of the owner, deleted or not (not found: nil).
func GetCustomPattern(ctx context.Context, ownerID, id int64) (*CustomPattern, error) {
	p := &CustomPattern{}
	has, err := db.GetEngine(ctx).Where("owner_id = ? AND id = ?", ownerID, id).Get(p)
	if err != nil || !has {
		return nil, err
	}
	return p, nil
}

// FindCustomPatterns lists the live patterns of the owner, oldest first.
func FindCustomPatterns(ctx context.Context, ownerID int64) ([]*CustomPattern, error) {
	var out []*CustomPattern
	return out, db.GetEngine(ctx).Where("owner_id = ? AND deleted_unix = 0", ownerID).OrderBy("id").Find(&out)
}

// FindActiveCustomPatterns lists the enabled live patterns of the owners, the site's (owner 0) first, then by ID:
// the order in which the cap keeps them. It reads at most twice the cap, so rows the engine refuses can be skipped
// without loading every row a race of saves left.
func FindActiveCustomPatterns(ctx context.Context, ownerIDs ...int64) ([]*CustomPattern, error) {
	var out []*CustomPattern
	return out, db.GetEngine(ctx).Where(builder.In("owner_id", ownerIDs).And(builder.Eq{"enabled": true, "deleted_unix": 0})).
		OrderBy("owner_id, id").Limit(2 * secretdetection.MaxCustomRules).Find(&out)
}

// CountActiveCustomPatterns counts the enabled live patterns of the owners.
func CountActiveCustomPatterns(ctx context.Context, ownerIDs ...int64) (int64, error) {
	return db.GetEngine(ctx).Where(builder.In("owner_id", ownerIDs).And(builder.Eq{"enabled": true, "deleted_unix": 0})).Count(new(CustomPattern))
}

// GetCustomPatternsByIDs returns the patterns with these IDs, tombstones too, in one query.
func GetCustomPatternsByIDs(ctx context.Context, ids []int64) (map[int64]*CustomPattern, error) {
	out := map[int64]*CustomPattern{}
	if len(ids) == 0 {
		return out, nil
	}
	var list []*CustomPattern
	if err := db.GetEngine(ctx).In("id", ids).Find(&list); err != nil {
		return nil, err
	}
	for _, p := range list {
		out[p.ID] = p
	}
	return out, nil
}
