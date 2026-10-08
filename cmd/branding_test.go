// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/urfave/cli/v3"
)

// The CLI help says CodeHat; only the upstream feature names stay (see docs/branding.md).
func TestHelpBranding(t *testing.T) {
	allowed := strings.NewReplacer("Forgejo Actions", "", "Forgejo Runner", "")
	var walk func(c *cli.Command)
	walk = func(c *cli.Command) {
		texts := []string{c.Usage, c.Description}
		for _, f := range c.Flags {
			if u, ok := f.(cli.DocGenerationFlag); ok {
				texts = append(texts, u.GetUsage())
			}
		}
		for _, text := range texts {
			assert.NotContains(t, allowed.Replace(text), "Forgejo", c.Name)
		}
		for _, sub := range c.Commands {
			if !strings.HasPrefix(sub.Name, "forgejo") { // the `forgejo`/`forgejo-cli` subcommand trees (cmd/forgejo) keep upstream help
				walk(sub)
			}
		}
	}
	walk(NewMainApp("1.0", ""))
}
