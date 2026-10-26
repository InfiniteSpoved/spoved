// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requirements returns name -> requirement of an unpinned parse, checking that no row has a version and that every
// row is direct.
func requirements(t *testing.T, name, content string) map[string]string {
	t.Helper()
	deps, err := FormatFor(name).Parse(t.Context(), []byte(content), nil)
	require.NoError(t, err, name)
	out := map[string]string{}
	for _, d := range deps {
		assert.Empty(t, d.Version, d.Name)
		assert.True(t, d.Direct, d.Name)
		out[d.Name] = d.Requirement
	}
	return out
}

func TestUnpinnedFormats(t *testing.T) {
	for name, locks := range map[string][]string{
		"package.json":   {"package-lock.json", "yarn.lock", "pnpm-lock.yaml"},
		"Cargo.toml":     {"Cargo.lock"},
		"Gemfile":        {"Gemfile.lock"},
		"composer.json":  {"composer.lock"},
		"pyproject.toml": {"poetry.lock", "uv.lock"},
	} {
		f := FormatFor(name)
		require.NotNil(t, f, name)
		assert.True(t, f.Unpinned && f.Small, name)
		assert.Equal(t, locks, f.Locks, name)
		for _, l := range locks {
			assert.True(t, IsLockName(l), l)
		}
	}
	assert.False(t, FormatFor("package-lock.json").Unpinned)
	assert.False(t, IsLockName("package.json"))
	assert.False(t, IsLockName("go.sum"))
}

func TestCovered(t *testing.T) {
	tree := map[string]bool{"package-lock.json": true, "apps/web/package.json": true, "ruby/Gemfile.lock": true, "crates/a/Cargo.toml": true}
	has := func(p string) bool { return tree[p] }
	// a root lock covers every package.json below it (a workspace), a Gemfile only the lock next to it
	assert.True(t, FormatFor("package.json").Covered("apps/web/package.json", has))
	assert.True(t, FormatFor("package.json").Covered("package.json", has))
	assert.True(t, FormatFor("Gemfile").Covered("ruby/Gemfile", has))
	assert.False(t, FormatFor("Gemfile").Covered("ruby/sub/Gemfile", has), "no lock above for Gemfile")
	assert.False(t, FormatFor("Cargo.toml").Covered("crates/a/Cargo.toml", has))
	tree["Cargo.lock"] = true
	assert.True(t, FormatFor("Cargo.toml").Covered("crates/a/Cargo.toml", has), "the workspace root's lock")
	assert.False(t, FormatFor("composer.json").Covered("composer.json", has))
	assert.False(t, FormatFor("pyproject.toml").Covered("py/pyproject.toml", func(p string) bool { return p == "uv.lock" }), "no lock above for pyproject.toml")
	assert.True(t, FormatFor("pyproject.toml").Covered("py/pyproject.toml", func(p string) bool { return p == "py/uv.lock" }))
}

func TestFallbackRealFixtures(t *testing.T) {
	for rel, want := range map[string]map[string]string{
		"nodegoat/package.json":   {"express": "^4.13.4", "bcrypt-nodejs": "0.0.3"},
		"koel/composer.json":      {"laravel/framework": "^9.0", "predis/predis": "~1.0"},
		"poetry/pyproject.toml":   {"poetry-core": "1.8.1", "cleo": "^2.1.0"},
		"serde-json/Cargo.toml":   {"itoa": "1.0", "memchr": "2", "serde": "1.0.220", "trybuild": "1.0.108"},
		"sinatra/Gemfile":         {"minitest": "~> 5.0", "rubocop": "~> 1.32.0", "rake": "", "falcon": "~> 0.40"},
		"httpx/pyproject.toml":    {"certifi": "", "httpcore": "==1.*", "rich": ">=10,<15", "h2": ">=3,<5"},
		"mastodon/package.json":   {},
		"defectdojo/package.json": {},
		"vite/package.json":       {},
	} {
		deps := parseFixture(t, rel)
		got := map[string]string{}
		for _, d := range deps {
			assert.Empty(t, d.Version, rel)
			got[d.Name] = d.Requirement
		}
		assert.NotEmpty(t, got, rel)
		for n, r := range want {
			assert.Contains(t, got, n, rel)
			assert.Equal(t, r, got[n], "%s: %s", rel, n)
		}
		switch rel {
		case "poetry/pyproject.toml":
			assert.NotContains(t, got, "python")
		case "sinatra/Gemfile":
			// path gems are the project's own; lines with a modifier are not plain gem lines
			assert.NotContains(t, got, "rack-protection")
			assert.NotContains(t, got, "sinatra-contrib")
			assert.NotContains(t, got, "sass-embedded")
		}
	}
}

func TestPackageJSON(t *testing.T) {
	got := requirements(t, "package.json", `{
		"name": "app", "version": "1.0.0",
		"dependencies": {"express": "^4.18.0", "local": "file:../local", "ws": "workspace:*", "rel": "./x.tgz",
			"alias": "npm:real-name@^2.1.0", "git": "git+https://example.com/x.git", "gh": "user/repo", "odd": 5},
		"devDependencies": {"lodash": "~4.17.20", "@types/node": "*"},
		"optionalDependencies": {"fsevents": "^2"},
		"peerDependencies": {"react": "^18"},
		"scripts": {"dependencies": "x"}
	}`)
	assert.Equal(t, map[string]string{
		"express": "^4.18.0", "real-name": "^2.1.0", "git": "", "gh": "", "odd": "",
		"lodash": "~4.17.20", "@types/node": "*", "fsevents": "^2",
	}, got)
	for _, bad := range []string{`[]`, `{"dependencies": []}`, `{"dependencies": {"a": "1"`, `"x"`, ``} {
		_, err := FormatFor("package.json").Parse(t.Context(), []byte(bad), nil)
		require.ErrorIs(t, err, ErrParse, bad)
	}
}

func TestComposerJSON(t *testing.T) {
	got := requirements(t, "composer.json", `{"require": {"php": ">=8.1", "ext-json": "*", "Monolog/Monolog": "^3.0", "composer-plugin-api": "^2"},
		"require-dev": {"phpunit/phpunit": "^10"}, "autoload": {"psr-4": {"A\\": "src/"}}}`)
	assert.Equal(t, map[string]string{"monolog/monolog": "^3.0", "phpunit/phpunit": "^10"}, got)
	_, err := FormatFor("composer.json").Parse(t.Context(), []byte(`{"require": "x"}`), nil)
	require.ErrorIs(t, err, ErrParse)
}

func TestCargoToml(t *testing.T) {
	got := requirements(t, "Cargo.toml", `# a comment
[package]
name = "app"
description = """
[dependencies]
not-a-dependency = "1"
"""
keywords = ["a", "b",
  "c", # a comment in an array
]

[dependencies]
serde = { version = "1.0", features = ["derive"] }
itoa = "1.0"
"quoted" = '2'
renamed = { package = "real-crate", version = "0.3" }
local = { path = "../local" }
inherited = { workspace = true }
dotted.version = "4"
dotted.optional = true
gitdep = { git = "https://example.com/x" }

[dependencies.tabled]
version = "~5"
default-features = false

[dev-dependencies]
tempfile = "3"

[build-dependencies]
cc = "1.0.83"

[target.'cfg(unix)'.dependencies]
libc = "0.2"

[target."cfg(windows)".dev-dependencies.winapi]
version = "0.3"

[workspace.dependencies]
anyhow = "1"

[features]
default = ["serde"]

[[bin]]
name = "x"
path = "src/x.rs"
`)
	assert.Equal(t, map[string]string{
		"serde": "1.0", "itoa": "1.0", "quoted": "2", "real-crate": "0.3", "inherited": "", "dotted": "4", "gitdep": "",
		"tabled": "~5", "tempfile": "3", "cc": "1.0.83", "libc": "0.2", "winapi": "0.3", "anyhow": "1",
	}, got)

	for name, bad := range map[string]string{
		"dependency twice":            "[dependencies]\na = \"1\"\na = \"2\"\n",
		"table and value":             "[dependencies]\na = \"1\"\n[dependencies.a]\nversion = \"2\"\n",
		"field twice":                 "[dependencies]\na.version = \"1\"\na.version = \"2\"\n",
		"inline table twice":          "[dependencies]\na = {version = \"1\", version = \"2\"}\n",
		"number":                      "[dependencies]\na = 1\n",
		"unterminated string":         "[dependencies]\na = \"1\n",
		"unterminated array":          "[package]\nk = [\"a\",\n",
		"unterminated multi-line":     "[package]\nd = \"\"\"\n[dependencies]\na = \"1\"\n",
		"whole table as a value":      "dependencies = { a = \"1\" }\n",
		"garbage after a value":       "[dependencies]\na = \"1\" b\n",
		"bad header":                  "[dependencies\na = \"1\"\n",
		"lone carriage return":        "[dependencies]\ra = \"1\"\n",
		"deep nesting":                "[package]\nk = " + strings.Repeat("[", 40) + strings.Repeat("]", 40) + "\n",
		"bad escape":                  "[dependencies]\n\"a\\q\" = \"1\"\n",
		"key without a value":         "[dependencies]\na\n",
		"unicode escape out of range": "[dependencies]\n\"\\U00110000\" = \"1\"\n",
	} {
		_, err := FormatFor("Cargo.toml").Parse(t.Context(), []byte(bad), nil)
		require.ErrorIs(t, err, ErrParse, name)
	}
	// CRLF line ends and a byte order mark are TOML
	got = requirements(t, "Cargo.toml", "\xef\xbb\xbf[dependencies]\r\na = \"1\"\r\n\"\\u00e9t\\u00e9\" = \"2\"\r\n")
	assert.Equal(t, map[string]string{"a": "1", "été": "2"}, got)
}

func TestPyprojectToml(t *testing.T) {
	got := requirements(t, "pyproject.toml", `[project]
name = "app"
dependencies = [
  "requests>=2.31,<3",
  "Flask[async] (>=3.0)",
  "tomli; python_version < '3.11'",
  "exact==1.2.3",
  "bare",
  "pkg @ https://example.com/pkg.whl",
  "./local.whl",
]
classifiers = ["Programming Language :: Python"]

[project.optional-dependencies]
dev = ["pytest>=7"]

[dependency-groups]
test = ["coverage", {include-group = "dev"}]

[tool.poetry.dependencies]
python = "^3.11"
httpx = "^0.27"
mylib = { path = "../mylib", develop = true }
numpy = [
  { version = "^1.26", python = ">=3.9" },
  { version = "^1.24", python = "<3.9" },
]
pydantic = { version = "^2", extras = ["email"] }

[tool.poetry.group.lint.dependencies]
ruff = "^0.4"

[tool.poetry.dependencies.rich]
version = "^13"
`)
	assert.Equal(t, map[string]string{
		"requests": ">=2.31,<3", "Flask": ">=3.0", "tomli": "", "exact": "==1.2.3", "bare": "", "pytest": ">=7",
		"coverage": "", "httpx": "^0.27", "numpy": "", "pydantic": "^2", "ruff": "^0.4", "rich": "^13",
	}, got)
	for name, bad := range map[string]string{
		"dependencies not an array": "[project]\ndependencies = \"requests\"\n",
		"poetry number":             "[tool.poetry.dependencies]\nhttpx = 1\n",
		"duplicate":                 "[tool.poetry.dependencies]\nhttpx = \"1\"\nhttpx = \"2\"\n",
	} {
		_, err := FormatFor("pyproject.toml").Parse(t.Context(), []byte(bad), nil)
		require.ErrorIs(t, err, ErrParse, name)
	}
	// a pyproject.toml of a tool's settings only names nothing
	assert.Empty(t, requirements(t, "pyproject.toml", "[tool.black]\nline-length = 100\n"))
}

func TestGemfile(t *testing.T) {
	got := requirements(t, "Gemfile", `source "https://rubygems.org"
ruby "3.3.0"
gemspec

gem "rails", "~> 7.1", ">= 7.1.2"
gem 'pg', '~> 1.1' # the database
  gem "puma", require: false
gem "local", path: "../local"
gem "old", :path => "vendor/old"
gem "gitgem", git: "https://example.com/x.git"
gem "dynamic", ENV.fetch("V", "1")
gem "interpolated", "#{ver}"
gem "escaped\"name"
gem "modifier" if ENV["X"]
gem("parens", "1")
gems "notgem"
gem "bad name"
group :development, :test do
  gem "rspec-rails", "~> 6"
end
# gem "commented", "1"
`)
	assert.Equal(t, map[string]string{
		"rails": "~> 7.1, >= 7.1.2", "pg": "~> 1.1", "puma": "", "gitgem": "", "dynamic": "", "interpolated": "", "rspec-rails": "~> 6",
	}, got)
}

// The fallback parsers stop at the caps, look at the context and refuse what can't be read, like the others.
func TestFallbackLimits(t *testing.T) {
	line := func(format string) func([]byte, int) []byte {
		return func(b []byte, i int) []byte {
			before, after, _ := strings.Cut(format, "%")
			return append(hexID(append(b, before...), i), after...)
		}
	}
	const small = 1 << 20
	for name, c := range map[string]struct {
		file string
		data []byte
	}{
		"package.json":      {"package.json", fill(`{"dependencies":{`, `"z":"1"}}`, small, line(`"%":"^1",`))},
		"composer.json":     {"composer.json", fill(`{"require":{`, `"a/z":"1"}}`, small, line(`"a/%":"^1",`))},
		"Cargo.toml":        {"Cargo.toml", fill("[dependencies]\n", "", small, line("% = \"1\"\n"))},
		"Cargo.toml table":  {"Cargo.toml", fill("", "", small, line("[dependencies.%]\nversion = \"1\"\n"))},
		"pyproject PEP 621": {"pyproject.toml", fill("[project]\ndependencies = [\n", "]\n", small, line("\"%>=1\",\n"))},
		"poetry":            {"pyproject.toml", fill("[tool.poetry.dependencies]\n", "", small, line("% = \"^1\"\n"))},
		"Gemfile":           {"Gemfile", fill("", "", small, line("gem \"%\", \"~> 1\"\n"))},
	} {
		t.Run(name, func(t *testing.T) {
			f := FormatFor(c.file)
			_, err := f.ParseLimited(t.Context(), c.data, nil, 100)
			require.True(t, isLimit(err), "%v", err) // rows, or the entries kept before the rows are made
			deps, err := f.ParseLimited(t.Context(), c.data, nil, 1<<20)
			require.NoError(t, err)
			assert.Greater(t, len(deps), 10000)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, err = f.ParseLimited(ctx, c.data, nil, DefaultMaxDependencies)
			require.ErrorIs(t, err, context.Canceled)
		})
	}
	// many fields of one dependency are entries: the cap of the entries stops them
	_, err := FormatFor("Cargo.toml").ParseLimited(t.Context(), fill("[dependencies.a]\n", "", small, line("% = 1\n")), nil, 100)
	require.ErrorIs(t, err, ErrTooManyEntries)
	// a manifest of many keys that are no dependencies costs nothing kept
	deps, err := FormatFor("pyproject.toml").ParseLimited(t.Context(), fill("[tool.x]\n", "", small, line("% = [1, 2]\n")), nil, 100)
	require.NoError(t, err)
	assert.Empty(t, deps)
}
