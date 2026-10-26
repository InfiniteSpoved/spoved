// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import "context"

// Parse is ParseLimited with DefaultMaxDependencies.
func (f *Format) Parse(ctx context.Context, data []byte, find Finder) ([]Dependency, error) {
	return f.ParseLimited(ctx, data, find, DefaultMaxDependencies)
}
