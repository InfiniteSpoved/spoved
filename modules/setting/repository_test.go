// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package setting

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestSSHInstanceKey(t *testing.T) {
	sshSigningKeyPath, err := filepath.Abs("../../tests/integration/ssh-signing-key.pub")
	require.NoError(t, err)

	t.Run("None value", func(t *testing.T) {
		cfg, err := NewConfigProviderFromData(`
[repository.signing]
FORMAT = ssh
SIGNING_KEY = none
`)
		require.NoError(t, err)

		loadRepositoryFrom(cfg)

		assert.Nil(t, SSHInstanceKey)
	})

	t.Run("No value", func(t *testing.T) {
		cfg, err := NewConfigProviderFromData(`
[repository.signing]
FORMAT = ssh
`)
		require.NoError(t, err)

		loadRepositoryFrom(cfg)

		assert.Nil(t, SSHInstanceKey)
	})
	t.Run("Normal", func(t *testing.T) {
		iniStr := fmt.Sprintf(`
[repository.signing]
FORMAT = ssh
SIGNING_KEY = %s
`, sshSigningKeyPath)
		cfg, err := NewConfigProviderFromData(iniStr)
		require.NoError(t, err)

		loadRepositoryFrom(cfg)

		assert.NotNil(t, SSHInstanceKey)
		assert.Equal(t, "ssh-ed25519", SSHInstanceKey.Type())
		assert.EqualValues(t, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFeRC8GfFyXtiy0f1E7hLv77BXW7e68tFvIcs8/29YqH\n", ssh.MarshalAuthorizedKey(SSHInstanceKey))
	})
}

func TestParseOrgPrivateForkPolicy(t *testing.T) {
	for in, want := range map[string]int{"": 0, "all": 0, " ALL ": 0, "org": 1, "Org": 1, "none": 2, " none": 2} {
		got, err := parseOrgPrivateForkPolicy(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{"deny", "0", "1", "all org", "nobody"} {
		_, err := parseOrgPrivateForkPolicy(in)
		require.Error(t, err, in)
	}
}

func TestOrgPolicyFloorSettings(t *testing.T) {
	t.Cleanup(func() { Repository.OrgRestrictVisibilityChange, Repository.OrgPrivateForkPolicy = false, 0 })
	cfg, err := NewConfigProviderFromData("[repository]\nORG_RESTRICT_VISIBILITY_CHANGE = true\nORG_PRIVATE_FORK_POLICY = org\n")
	require.NoError(t, err)
	loadRepositoryFrom(cfg)
	assert.True(t, Repository.OrgRestrictVisibilityChange)
	assert.Equal(t, 1, Repository.OrgPrivateForkPolicy)

	// the bool accepts what MapTo accepts, and rejects a typo
	for v, ok := range map[string]bool{"yes": true, "On": true, "1": true, "ture": false} {
		cfg, err = NewConfigProviderFromData("[repository]\nORG_RESTRICT_VISIBILITY_CHANGE = " + v + "\n")
		require.NoError(t, err)
		_, err = cfg.Section("repository").Key("ORG_RESTRICT_VISIBILITY_CHANGE").Bool()
		assert.Equal(t, ok, err == nil, v)
	}
}
