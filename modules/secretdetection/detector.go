// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretdetection

import (
	"bytes"
	"sort"
	"unicode/utf16"
	"unicode/utf8"

	"forgejo.org/modules/keying"
)

const (
	// DefaultMaxContentSize is used when Detector.MaxContentSize is not set.
	DefaultMaxContentSize = 10 << 20
	// DefaultMaxMatchesPerRule is the number of findings one rule may report
	// per Scan, when Detector.MaxMatchesPerRule is not set. Only accepted
	// findings count: placeholders and other rejected matches never use it up.
	DefaultMaxMatchesPerRule = 100
	// DefaultMaxSecretLen is the longest secret reported (Rule.MaxSecretLen
	// overrides it): a longer capture is a bad pattern or not a secret.
	DefaultMaxSecretLen = 1 << 10

	// maxAttemptsPerRule bounds the regex matches one rule is asked for per
	// Scan, accepted or not.
	maxAttemptsPerRule = 10000

	binarySniffLen = 8000 // as git
	lfsPointerHead = "version https://git-lfs.github.com/spec/v1"
	lfsBlobMax     = 1024 // as modules/lfs BlobSizeCutoff
)

// Finding is a secret found at a place. It never holds the secret: only its
// fingerprint, so printing or logging a Finding, however it is nested, can't
// leak it. Callers must never store or log the secret they scanned either.
type Finding struct {
	RuleID    string
	StartLine int // 1-based, line of the first byte of the secret
	EndLine   int // 1-based, line of its last byte (PEM blocks span lines)
	StartCol  int // 1-based byte column in StartLine
	// Fingerprint is Fingerprint(secret) (of the normalized secret, see
	// Rule.Normalize).
	Fingerprint string
}

// Detector scans content with a fixed set of rules. Set the limits right after
// NewDetector; after that a Detector may be used by many goroutines.
type Detector struct {
	// MaxContentSize cuts the content scanned (bytes); 0 or less means
	// DefaultMaxContentSize.
	MaxContentSize int
	// MaxMatchesPerRule is the findings per rule; 0 or less means
	// DefaultMaxMatchesPerRule.
	MaxMatchesPerRule int

	rules []*compiledRule
}

// NewDetector compiles rules. Which rules to pass is the caller's decision.
func NewDetector(rules []Rule) (*Detector, error) {
	d := &Detector{}
	for _, r := range rules {
		c, err := compileRule(r)
		if err != nil {
			return nil, err
		}
		d.rules = append(d.rules, c)
	}
	return d, nil
}

type rawFinding struct {
	rule        *compiledRule
	start, end  int
	fingerprint string
}

// Scan returns the secrets in content, ordered by position. Binary content (a
// NUL byte in the first 8000 bytes, as git decides; UTF-16 text with a byte
// order mark is decoded first) and git LFS pointer files give nothing.
func (d *Detector) Scan(content []byte) []Finding {
	maxSize := d.MaxContentSize
	if maxSize <= 0 {
		maxSize = DefaultMaxContentSize
	}
	maxFindings := d.MaxMatchesPerRule
	if maxFindings <= 0 {
		maxFindings = DefaultMaxMatchesPerRule
	}
	if content = text(content, maxSize); content == nil {
		return nil
	}
	lower := asciiLower(content)

	var macKey []byte
	var found []rawFinding
	for _, r := range d.rules {
		if !hasKeyword(lower, r.Keywords) {
			continue
		}
		maxLen := r.MaxSecretLen
		if maxLen <= 0 {
			maxLen = DefaultMaxSecretLen
		}
		accepted := 0
		for _, m := range r.re.FindAllSubmatchIndex(content, maxAttemptsPerRule) {
			start, end := m[2*r.SecretGroup], m[2*r.SecretGroup+1]
			if start < 0 || end <= start || end-start > maxLen || !r.boundaryOK(content, start, end) {
				continue
			}
			secret := content[start:end]
			if r.Normalize != nil {
				if secret = r.Normalize(secret); len(secret) == 0 {
					continue
				}
			}
			if !r.accepts(secret) || overlaps(found, start, end) {
				continue
			}
			if macKey == nil {
				macKey = keying.MACKey(keying.SecretScanning)
			}
			found = append(found, rawFinding{r, start, end, fingerprint(macKey, secret)})
			if accepted++; accepted >= maxFindings {
				break
			}
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].start < found[j].start })

	// One forward pass over the content turns offsets into lines, so memory
	// stays O(findings) however many lines there are.
	findings := make([]Finding, len(found))
	pos, line, lineStart := 0, 1, 0
	for i, f := range found {
		for {
			e := lineBreakEnd(content, pos, f.start)
			if e < 0 {
				break
			}
			pos, line, lineStart = e, line+1, e
		}
		pos = f.start
		endLine := line
		for p := f.start; ; {
			e := lineBreakEnd(content, p, f.end-1)
			if e < 0 {
				break
			}
			p, endLine = e, endLine+1
		}
		findings[i] = Finding{RuleID: f.rule.ID, StartLine: line, EndLine: endLine, StartCol: f.start - lineStart + 1, Fingerprint: f.fingerprint}
	}
	return findings
}

// text returns the content to scan as UTF-8, cut to maxSize bytes, or nil if
// there is nothing to scan: binary, an LFS pointer or empty.
func text(content []byte, maxSize int) []byte {
	if len(content) >= 2 {
		switch {
		case content[0] == 0xFF && content[1] == 0xFE:
			content = decodeUTF16(content[2:min(len(content), 2*maxSize+2)], false)
		case content[0] == 0xFE && content[1] == 0xFF:
			content = decodeUTF16(content[2:min(len(content), 2*maxSize+2)], true)
		default:
			if bytes.IndexByte(content[:min(len(content), binarySniffLen)], 0) >= 0 || isLFSPointer(content) {
				return nil
			}
		}
	}
	if len(content) > maxSize {
		content = content[:maxSize]
	}
	if len(content) == 0 {
		return nil
	}
	return content
}

func decodeUTF16(b []byte, bigEndian bool) []byte {
	units := make([]uint16, len(b)/2)
	for i := range units {
		if bigEndian {
			units[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
		} else {
			units[i] = uint16(b[2*i+1])<<8 | uint16(b[2*i])
		}
	}
	out := make([]byte, 0, len(b))
	for _, r := range utf16.Decode(units) {
		out = utf8.AppendRune(out, r)
	}
	return out
}

// isLFSPointer is true for what git-lfs writes as a pointer (the checks of
// modules/lfs ReadPointerFromBuffer): at most 1024 bytes, the version line, an
// "oid sha256:<64 hex>" line and a "size <n>" line. Anything else that starts
// with the version line is a file like any other.
func isLFSPointer(content []byte) bool {
	if len(content) > lfsBlobMax || !bytes.HasPrefix(content, []byte(lfsPointerHead)) {
		return false
	}
	lines := bytes.Split(content, []byte("\n"))
	if len(lines) < 3 || len(lines) > 4 || len(lines) == 4 && len(lines[3]) > 0 || !bytes.HasPrefix(lines[1], []byte("oid sha256:")) || len(lines[1]) != len("oid sha256:")+64 {
		return false
	}
	for _, c := range lines[1][len("oid sha256:"):] {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	size := bytes.TrimPrefix(lines[2], []byte("size "))
	if len(size) == 0 {
		return false
	}
	for _, c := range size {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// accepts checks the secret itself, never the text around it: context such as
// XML tags or a template next to a real key must not drop it.
func (r *compiledRule) accepts(secret []byte) bool {
	if r.MinEntropy > 0 && entropy(secret) < r.MinEntropy {
		return false
	}
	for _, a := range r.allowlist {
		if a.Match(secret) {
			return false
		}
	}
	return r.Check == nil || r.Check(secret)
}

// overlaps: a secret overlapping one already found is the same secret, the
// first rule wins.
func overlaps(found []rawFinding, start, end int) bool {
	for _, f := range found {
		if start < f.end && f.start < end {
			return true
		}
	}
	return false
}

// lineBreakEnd returns the offset just past the first line break that starts
// in c[from:to], or -1. A line ends at LF, CRLF or a lone CR.
func lineBreakEnd(c []byte, from, to int) int {
	for from < to {
		i := bytes.IndexAny(c[from:to], "\r\n")
		if i < 0 {
			return -1
		}
		i += from
		if c[i] == '\r' && i+1 < len(c) && c[i+1] == '\n' {
			from = i + 1 // the LF counts
			continue
		}
		return i + 1
	}
	return -1
}

// asciiLower lowers A-Z only, so offsets stay the same as in content.
func asciiLower(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return out
}

func hasKeyword(lower []byte, keywords []string) bool {
	if len(keywords) == 0 {
		return true
	}
	for _, k := range keywords {
		if bytes.Contains(lower, []byte(k)) {
			return true
		}
	}
	return false
}
