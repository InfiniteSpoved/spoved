// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repo

import (
	"context"
	"slices"

	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/container"
	"forgejo.org/modules/setting"

	"xorm.io/builder"
)

// PolicyUnitTypes are the units the site policy decides on: every unit but Code.
func PolicyUnitTypes() []unit.Type {
	return slices.DeleteFunc(slices.Clone(unit.AllRepoUnitTypes), func(t unit.Type) bool { return t == unit.TypeCode })
}

// HiddenUnits returns the units the site policy hides in repositories of owner, which must be loaded from the
// database (a nil owner hides everything). The repo_unit rows are kept, so a unit comes back when it is allowed again.
// App.ini DISABLED_REPO_UNITS is checked separately (UnitGlobalDisabled).
func HiddenUnits(ctx context.Context, owner *user_model.User) container.Set[unit.Type] {
	hidden := container.Set[unit.Type]{}
	if owner != nil && !owner.IsOrganization() {
		return hidden
	}
	p := setting.Config().RepoUnitPolicy.Value(ctx)
	for _, t := range PolicyUnitTypes() {
		if owner == nil || !p.OrgAllowed(unit.Units[t].NameKey, owner.ID) {
			hidden.Add(t)
		}
	}
	return hidden
}

// HiddenUnitCond is HiddenUnits in SQL: it matches the rows whose unit (the typeCol column) is hidden in
// repositories owned by ownerCol. It is empty when nothing is hidden.
func HiddenUnitCond(ctx context.Context, ownerCol, typeCol string) builder.Cond {
	return hiddenUnitCond(ctx, ownerCol, PolicyUnitTypes(), func(t unit.Type) builder.Cond { return builder.Eq{typeCol: t} })
}

// HiddenUnitTypeCond matches the owners (ownerCol) in whose repositories the site policy hides the unit t. It is
// empty when nothing is hidden.
func HiddenUnitTypeCond(ctx context.Context, ownerCol string, t unit.Type) builder.Cond {
	return hiddenUnitCond(ctx, ownerCol, []unit.Type{t}, func(unit.Type) builder.Cond { return builder.NewCond() })
}

func hiddenUnitCond(ctx context.Context, ownerCol string, types []unit.Type, typeCond func(unit.Type) builder.Cond) builder.Cond {
	p := setting.Config().RepoUnitPolicy.Value(ctx)
	cond := builder.NewCond()
	for _, t := range types {
		key := unit.Units[t].NameKey
		flipped := p.OrgOverrides[key]
		switch {
		case t == unit.TypeCode:
		case p.Invalid || slices.Contains(p.NotAllowed, key):
			c := typeCond(t).And(builder.In(ownerCol, builder.Select("id").From("`user`").Where(builder.Eq{"type": user_model.UserTypeOrganization})))
			if len(flipped) > 0 && !p.Invalid {
				c = c.And(builder.NotIn(ownerCol, flipped))
			}
			cond = cond.Or(c)
		case len(flipped) > 0:
			// only organization IDs are stored, and IDs are never reused
			cond = cond.Or(typeCond(t).And(builder.In(ownerCol, flipped)))
		}
	}
	return cond
}

// AvailableUnitCond is the negation of HiddenUnitCond, or an empty condition when nothing is hidden.
func AvailableUnitCond(ctx context.Context, ownerCol, typeCol string) builder.Cond {
	return notCond(HiddenUnitCond(ctx, ownerCol, typeCol))
}

// AvailableUnitTypeCond is the negation of HiddenUnitTypeCond, or an empty condition when nothing is hidden.
func AvailableUnitTypeCond(ctx context.Context, ownerCol string, t unit.Type) builder.Cond {
	return notCond(HiddenUnitTypeCond(ctx, ownerCol, t))
}

func notCond(c builder.Cond) builder.Cond {
	if c.IsValid() {
		return builder.Not{c}
	}
	return builder.NewCond()
}
