// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import (
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const hostileSize = 20 << 20 // the default MAX_MANIFEST_SIZE

// allocated returns the bytes allocated while f ran.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// A hostile lockfile must not cost much more memory than its size: only the keys we read are kept.
func TestHostileLockfilesBoundMemory(t *testing.T) {
	const limit = 6 * hostileSize
	cases := map[string]string{
		// the values under the dependency maps are never used
		"package-lock.json": `{"lockfileVersion":3,"packages":{"":{"dependencies":{"x":[` + strings.Repeat("0,", hostileSize/2) + `0]}}}}`,
		// many lines just below the line limit, each a flow sequence a YAML library would build nodes for
		"pnpm-lock.yaml": "lockfileVersion: '9.0'\n" + strings.Repeat("other: ["+strings.Repeat("0,", 30000)+"0]\n", hostileSize/60000),
		"yarn.lock":      "x@^1:\n  version \"1.0.0\"\n" + strings.Repeat("#"+strings.Repeat("a", 60)+"\n", hostileSize/62),
		"composer.lock":  `{"packages":[{"name":"a/b","version":"1","extra":[` + strings.Repeat("0,", hostileSize/2) + `0]}]}`,
		"packages.lock.json": `{"version":1,"dependencies":{"net8.0":{"A":{"type":"Direct","resolved":"1.0.0","extra":[` +
			strings.Repeat("0,", hostileSize/2) + `0]}}}}`,
		"poetry.lock": "[[package]]\nname = \"a\"\nversion = \"1\"\n" + strings.Repeat("# "+strings.Repeat("a", 60)+"\n", hostileSize/62),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			require.GreaterOrEqual(t, len(content), hostileSize-(256<<10))
			data := []byte(content)
			content = ""
			var err error
			n := allocated(func() { _, err = FormatFor(name).Parse(t.Context(), data, nil) })
			t.Logf("%s: %d MiB allocated for %d MiB", name, n>>20, len(data)>>20)
			assert.LessOrEqual(t, n, uint64(limit), "allocations of a %d MiB file", len(data)>>20)
			_ = err // a file that isn't valid may be refused; the cost is what is measured
		})
	}
}

// A lockfile of a real size still gives all its packages.
func TestLargeLockfilesStillParse(t *testing.T) {
	var b strings.Builder
	b.WriteString("lockfileVersion: '9.0'\n\nimporters:\n\n  .:\n    dependencies:\n      pkg0:\n        specifier: ^1.0.0\n        version: 1.0.0\n\npackages:\n\n")
	for i := range 20000 {
		b.WriteString("  'pkg" + itoa(i) + "@1.0.0':\n    resolution: {integrity: sha512-abc}\n    engines: {node: '>=12'}\n\n")
	}
	deps, err := FormatFor("pnpm-lock.yaml").Parse(t.Context(), []byte(b.String()), nil)
	require.NoError(t, err)
	assert.Len(t, deps, 20000)
	assert.True(t, deps[0].Direct || deps[len(deps)-1].Direct)
}

func itoa(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var s []byte
	for ; i > 0; i /= 10 {
		s = append([]byte{digits[i%10]}, s...)
	}
	return string(s)
}
