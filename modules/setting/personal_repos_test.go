// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package setting

import (
	"testing"

	"forgejo.org/modules/test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePersonalRepos(t *testing.T) {
	for in, want := range map[string]string{"": "allowed", "allowed": "allowed", "Private_Only": "private_only", "disabled": "disabled"} {
		got, err := parsePersonalRepos(in)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
	_, err := parsePersonalRepos("private")
	require.Error(t, err)
}

func TestPersonalReposEffectiveMode(t *testing.T) {
	defer test.MockVariableValue(&Repository.PersonalRepos, PersonalReposPrivateOnly)()
	assert.Equal(t, PersonalReposPrivateOnly, PersonalReposState{}.EffectiveMode())
	assert.Equal(t, PersonalReposAllowed, PersonalReposState{Mode: "allowed"}.EffectiveMode())
	assert.Equal(t, PersonalReposDisabled, PersonalReposState{Mode: "invalid"}.EffectiveMode())
}
