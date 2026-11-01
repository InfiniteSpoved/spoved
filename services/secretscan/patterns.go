// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretscan

import (
	"context"
	"errors"
	"strings"

	audit_model "forgejo.org/models/audit"
	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	secretscan_model "forgejo.org/models/secretscan"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/log"
	"forgejo.org/modules/secretdetection"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"
)

// MaxPatternNameLen is the longest name of a custom pattern, in bytes.
const MaxPatternNameLen = 64

var (
	// ErrPatternLimit is a save that would make more than secretdetection.MaxCustomRules enabled patterns of the
	// site and the organization together (the site's come first, so the site's own count is the limit for a site
	// pattern).
	ErrPatternLimit = util.NewInvalidArgumentErrorf("too many custom patterns")
	// ErrPatternNameTaken is a name another live pattern of the owner has.
	ErrPatternNameTaken = util.NewAlreadyExistErrorf("a pattern of that name exists")
	// ErrPatternNotFound is a pattern the owner doesn't have, or one that is deleted.
	ErrPatternNotFound = util.NewNotExistErrorf("pattern does not exist")
)

// PatternInput is what a user writes: the rule and a sample the rule must find a secret in (it is checked and then
// dropped, never stored).
type PatternInput struct {
	Name, Pattern, Sample string
	Keywords              []string
}

// normalize strips the name and the keywords and drops repeated keywords (compared in lower case).
func (in PatternInput) normalize() PatternInput {
	in.Name = strings.TrimSpace(in.Name)
	var keywords []string
	seen := map[string]bool{}
	for _, k := range in.Keywords {
		k = strings.TrimSpace(k)
		if !seen[strings.ToLower(k)] {
			seen[strings.ToLower(k)] = true
			keywords = append(keywords, k)
		}
	}
	in.Keywords = keywords
	return in
}

// ValidatePattern is the one check of a pattern on save and in the Test button: the engine's ValidateCustomRule, and
// the name limits of the store. Nothing is stored.
func ValidatePattern(in PatternInput) error {
	in = in.normalize()
	if len(in.Name) > MaxPatternNameLen || util.StripControl(in.Name) != in.Name {
		return secretdetection.ErrCustomNameInvalid
	}
	return secretdetection.ValidateCustomRule(in.Name, in.Pattern, in.Sample, in.Keywords)
}

// ownerID is the owner of a pattern: the organization's ID, 0 for the site's.
func ownerID(org *user_model.User) int64 {
	if org == nil {
		return 0
	}
	return org.ID
}

// capOwners are the owners whose enabled patterns count against the cap of the owner's patterns.
func capOwners(owner int64) []int64 {
	if owner == 0 {
		return []int64{0}
	}
	return []int64{0, owner}
}

func checkCap(ctx context.Context, owner int64) error {
	n, err := secretscan_model.CountActiveCustomPatterns(ctx, capOwners(owner)...)
	if err != nil {
		return err
	}
	if n >= secretdetection.MaxCustomRules {
		return ErrPatternLimit
	}
	return nil
}

func nameTaken(ctx context.Context, owner int64, name string, exceptID int64) (bool, error) {
	return db.GetEngine(ctx).Where("owner_id = ? AND name = ? AND deleted_unix = 0 AND id <> ?", owner, name, exceptID).
		Exist(new(secretscan_model.CustomPattern))
}

func checkName(ctx context.Context, owner int64, name string, exceptID int64) error {
	taken, err := nameTaken(ctx, owner, name, exceptID)
	if err == nil && taken {
		err = ErrPatternNameTaken
	}
	return err
}

// recordPattern writes the audit entry of a change. The pattern is a rule, not a secret, so its text is recorded.
func recordPattern(ctx context.Context, action string, p *secretscan_model.CustomPattern, data map[string]any) error {
	if data == nil {
		data = map[string]any{}
	}
	data["name"], data["pattern"], data["keyword_count"] = p.Name, p.Pattern, len(p.KeywordList())
	return audit_model.Record(ctx, audit_model.Event{
		Action: action, Result: audit_model.ResultSuccess, OwnerID: p.OwnerID,
		TargetType: "secret_pattern", TargetID: p.ID, TargetName: p.Name, Data: data,
	})
}

// levelAction picks the key of the organization's or the site's level; each key is a literal at its call site.
func levelAction(owner int64, a audit_model.LevelActions) string {
	if owner == 0 {
		return a.Admin
	}
	return a.Org
}

// CreatePattern validates and stores a pattern of the organization (nil: the site). The caller checked who may.
// It is enabled and counts against the cap from the start; history is not scanned again.
func CreatePattern(ctx context.Context, doer, org *user_model.User, in PatternInput) (*secretscan_model.CustomPattern, error) {
	in = in.normalize()
	if err := ValidatePattern(in); err != nil {
		return nil, err
	}
	owner := ownerID(org)
	p := &secretscan_model.CustomPattern{
		OwnerID: owner, Name: in.Name, Pattern: in.Pattern, Keywords: secretscan_model.EncodeKeywords(in.Keywords),
		Enabled: true, CreatedByID: doer.ID,
	}
	err := db.WithTx(ctx, func(ctx context.Context) error {
		if err := checkName(ctx, owner, p.Name, 0); err != nil {
			return err
		}
		if err := checkCap(ctx, owner); err != nil {
			return err
		}
		if _, err := db.GetEngine(ctx).Insert(p); err != nil {
			return err
		}
		return recordPattern(ctx, levelAction(owner, audit_model.LevelActions{Org: "org.secret_pattern.created", Admin: "admin.secret_pattern.created"}), p, nil)
	})
	if err != nil {
		if errors.Is(err, util.ErrAlreadyExist) || errors.Is(err, util.ErrInvalidArgument) {
			return nil, err
		}
		if taken, terr := nameTaken(ctx, owner, p.Name, 0); terr == nil && taken {
			return nil, ErrPatternNameTaken // lost a race on the unique key
		}
		return nil, err
	}
	return p, nil
}

// load reads a live pattern of the owner for a change.
func load(ctx context.Context, owner, id int64) (*secretscan_model.CustomPattern, error) {
	p, err := secretscan_model.GetCustomPattern(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	if p == nil || p.DeletedUnix != 0 {
		return nil, ErrPatternNotFound
	}
	return p, nil
}

// UpdatePattern replaces the name, pattern and keywords of a live pattern, which is checked again with the sample.
// A save that changes nothing records nothing. Alerts already found keep their rule.
func UpdatePattern(ctx context.Context, org *user_model.User, id int64, in PatternInput) (*secretscan_model.CustomPattern, error) {
	in = in.normalize()
	if err := ValidatePattern(in); err != nil {
		return nil, err
	}
	owner := ownerID(org)
	var p *secretscan_model.CustomPattern
	err := db.WithTx(ctx, func(ctx context.Context) error {
		var err error
		if p, err = load(ctx, owner, id); err != nil {
			return err
		}
		keywords := secretscan_model.EncodeKeywords(in.Keywords)
		if p.Name == in.Name && p.Pattern == in.Pattern && p.Keywords == keywords {
			return nil
		}
		if err := checkName(ctx, owner, in.Name, id); err != nil {
			return err
		}
		prev := p.Pattern
		n, err := db.GetEngine(ctx).Where("id = ? AND owner_id = ? AND deleted_unix = 0", id, owner).Cols("name", "pattern", "keywords").
			Update(&secretscan_model.CustomPattern{Name: in.Name, Pattern: in.Pattern, Keywords: keywords})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrPatternNotFound // deleted meanwhile
		}
		p.Name, p.Pattern, p.Keywords = in.Name, in.Pattern, keywords
		return recordPattern(ctx, levelAction(owner, audit_model.LevelActions{Org: "org.secret_pattern.updated", Admin: "admin.secret_pattern.updated"}), p, map[string]any{"previous_pattern": prev})
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// SetPatternEnabled turns a live pattern on or off. Off stops it from the next scan on; its alerts stay as they are.
// Turning it on counts against the cap again.
func SetPatternEnabled(ctx context.Context, org *user_model.User, id int64, enabled bool) error {
	owner := ownerID(org)
	return db.WithTx(ctx, func(ctx context.Context) error {
		p, err := load(ctx, owner, id)
		if err != nil || p.Enabled == enabled {
			return err
		}
		if enabled {
			if err := checkCap(ctx, owner); err != nil {
				return err
			}
		}
		n, err := db.GetEngine(ctx).Where("id = ? AND owner_id = ? AND deleted_unix = 0 AND enabled = ?", id, owner, !enabled).Cols("enabled").
			Update(&secretscan_model.CustomPattern{Enabled: enabled})
		if err != nil || n == 0 {
			return err // nothing matched: someone else changed it, which is the state asked for or a delete
		}
		if enabled {
			return recordPattern(ctx, levelAction(owner, audit_model.LevelActions{Org: "org.secret_pattern.enabled", Admin: "admin.secret_pattern.enabled"}), p, nil)
		}
		return recordPattern(ctx, levelAction(owner, audit_model.LevelActions{Org: "org.secret_pattern.disabled", Admin: "admin.secret_pattern.disabled"}), p, nil)
	})
}

// DeletePattern marks a live pattern deleted and off. The row stays for the label of its alerts. Its tombstone time
// is made unique among the tombstones of the same name (the unique key has one per second).
func DeletePattern(ctx context.Context, org *user_model.User, id int64) error {
	owner := ownerID(org)
	return db.WithTx(ctx, func(ctx context.Context) error {
		p, err := load(ctx, owner, id)
		if err != nil {
			return err
		}
		at := timeutil.TimeStampNow()
		for {
			taken, err := db.GetEngine(ctx).Where("owner_id = ? AND name = ? AND deleted_unix = ?", owner, p.Name, at).Exist(new(secretscan_model.CustomPattern))
			if err != nil {
				return err
			}
			if !taken {
				break
			}
			at++
		}
		n, err := db.GetEngine(ctx).Where("id = ? AND owner_id = ? AND deleted_unix = 0", id, owner).Cols("enabled", "deleted_unix").
			Update(&secretscan_model.CustomPattern{DeletedUnix: at})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrPatternNotFound
		}
		return recordPattern(ctx, levelAction(owner, audit_model.LevelActions{Org: "org.secret_pattern.deleted", Admin: "admin.secret_pattern.deleted"}), p, nil)
	})
}

// customRules are the rules of the enabled custom patterns that apply to the repository: the site's and, for a
// repository of an organization, the organization's (a site pattern can't be turned off by an organization). One
// query. A pattern the engine refuses now is skipped and logged, and at most MaxCustomRules are taken, the site's
// first.
func customRules(ctx context.Context, repo *repo_model.Repository) ([]secretdetection.Rule, error) {
	if err := repo.LoadOwner(ctx); err != nil {
		return nil, err
	}
	owners := []int64{0}
	if repo.Owner.IsOrganization() {
		owners = append(owners, repo.OwnerID)
	}
	patterns, err := secretscan_model.FindActiveCustomPatterns(ctx, owners...)
	if err != nil {
		return nil, err
	}
	var rules []secretdetection.Rule
	for _, p := range patterns {
		if len(rules) >= secretdetection.MaxCustomRules {
			log.Warn("secret scanning: repository %d has more than %d custom patterns, the rest are not used", repo.ID, secretdetection.MaxCustomRules)
			break
		}
		keywords := p.KeywordList()
		if err := secretdetection.ValidateStoredRule(p.Name, p.Pattern, keywords); err != nil {
			log.Warn("secret scanning: custom pattern %d is skipped: %v", p.ID, err)
			continue
		}
		rules = append(rules, secretdetection.CustomRule(p.RuleID(), p.Name, p.Pattern, keywords))
	}
	return rules, nil
}

// RuleLabel is how an alert's rule is shown. Name of a deleted pattern is empty unless the viewer may read it.
type RuleLabel struct {
	Name                       string
	Generic, Deleted, Disabled bool
}

// RuleLabels names the rules of alerts: the built-in ones from the engine, a custom one from its row in one query for
// all of them. A pattern that is deleted (or has no row) is Deleted, and its name is given only where seeDeleted
// says the viewer may read it for the pattern's owner (nil: never): the others get the generic label.
func RuleLabels(ctx context.Context, ruleIDs []string, seeDeleted func(ownerID int64) bool) (map[string]RuleLabel, error) {
	out := map[string]RuleLabel{}
	var ids []int64
	for _, ruleID := range ruleIDs {
		if id, ok := secretscan_model.ParseCustomRuleID(ruleID); ok {
			ids = append(ids, id)
			continue
		}
		name, generic := RuleInfo(ruleID)
		out[ruleID] = RuleLabel{Name: name, Generic: generic}
	}
	patterns, err := secretscan_model.GetCustomPatternsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		l := RuleLabel{Deleted: true}
		switch p := patterns[id]; {
		case p == nil:
		case p.DeletedUnix != 0:
			if seeDeleted != nil && seeDeleted(p.OwnerID) {
				l.Name = p.Name
			}
		default:
			l = RuleLabel{Name: p.Name, Disabled: !p.Enabled}
		}
		out[secretscan_model.CustomRuleID(id)] = l
	}
	return out, nil
}
