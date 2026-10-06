// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package ssh

import (
	"net"
	"os/exec"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

type badAddr struct{}

func (badAddr) Network() string { return "tcp" }
func (badAddr) String() string  { return "garbage" }

// AC7: the built-in server always sets SSH_CONNECTION from the (PROXY protocol) client address, after the inherited
// environment, so a value inherited from the server's own environment never wins
func TestServEnv(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "10.1.2.3 1 10.0.0.1 22")
	remote := &net.TCPAddr{IP: net.ParseIP("198.51.100.7"), Port: 51234}
	local := &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 22}

	cmd := exec.Command("true")
	cmd.Env = servEnv("git-upload-pack 'user2/repo1.git'", "version=2", remote, local)
	env := cmd.Environ()
	assert.Contains(t, env, "SSH_CONNECTION=198.51.100.7 51234 10.0.0.1 22")
	assert.NotContains(t, env, "SSH_CONNECTION=10.1.2.3 1 10.0.0.1 22")
	assert.Contains(t, env, "SSH_ORIGINAL_COMMAND=git-upload-pack 'user2/repo1.git'")
	assert.Contains(t, env, "GIT_PROTOCOL=version=2")

	cmd.Env = servEnv("", "", badAddr{}, local)
	env = cmd.Environ()
	assert.True(t, slices.Contains(env, "SSH_CONNECTION="), env)
	assert.NotContains(t, env, "SSH_CONNECTION=10.1.2.3 1 10.0.0.1 22")
}
