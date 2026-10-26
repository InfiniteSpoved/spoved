// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// Package dependency reads the dependencies a repository declares in its manifests and lock files. The parsers are
// pure Go: nothing is executed, downloaded or read from outside the manifest's own bytes (the callers hand over the
// few sibling files a format needs).
package dependency

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Ecosystems, named as OSV names them. This is a closed list: nothing else is stored.
const (
	Go        = "Go"
	NPM       = "npm"
	PyPI      = "PyPI"
	Maven     = "Maven"
	NuGet     = "NuGet"
	Cargo     = "crates.io"
	RubyGems  = "RubyGems"
	Packagist = "Packagist"
)

// Ecosystems lists every ecosystem.
var Ecosystems = []string{Go, NPM, PyPI, Maven, NuGet, Cargo, RubyGems, Packagist}

// IsEcosystem reports whether e is one of Ecosystems.
func IsEcosystem(e string) bool {
	return slices.Contains(Ecosystems, e)
}

// Limits of one stored row, in bytes.
const (
	MaxNameLen    = 512
	MaxVersionLen = 256
	MaxLicenseLen = 256
	MaxPathLen    = 1024

	// MaxSmallManifestSize is the size limit of the formats that are small by nature (go.mod, requirements.txt,
	// pom.xml, project files, Gemfile.lock); lock files may be as large as the configured limit.
	MaxSmallManifestSize = 1 << 20

	maxLineLen = 64 << 10
	maxXMLDeep = 256
)

// ErrParse is the error of a manifest that can't be read. The text of the wrapped error may hold bytes of the
// manifest, so callers store and show only the fixed code.
var ErrParse = errors.New("dependency: cannot parse manifest")

// Dependency is one package a manifest declares or locks.
type Dependency struct {
	Ecosystem string
	Name      string
	Version   string // "" if the manifest doesn't fix it
	License   string // as the manifest states it, "" if unknown
	Direct    bool   // declared by the project itself, not only needed by another dependency
}

// Finder returns the content of the file name next to the manifest, or with up, in the nearest directory above it
// (the manifest's own directory first). It returns nil if there is none. The caller limits the size.
type Finder func(name string, up bool) []byte

// Format is a manifest format.
type Format struct {
	Ecosystem string
	// Small formats are limited to MaxSmallManifestSize.
	Small bool
	match func(base string) bool
	parse func(s *sink, data []byte, find Finder) error
}

var formats []*Format

func register(f *Format) { formats = append(formats, f) }

// FormatFor returns the format of a file with the given base name, or nil. Names are matched exactly (the project
// files by their suffix), as git stores them.
func FormatFor(base string) *Format {
	for _, f := range formats {
		if f.match(base) {
			return f
		}
	}
	return nil
}

// FormatForPath is FormatFor for a path in the repository: it also knows the Django layout, where any *.txt file
// directly inside a directory named requirements is a requirements file.
func FormatForPath(p string) *Format {
	base := path.Base(p)
	if f := FormatFor(base); f != nil {
		return f
	}
	if strings.HasSuffix(base, ".txt") && path.Base(path.Dir(p)) == "requirements" {
		return FormatFor("requirements.txt")
	}
	return nil
}

// ErrTooManyDependencies is returned when a manifest holds more rows than the caller allows, or so many entries that
// reading it would cost more than that many rows do. It stops the parser at once: the work and the memory of a parse
// are bounded by the limit, not by the shape of the input.
var ErrTooManyDependencies = errors.New("dependency: too many dependencies")

// DefaultMaxDependencies is the row limit of Parse.
const DefaultMaxDependencies = 50000

// entryFactor is how many intermediate entries (map keys, lockfile entries, lines with a key) a parse may keep per
// allowed row.
const entryFactor = 4

// sink receives the rows of a parse. emit counts the rows, entry counts everything else a parser keeps in memory;
// both fail with ErrTooManyDependencies past their limit, and both look at the context now and then.
type sink struct {
	ctx        context.Context
	maxRows    int
	maxEntries int
	rows       []Dependency
	index      map[[2]string]int // name, version -> position in rows
	entries    int
	ticks      int
}

func newSink(ctx context.Context, maxRows int) *sink {
	return &sink{ctx: ctx, maxRows: maxRows, maxEntries: maxRows * entryFactor, index: map[[2]string]int{}}
}

func (s *sink) tick() error {
	if s.ticks++; s.ticks&255 == 0 {
		return s.ctx.Err()
	}
	return nil
}

// emit adds a row; a package listed again (same name and version) is merged into the first row.
func (s *sink) emit(d Dependency) error {
	k := [2]string{d.Name, d.Version}
	if i, ok := s.index[k]; ok {
		s.rows[i].Direct = s.rows[i].Direct || d.Direct
		if s.rows[i].License == "" {
			s.rows[i].License = d.License
		}
		return s.tick()
	}
	if len(s.rows) >= s.maxRows {
		return ErrTooManyDependencies
	}
	s.index[k] = len(s.rows)
	s.rows = append(s.rows, d)
	return s.tick()
}

// entry accounts for one intermediate entry a parser is about to keep.
func (s *sink) entry() error {
	if s.entries++; s.entries > s.maxEntries {
		return ErrTooManyDependencies
	}
	return s.tick()
}

// Parse is ParseLimited with DefaultMaxDependencies.
func (f *Format) Parse(ctx context.Context, data []byte, find Finder) ([]Dependency, error) {
	return f.ParseLimited(ctx, data, find, DefaultMaxDependencies)
}

// ParseLimited reads a manifest and returns at most maxRows rows, or ErrTooManyDependencies. A panic of a parser is
// an ErrParse, and every row is validated: names, versions and licenses are cut of control characters and
// bidirectional marks, a row with an empty or too long value is dropped, and a package listed twice in one manifest
// is one row.
func (f *Format) ParseLimited(ctx context.Context, data []byte, find Finder, maxRows int) (deps []Dependency, err error) {
	defer func() {
		if r := recover(); r != nil {
			deps, err = nil, fmt.Errorf("%w: panic", ErrParse)
		}
	}()
	if find == nil {
		find = func(string, bool) []byte { return nil }
	}
	s := newSink(ctx, max(maxRows, 0))
	err = f.parse(s, data, find)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return validate(f.Ecosystem, s.rows), nil
}

func clean(s string) string {
	s = strings.ToValidUTF8(s, "�")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == ' ' || r == ' ' {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
}

func validate(eco string, in []Dependency) []Dependency {
	out := make([]Dependency, 0, len(in))
	seen := map[[3]string]int{}
	for _, d := range in {
		d.Ecosystem = eco
		if len(d.Name) > MaxNameLen*2 || len(d.Version) > MaxVersionLen*2 || len(d.License) > MaxLicenseLen*2 {
			continue // not worth cleaning
		}
		d.Name, d.Version, d.License = clean(d.Name), clean(d.Version), clean(d.License)
		if d.Name == "" || len(d.Name) > MaxNameLen || len(d.Version) > MaxVersionLen {
			continue
		}
		if len(d.License) > MaxLicenseLen {
			d.License = ""
		}
		k := [3]string{eco, d.Name, d.Version}
		if i, ok := seen[k]; ok {
			out[i].Direct = out[i].Direct || d.Direct
			if out[i].License == "" {
				out[i].License = d.License
			}
			continue
		}
		seen[k] = len(out)
		out = append(out, d)
	}
	return out
}

// lineScanner reads lines of at most maxLineLen bytes and stops (Err reports why) once the context is done, so a
// scan that timed out doesn't finish a long file.
type lineScanner struct {
	*bufio.Scanner
	s *sink
}

func newScanner(s *sink, data []byte) *lineScanner {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 4096), maxLineLen)
	return &lineScanner{Scanner: sc, s: s}
}

func (l *lineScanner) Scan() bool {
	if l.s.tick() != nil {
		return false
	}
	return l.Scanner.Scan()
}

func (l *lineScanner) Err() error {
	if err := l.s.ctx.Err(); err != nil {
		return err
	}
	return l.Scanner.Err()
}

// scanErr turns the error of a line scan into the parse error, keeping the context and limit errors as they are.
func scanErr(err error, format string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrTooManyDependencies) {
		return err
	}
	return parseErr("%s", format)
}

func parseErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrParse, fmt.Sprintf(format, a...))
}

// PURL builds the package URL of a dependency (https://github.com/package-url/purl-spec) from its stored values,
// never from text of a manifest. It returns "" for an unknown ecosystem or an empty name.
func PURL(eco, name, version string) string {
	var typ, ns, n string
	switch eco {
	case Go:
		typ = "golang"
		ns, n = splitLast(strings.ToLower(name), "/")
	case NPM:
		typ = "npm"
		ns, n = splitLast(strings.ToLower(name), "/")
	case PyPI:
		typ = "pypi"
		n = strings.ReplaceAll(strings.ToLower(name), "_", "-")
	case Maven:
		typ = "maven"
		ns, n, _ = strings.Cut(name, ":")
		if n == "" {
			ns, n = "", ns
		}
	case NuGet:
		typ, n = "nuget", name
	case Cargo:
		typ, n = "cargo", name
	case RubyGems:
		typ, n = "gem", name
	case Packagist:
		typ = "composer"
		ns, n = splitLast(strings.ToLower(name), "/")
	default:
		return ""
	}
	if n == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("pkg:" + typ + "/")
	if ns != "" {
		for seg := range strings.SplitSeq(ns, "/") {
			b.WriteString(purlEscape(seg) + "/")
		}
	}
	b.WriteString(purlEscape(n))
	if version != "" {
		b.WriteString("@" + purlEscape(version))
	}
	return b.String()
}

func splitLast(s, sep string) (string, string) {
	if i := strings.LastIndex(s, sep); i >= 0 {
		return s[:i], s[i+len(sep):]
	}
	return "", s
}

// purlEscape percent-encodes everything but the unreserved characters.
func purlEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// SafeText cuts a string for display or storage as validate does.
func SafeText(s string) string { return clean(s) }

// ValidPath reports whether p can be stored as the path of a manifest.
func ValidPath(p string) bool {
	return p != "" && len(p) <= MaxPathLen && utf8.ValidString(p) && clean(p) == p && path.Clean(p) == p
}

// cmpOr returns err if it is set, else other.
func cmpOr(err, other error) error {
	if err != nil {
		return err
	}
	return other
}
