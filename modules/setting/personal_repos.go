// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package setting

import (
	"fmt"
	"strings"
)

// The personal repository policies: which repositories individual users may have in their own namespace.
const (
	PersonalReposAllowed     = "allowed"
	PersonalReposPrivateOnly = "private_only"
	PersonalReposDisabled    = "disabled"
)

// PersonalReposState is stored as JSON in the "repository.personal_repos" system setting. An empty Mode means the
// app.ini default ([repository] PERSONAL_REPOS).
type PersonalReposState struct {
	Mode        string `json:"mode"`
	UpdatedBy   int64  `json:"updated_by"`
	UpdatedUnix int64  `json:"updated_unix"`
}

// EffectiveMode is the policy in force: the stored mode, else the app.ini default. A stored value that can't be
// parsed or isn't a known mode reads as disabled, so a broken setting fails closed.
func (s PersonalReposState) EffectiveMode() string {
	switch s.Mode {
	case "":
		return Repository.PersonalRepos
	case PersonalReposAllowed, PersonalReposPrivateOnly, PersonalReposDisabled:
		return s.Mode
	}
	return PersonalReposDisabled
}

// parsePersonalRepos reads [repository] PERSONAL_REPOS; only an empty value means allowed.
func parsePersonalRepos(v string) (string, error) {
	switch v = strings.ToLower(strings.TrimSpace(v)); v {
	case "":
		return PersonalReposAllowed, nil
	case PersonalReposAllowed, PersonalReposPrivateOnly, PersonalReposDisabled:
		return v, nil
	}
	return "", fmt.Errorf("%q is not one of allowed, private_only, disabled", v)
}
