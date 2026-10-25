// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package dependency

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseFixture parses testdata/<rel>, giving the parser the files next to it and above it like the scanner does.
func parseFixture(t *testing.T, rel string) []Dependency {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", rel))
	require.NoError(t, err)
	f := FormatFor(path.Base(rel))
	require.NotNil(t, f, rel)
	find := func(name string, up bool) []byte {
		for dir := path.Dir(rel); ; dir = path.Dir(dir) {
			if b, err := os.ReadFile(filepath.Join("testdata", dir, name)); err == nil {
				return b
			}
			if !up || dir == "." {
				return nil
			}
		}
	}
	deps, err := f.Parse(t.Context(), data, find)
	require.NoError(t, err, rel)
	return deps
}

func index(deps []Dependency) map[string]Dependency {
	m := map[string]Dependency{}
	for _, d := range deps {
		m[d.Name+"@"+d.Version] = d
	}
	return m
}

func TestRealFixtures(t *testing.T) {
	for rel, min := range map[string]int{
		"gin/go.mod":                  10,
		"nodegoat/package-lock.json":  50,
		"defectdojo/requirements.txt": 50,
		"defectdojo/yarn.lock":        20,
		"mastodon/yarn.lock":          40,
		"koel/yarn.lock":              40,
		"hyper-app/pnpm-lock.yaml":    50,
		"vite/pnpm-lock.yaml":         50,
		"mastodon/Gemfile.lock":       150,
		"koel/composer.lock":          30,
		"poetry/poetry.lock":          30,
		"ripgrep/Cargo.lock":          30,
		"webgoat/pom.xml":             10,
		"eshop/src/Web/Web.csproj":    10,
	} {
		deps := parseFixture(t, rel)
		assert.GreaterOrEqual(t, len(deps), min, rel)
		for _, d := range deps {
			assert.NotEmpty(t, d.Name, rel)
		}
	}
}

func TestGoMod(t *testing.T) {
	m := index(parseFixture(t, "gin/go.mod"))
	assert.True(t, m["github.com/bytedance/sonic@v1.9.1"].Direct)
	var direct, indirect int
	for _, d := range m {
		if d.Direct {
			direct++
		} else {
			indirect++
		}
	}
	assert.Positive(t, direct)
	assert.Positive(t, indirect)
}

func TestNPM(t *testing.T) {
	m := index(parseFixture(t, "npm-v3/package-lock.json"))
	assert.Equal(t, Dependency{Ecosystem: NPM, Name: "@scope/pkg", Version: "1.2.0", License: "MIT", Direct: true}, m["@scope/pkg@1.2.0"])
	assert.False(t, m["left-pad@1.0.0"].Direct, "nested copy")
	assert.True(t, m["left-pad@1.3.0"].Direct)
	assert.True(t, m["jest@29.7.0"].Direct)
	assert.Equal(t, "ISC", m["real-name@2.0.0"].License)
	assert.NotContains(t, m, "app@")
	assert.Len(t, m, 5, "workspace link and workspace member are no packages")

	m = index(parseFixture(t, "npm-v1/package-lock.json"))
	assert.True(t, m["express@4.18.2"].Direct)
	assert.True(t, m["mime-types@2.1.35"].Direct)
	assert.False(t, m["accepts@1.3.8"].Direct)
	assert.False(t, m["debug@2.6.9"].Direct)

	m = index(parseFixture(t, "nodegoat/package-lock.json"))
	assert.True(t, m["body-parser@1.18.3"].Direct)
}

func TestYarn(t *testing.T) {
	m := index(parseFixture(t, "yarn-berry/yarn.lock"))
	assert.Len(t, m, 3, "the workspace is not a package")
	assert.True(t, m["lodash@4.17.21"].Direct)
	assert.False(t, m["@babel/code-frame@7.22.13"].Direct)
	assert.Contains(t, m, "resolve@1.22.8")

	m = index(parseFixture(t, "koel/yarn.lock"))
	assert.Contains(t, m, "@ampproject/remapping@2.2.0")
}

func TestYarnAliasesAndLocal(t *testing.T) {
	pkgJSON := []byte(`{"dependencies":{"debug":"^4.3.4","str":"npm:string-width@^4.2.0","local":"link:../local"}}`)
	find := func(n string, _ bool) []byte {
		if n == "package.json" {
			return pkgJSON
		}
		return nil
	}
	parse := func(lock string) map[string]Dependency {
		deps, err := FormatFor("yarn.lock").Parse(t.Context(), []byte(lock), find)
		require.NoError(t, err)
		return index(deps)
	}
	t.Run("classic", func(t *testing.T) {
		m := parse(`# yarn lockfile v1


debug@^2.6.9:
  version "2.6.9"

debug@^4.3.4:
  version "4.3.4"

"string-width-cjs@npm:string-width@^4.2.0", "string-width@^4.1.0", str@npm:string-width@^4.2.0:
  version "4.2.3"

local@link:../local:
  version "1.0.0"
`)
		assert.Len(t, m, 3)
		assert.True(t, m["debug@4.3.4"].Direct)
		assert.False(t, m["debug@2.6.9"].Direct, "the other version is transitive")
		require.Contains(t, m, "string-width@4.2.3", "the alias entry is the real package")
		assert.True(t, m["string-width@4.2.3"].Direct)
		assert.NotContains(t, m, "string-width-cjs@4.2.3")
	})
	t.Run("berry", func(t *testing.T) {
		m := parse(`__metadata:
  version: 8

"debug@npm:^4.3.4":
  version: 4.3.4
  languageName: node

"string-width-cjs@npm:string-width@^4.2.0":
  version: 4.2.3
  languageName: node

"local@link:../local::locator=app%40workspace%3A.":
  version: 0.0.0-use.local

"portal-pkg@portal:../p::locator=app%40workspace%3A.":
  version: 0.0.0-use.local

"file-pkg@file:../f#../f::hash=abc&locator=app%40workspace%3A.":
  version: 1.0.0

"app@workspace:.":
  version: 0.0.0-use.local
`)
		assert.Len(t, m, 2)
		assert.True(t, m["debug@4.3.4"].Direct)
		assert.Contains(t, m, "string-width@4.2.3")
	})
}

func TestPnpm(t *testing.T) {
	m := index(parseFixture(t, "pnpm-v5/pnpm-lock.yaml"))
	assert.Len(t, m, 3)
	assert.True(t, m["lodash@4.17.21"].Direct)
	assert.True(t, m["@types/node@18.11.9"].Direct)
	assert.False(t, m["react-dom@18.2.0"].Direct)

	deps := parseFixture(t, "hyper-app/pnpm-lock.yaml")
	var direct bool
	for _, d := range deps {
		direct = direct || d.Direct
		assert.NotContains(t, d.Version, "(")
	}
	assert.True(t, direct)
}

func TestPnpmV6(t *testing.T) {
	m := index(parseFixture(t, "pnpm-v6/pnpm-lock.yaml"))
	require.Contains(t, m, "react@18.2.0")
	assert.True(t, m["react@18.2.0"].Direct)
	assert.False(t, m["react@17.0.2"].Direct, "another version of a direct package is transitive")
	assert.True(t, m["react-dom@18.2.0"].Direct, "the peer suffix is not part of the version")
	assert.True(t, m["@types/node@20.8.7"].Direct)
	assert.False(t, m["loose-envify@1.4.0"].Direct)
	assert.True(t, m["string-width@4.2.3"].Direct, "an alias is the real package")
	for k := range m {
		assert.NotContains(t, k, "localpkg", "a package on the disk is not listed")
		assert.NotContains(t, k, "(")
	}
	assert.Len(t, m, 8)
}

func TestPnpmMultiDocument(t *testing.T) {
	deps := parseFixture(t, "vite/pnpm-lock.yaml")
	m := index(deps)
	assert.GreaterOrEqual(t, len(deps), 60, "the project's document is read")
	for k := range m {
		assert.NotContains(t, k, "@pnpm/exe", "the tooling document is left out: %s", k)
	}
	var direct int
	for _, d := range deps {
		if d.Direct {
			direct++
		}
	}
	assert.Positive(t, direct)

	// without importers in a later document, every document counts
	two := "lockfileVersion: '9.0'\npackages:\n  a@1.0.0:\n    resolution: {}\n---\nlockfileVersion: '9.0'\npackages:\n  b@2.0.0:\n    resolution: {}\n"
	got, err := FormatFor("pnpm-lock.yaml").Parse(t.Context(), []byte(two), nil)
	require.NoError(t, err)
	assert.Len(t, got, 2)
	got, err = FormatFor("pnpm-lock.yaml").Parse(t.Context(), []byte("a: [\n---\nb: 1"), nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestFormatForPath(t *testing.T) {
	for p, want := range map[string]bool{
		"requirements.txt": true, "a/requirements-dev.txt": true, "dev-requirements.txt": true, "requirements_test.txt": true,
		"requirements/base.txt": true, "x/requirements/prod.txt": true,
		"requirements/base.md": false, "requirements/sub/base.txt": false, "notes.txt": false, "requirements.md": false,
	} {
		assert.Equal(t, want, FormatForPath(p) != nil, p)
	}
}

func TestPython(t *testing.T) {
	m := index(parseFixture(t, "defectdojo/requirements.txt"))
	for _, d := range m {
		assert.True(t, d.Direct)
	}
	f := FormatFor("requirements.txt")
	deps, err := f.Parse(t.Context(), []byte("# c\nDjango==4.2.1  # why\nrequests[security]==2.31.0 ; python_version>'3'\nflask>=2\n-r other.txt\n--index-url http://169.254.169.254\nfoo==1.*\ngit+https://x/y.git#egg=y\n"), nil)
	require.NoError(t, err)
	assert.Equal(t, []Dependency{
		{Ecosystem: PyPI, Name: "Django", Version: "4.2.1", Direct: true},
		{Ecosystem: PyPI, Name: "requests", Version: "2.31.0", Direct: true},
		{Ecosystem: PyPI, Name: "flask", Direct: true},
		{Ecosystem: PyPI, Name: "foo", Direct: true},
	}, deps, "a range or a wildcard keeps the name with no version; URLs and options are dropped")

	deps, err = f.Parse(t.Context(), []byte("flask>=2.0\nrequests\nDjango==4.2.1 --hash=sha256:abc\nnumpy==1.26.0 \\\n    --hash=sha256:def\npkg @ https://example.test/p.whl\n-c constraints.txt\n"), nil)
	require.NoError(t, err)
	assert.Equal(t, []Dependency{
		{Ecosystem: PyPI, Name: "flask", Direct: true},
		{Ecosystem: PyPI, Name: "requests", Direct: true},
		{Ecosystem: PyPI, Name: "Django", Version: "4.2.1", Direct: true},
		{Ecosystem: PyPI, Name: "numpy", Version: "1.26.0", Direct: true},
	}, deps)

	p := index(parseFixture(t, "poetry/poetry.lock"))
	var direct int
	for _, d := range p {
		if d.Direct {
			direct++
		}
	}
	assert.Positive(t, direct)
}

func TestMaven(t *testing.T) {
	m := index(parseFixture(t, "pom-props/pom.xml"))
	assert.Contains(t, m, "junit:junit@4.13.2")
	assert.Contains(t, m, "com.fasterxml.jackson.core:jackson-core@2.15.0")
	assert.Contains(t, m, "org.example:sibling@3.1.0")
	assert.Contains(t, m, "org.slf4j:slf4j-api@2.0.9", "version from the same file's dependencyManagement")
	assert.Contains(t, m, "org.unknown:bom-managed@")
	assert.Contains(t, m, "org.unknown:unresolved@")
	assert.Len(t, m, 6)
	assert.True(t, m["junit:junit@4.13.2"].Direct, "test scope is included")
}

func TestNuGet(t *testing.T) {
	deps := parseFixture(t, "eshop/src/Web/Web.csproj")
	m := index(deps)
	assert.Contains(t, m, "Ardalis.ListStartupServices@1.1.4", "central package management")
	for _, d := range deps {
		assert.NotEmpty(t, d.Version, d.Name)
		assert.True(t, d.Direct)
	}

	m = index(parseFixture(t, "nuget/packages.lock.json"))
	assert.Len(t, m, 3)
	assert.True(t, m["Newtonsoft.Json@13.0.1"].Direct)
	assert.False(t, m["Microsoft.Extensions.Logging@6.0.0"].Direct)
	assert.Contains(t, m, "Microsoft.Extensions.Logging@8.0.0")

	f := FormatFor("a.csproj")
	deps, err := f.Parse(t.Context(), []byte(`<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><V>2.0.0</V></PropertyGroup><ItemGroup>
<PackageReference Include="A" Version="1.0.0" VersionOverride="3.0.0"/>
<PackageReference Include="B"><Version>$(V)</Version></PackageReference>
<PackageReference Include="C" Version="$(Nope)"/>
</ItemGroup></Project>`), nil)
	require.NoError(t, err)
	assert.Equal(t, []Dependency{
		{Ecosystem: NuGet, Name: "A", Version: "3.0.0", Direct: true},
		{Ecosystem: NuGet, Name: "B", Version: "2.0.0", Direct: true},
		{Ecosystem: NuGet, Name: "C", Direct: true},
	}, deps)
	assert.Nil(t, FormatFor(".csproj"))
}

func TestCargoGemComposer(t *testing.T) {
	m := index(parseFixture(t, "ripgrep/Cargo.lock"))
	var direct, other int
	for _, d := range m {
		assert.NotEqual(t, "ripgrep", d.Name, "the project itself is no dependency")
		if d.Direct {
			direct++
		} else {
			other++
		}
	}
	assert.Positive(t, direct)
	assert.Positive(t, other)

	var gd int
	for _, d := range parseFixture(t, "mastodon/Gemfile.lock") {
		if d.Direct {
			gd++
		}
		assert.NotEmpty(t, d.Version)
	}
	assert.Positive(t, gd)

	var licensed, cd int
	for _, d := range parseFixture(t, "koel/composer.lock") {
		if d.License != "" {
			licensed++
		}
		if d.Direct {
			cd++
		}
	}
	assert.Positive(t, licensed)
	assert.Positive(t, cd)
}

func TestPURL(t *testing.T) {
	for _, tc := range []struct{ eco, name, version, want string }{
		{Go, "github.com/Gin-Gonic/gin", "v1.9.1", "pkg:golang/github.com/gin-gonic/gin@v1.9.1"},
		{Go, "golang.org/x/net", "v0.0.0-2021+incompatible", "pkg:golang/golang.org/x/net@v0.0.0-2021%2Bincompatible"},
		{NPM, "@scope/name", "1.0.0", "pkg:npm/%40scope/name@1.0.0"},
		{NPM, "left-pad", "", "pkg:npm/left-pad"},
		{PyPI, "Django_Rest.Framework", "3.0", "pkg:pypi/django-rest.framework@3.0"},
		{Maven, "org.apache:commons-lang3", "3.12.0", "pkg:maven/org.apache/commons-lang3@3.12.0"},
		{NuGet, "Newtonsoft.Json", "13.0.1", "pkg:nuget/Newtonsoft.Json@13.0.1"},
		{Cargo, "serde", "1.0.0", "pkg:cargo/serde@1.0.0"},
		{RubyGems, "rails", "7.0.0", "pkg:gem/rails@7.0.0"},
		{Packagist, "Laravel/Framework", "v10.0.0", "pkg:composer/laravel/framework@v10.0.0"},
		{NPM, "a b/<x>", "1 2", "pkg:npm/a%20b/%3Cx%3E@1%202"},
		{"Nope", "x", "1", ""},
	} {
		assert.Equal(t, tc.want, PURL(tc.eco, tc.name, tc.version), tc.name)
	}
}

func TestValidate(t *testing.T) {
	long := strings.Repeat("a", 1<<20)
	in := []Dependency{
		{Name: "evil\x00name\r\n\u202Etxt", Version: "1\x1b[31m.0"},
		{Name: long, Version: "1"},
		{Name: "n", Version: long},
		{Name: "", Version: "1"},
		{Name: "../../x<img onerror=1>", Version: "<script>"},
		{Name: "bad\xffutf", Version: "1"},
		{Name: "dup", Version: "1"},
		{Name: "dup", Version: "1", Direct: true, License: "MIT"},
		{Name: "lic", Version: "1", License: long[:MaxLicenseLen+1]},
	}
	out := validate(Go, in)
	require.Len(t, out, 5)
	assert.Equal(t, "evilnametxt", out[0].Name)
	assert.Equal(t, "1[31m.0", out[0].Version)
	assert.Equal(t, "../../x<img onerror=1>", out[1].Name, "markup is kept; templates and JSON escape it")
	assert.Equal(t, "bad�utf", out[2].Name)
	assert.Equal(t, Dependency{Ecosystem: Go, Name: "dup", Version: "1", Direct: true, License: "MIT"}, out[3])
	assert.Empty(t, out[4].License)
	assert.NotContains(t, PURL(Go, out[1].Name, out[1].Version), "<")
}

func TestHostile(t *testing.T) {
	t.Run("requirements follow nothing", func(t *testing.T) {
		deps, err := FormatFor("requirements.txt").Parse(t.Context(), []byte("-r ../../../../etc/passwd\n--index-url http://169.254.169.254\n-e .\n"), nil)
		require.NoError(t, err)
		assert.Empty(t, deps)
	})
	t.Run("xml entities", func(t *testing.T) {
		xxe := `<?xml version="1.0"?><!DOCTYPE foo [<!ENTITY x SYSTEM "file:///etc/passwd">]><project><artifactId>&x;</artifactId></project>`
		laughs := `<?xml version="1.0"?><!DOCTYPE lolz [<!ENTITY lol "lol"><!ENTITY lol2 "&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;">]><project><artifactId>&lol2;</artifactId></project>`
		for _, f := range []string{"pom.xml", "a.csproj"} {
			for _, x := range []string{xxe, laughs} {
				_, err := FormatFor(f).Parse(t.Context(), []byte(x), nil)
				require.ErrorIs(t, err, ErrParse, f)
			}
		}
	})
	t.Run("xml depth", func(t *testing.T) {
		start := time.Now()
		_, err := FormatFor("pom.xml").Parse(t.Context(), []byte(strings.Repeat("<a>", 100000)), nil)
		require.ErrorIs(t, err, ErrParse)
		assert.Less(t, time.Since(start), 5*time.Second)
	})
	t.Run("xml charset", func(t *testing.T) {
		_, err := FormatFor("pom.xml").Parse(t.Context(), []byte(`<?xml version="1.0" encoding="UTF-7"?><project/>`), nil)
		require.ErrorIs(t, err, ErrParse)
		m := parseText(t, "pom.xml", "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><project><dependencies><dependency><groupId>org.x</groupId><artifactId>caf\xe9</artifactId><version>1</version></dependency></dependencies></project>", nil)
		assert.Contains(t, m, "org.x:café@1", "latin-1 is read")
	})
	t.Run("maven parent in the same repository", func(t *testing.T) {
		files := map[string]string{
			"../pom.xml":    `<project><version>2.0</version><properties><lib.version>1.5</lib.version></properties><dependencyManagement><dependencies><dependency><groupId>org.x</groupId><artifactId>managed</artifactId><version>${lib.version}</version></dependency></dependencies></dependencyManagement></project>`,
			"../../pom.xml": `<project><properties><lib.version>9</lib.version></properties></project>`,
		}
		find := func(n string, _ bool) []byte {
			if c, ok := files[n]; ok {
				return []byte(c)
			}
			return nil
		}
		child := `<project><parent><groupId>org.x</groupId><artifactId>parent</artifactId><version>2.0</version></parent><dependencies>
<dependency><groupId>org.x</groupId><artifactId>managed</artifactId></dependency>
<dependency><groupId>org.x</groupId><artifactId>prop</artifactId><version>${lib.version}</version></dependency>
</dependencies></project>`
		m := parseText(t, "pom.xml", child, find)
		assert.Contains(t, m, "org.x:managed@1.5")
		assert.Contains(t, m, "org.x:prop@1.5")
		// a relativePath names another parent; without a parent the versions stay empty
		m = parseText(t, "pom.xml", strings.Replace(child, "</parent>", "<relativePath>../../pom.xml</relativePath></parent>", 1), find)
		assert.Contains(t, m, "org.x:prop@9")
		m = parseText(t, "pom.xml", child, nil)
		assert.Contains(t, m, "org.x:managed@")
		assert.Contains(t, m, "org.x:prop@")
	})
	t.Run("yaml alias bomb", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("lockfileVersion: '9.0'\nl0: &a0 [a, a, a, a, a, a, a, a, a]\n")
		for i := 1; i < 12; i++ {
			prev := "*a" + strconv.Itoa(i-1)
			fmt.Fprintf(&b, "l%d: &a%d [%s]\n", i, i, strings.TrimSuffix(strings.Repeat(prev+",", 9), ","))
		}
		start := time.Now()
		_, _ = FormatFor("pnpm-lock.yaml").Parse(t.Context(), []byte(b.String()), nil) // an error is expected; it must end quickly
		assert.Less(t, time.Since(start), time.Second)
	})
	t.Run("broken input is a parse error, never a panic", func(t *testing.T) {
		inputs := []string{"", "\x00\xff\xfe", "{", "[[[[", strings.Repeat(`{"a":`, 20000), strings.Repeat("x", 100000), "[[package]]\nname", "module", "\"@\":\n  version"}
		for _, name := range []string{"go.mod", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "requirements.txt", "poetry.lock", "pom.xml", "a.csproj", "packages.lock.json", "Cargo.lock", "Gemfile.lock", "composer.lock"} {
			for _, in := range inputs {
				_, _ = FormatFor(name).Parse(t.Context(), []byte(in), nil)
			}
		}
	})
	t.Run("a parser panic is a parse error", func(t *testing.T) {
		f := &Format{Ecosystem: Go, parse: func(context.Context, []byte, Finder) ([]Dependency, error) { panic("boom") }}
		_, err := f.Parse(t.Context(), nil, nil)
		require.ErrorIs(t, err, ErrParse)
	})
	t.Run("long line", func(t *testing.T) {
		_, err := FormatFor("Cargo.lock").Parse(t.Context(), []byte(strings.Repeat("x", maxLineLen+10)), nil)
		require.ErrorIs(t, err, ErrParse)
	})
	t.Run("json nesting", func(t *testing.T) {
		_, err := FormatFor("composer.lock").Parse(t.Context(), []byte(strings.Repeat("[", 100000)), nil)
		require.ErrorIs(t, err, ErrParse)
	})
}

func TestFormatFor(t *testing.T) {
	for _, n := range []string{"go.mod", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "requirements.txt", "poetry.lock", "pom.xml", "x.csproj", "packages.lock.json", "Cargo.lock", "Gemfile.lock", "composer.lock"} {
		assert.NotNil(t, FormatFor(n), n)
	}
	for _, n := range []string{"go.sum", "package.json", "Go.mod", "requirements.md", "x.csproj.user", "composer.json", ""} {
		assert.Nil(t, FormatFor(n), n)
	}
}

// FuzzParse runs every parser on the fixtures (the seed corpus) in a normal test run, and on mutations with -fuzz.
func FuzzParse(f *testing.F) {
	files, _ := filepath.Glob("testdata/*/*")
	files2, _ := filepath.Glob("testdata/*/*/*/*")
	for _, p := range append(files, files2...) {
		if b, err := os.ReadFile(p); err == nil && len(b) < 64<<10 {
			f.Add(path.Base(p), b)
		}
	}
	f.Fuzz(func(t *testing.T, name string, data []byte) {
		if format := FormatFor(name); format != nil {
			deps, err := format.Parse(t.Context(), data, nil)
			if err != nil {
				require.ErrorIs(t, err, ErrParse)
			}
			for _, d := range deps {
				require.NotEmpty(t, d.Name)
				require.LessOrEqual(t, len(d.Name), MaxNameLen)
				require.NotContains(t, d.Name+d.Version+d.License, "\x00")
			}
		}
	})
}

func parseText(t *testing.T, name, content string, find Finder) map[string]Dependency {
	t.Helper()
	deps, err := FormatFor(name).Parse(t.Context(), []byte(content), find)
	require.NoError(t, err)
	return index(deps)
}

func TestGoModBadLine(t *testing.T) {
	m := parseText(t, "go.mod", "module m\n\nrequire (\n\texample.com/a v1.0.0\n\texample.com/bad v9.9.9\n\texample.com/c v1.2.0 // indirect\n)\n\nrequire example.com/d v1.0.0\n", nil)
	assert.Len(t, m, 3)
	assert.Contains(t, m, "example.com/a@v1.0.0")
	assert.Contains(t, m, "example.com/c@v1.2.0")
	assert.Contains(t, m, "example.com/d@v1.0.0")
	_, err := FormatFor("go.mod").Parse(t.Context(), []byte("not a go mod ((("), nil)
	require.Error(t, err)
}

func TestLocalPackagesAndPlatforms(t *testing.T) {
	t.Run("go replace by a directory", func(t *testing.T) {
		m := parseText(t, "go.mod", "module m\n\nrequire (\n\texample.com/a v1.0.0\n\texample.com/b v1.0.0\n\texample.com/c v1.0.0\n)\n\nreplace example.com/a => ../a\n\nreplace example.com/b v1.0.0 => ./b\n\nreplace example.com/c => example.com/fork v1.2.3\n", nil)
		assert.Len(t, m, 1)
		assert.Contains(t, m, "example.com/c@v1.0.0")
	})
	t.Run("poetry directory and file sources", func(t *testing.T) {
		m := parseText(t, "poetry.lock", `[[package]]
name = "registry"
version = "1.0.0"

[[package]]
name = "localdir"
version = "0.1.0"

[package.source]
type = "directory"
url = "../localdir"

[[package]]
name = "localfile"
version = "0.2.0"

[package.source]
type = "file"
url = "dist/x.whl"

[[package]]
name = "fromgit"
version = "0.3.0"

[package.source]
type = "git"
url = "https://example.test/x.git"
`, nil)
		assert.Len(t, m, 2)
		assert.Contains(t, m, "registry@1.0.0")
		assert.Contains(t, m, "fromgit@0.3.0")
	})
	t.Run("pyproject include-group is not a package", func(t *testing.T) {
		m := pyprojectDeps(t.Context(), []byte("[dependency-groups]\ndev = [\"pytest>=8\", {include-group = \"test\"}]\n"))
		assert.True(t, m["pytest"])
		assert.False(t, m["test"])
	})
	t.Run("composer path repository", func(t *testing.T) {
		m := parseText(t, "composer.lock", `{"packages":[{"name":"a/b","version":"1.0.0","dist":{"type":"zip"}},{"name":"a/local","version":"dev-main","dist":{"type":"path"}}]}`, nil)
		assert.Len(t, m, 1)
		assert.Contains(t, m, "a/b@1.0.0")
	})
	t.Run("gem platform suffix", func(t *testing.T) {
		m := parseText(t, "Gemfile.lock", "GEM\n  remote: https://rubygems.org/\n  specs:\n    nokogiri (1.15.4-x86_64-linux)\n    nokogiri (1.15.4-arm64-darwin)\n    nokogiri (1.15.4)\n    rack (3.0.0)\n\nDEPENDENCIES\n  nokogiri\n", nil)
		assert.Len(t, m, 2)
		assert.True(t, m["nokogiri@1.15.4"].Direct)
	})
	t.Run("npm v3 aliases and workspace members", func(t *testing.T) {
		m := parseText(t, "package-lock.json", `{"lockfileVersion":3,"packages":{
"":{"workspaces":["packages/*"],"dependencies":{"str":"npm:string-width@4"}},
"node_modules/str":{"name":"string-width","version":"4.2.3"},
"packages/a":{"dependencies":{"chalk":"^5"}},
"node_modules/chalk":{"version":"5.0.0"},
"node_modules/other":{"version":"1.0.0"}}}`, nil)
		assert.True(t, m["string-width@4.2.3"].Direct, "an alias is direct under its installed name")
		assert.True(t, m["chalk@5.0.0"].Direct, "a workspace member's dependency is direct")
		assert.False(t, m["other@1.0.0"].Direct)
	})
}
