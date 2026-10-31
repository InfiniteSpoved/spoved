// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretdetection

import (
	"bytes"
	"fmt"
	"sort"
)

const (
	// DefaultMaxLineLength: of a longer line (minified JS, base64 blobs) only
	// the first bytes are scanned. A token cut at that point is not found, or
	// found truncated.
	DefaultMaxLineLength = 16 << 10
	// DefaultMaxMatchesPerRule bounds the regex matches one rule may produce
	// per Scan, whatever the content looks like.
	DefaultMaxMatchesPerRule = 100

	binarySniffLen = 8000 // as git
	lfsPointerHead = "version https://git-lfs.github.com/spec/v1"
)

// Finding is a secret found at a place. It deliberately carries the secret
// only privately: callers get its Fingerprint, never the bytes, and must never
// store or log the secret. String and GoString leave it out so a Finding is
// safe to print.
type Finding struct {
	RuleID    string
	StartLine int // 1-based, line of the first byte of the secret
	EndLine   int // 1-based, line of its last byte (PEM blocks span lines)
	StartCol  int // 1-based byte column in StartLine
	secret    []byte
}

// Fingerprint is Fingerprint(secret).
func (f Finding) Fingerprint() string { return Fingerprint(f.secret) }

func (f Finding) String() string {
	return fmt.Sprintf("%s at %d:%d-%d", f.RuleID, f.StartLine, f.StartCol, f.EndLine)
}

func (f Finding) GoString() string { return "secretdetection.Finding(" + f.String() + ")" }

// Detector scans content with a fixed set of rules. Set the limits right after
// NewDetector; after that a Detector may be used by many goroutines.
type Detector struct {
	// MaxContentSize cuts the content scanned (bytes); 0 = no limit.
	MaxContentSize int
	// MaxLineLength: see DefaultMaxLineLength.
	MaxLineLength int
	// MaxMatchesPerRule: see DefaultMaxMatchesPerRule.
	MaxMatchesPerRule int

	rules []*compiledRule
}

// NewDetector compiles rules. Which rules to pass is the caller's decision.
func NewDetector(rules []Rule) (*Detector, error) {
	d := &Detector{MaxLineLength: DefaultMaxLineLength, MaxMatchesPerRule: DefaultMaxMatchesPerRule}
	for _, r := range rules {
		c, err := compileRule(r)
		if err != nil {
			return nil, err
		}
		d.rules = append(d.rules, c)
	}
	return d, nil
}

// Scan returns the secrets in content. Binary content (a NUL byte in the first
// 8000 bytes, as git decides) and git LFS pointer files give nothing.
func (d *Detector) Scan(content []byte) []Finding {
	if bytes.IndexByte(content[:min(len(content), binarySniffLen)], 0) >= 0 || bytes.HasPrefix(content, []byte(lfsPointerHead)) {
		return nil
	}
	if d.MaxContentSize > 0 && len(content) > d.MaxContentSize {
		content = content[:d.MaxContentSize]
	}
	lower := asciiLower(content)
	spans := d.spans(content)

	var findings []Finding
	var lineStarts []int // built on the first finding
	var taken [][2]int   // spans of the secrets found: an overlapping one is the same secret, the first rule wins
	for _, r := range d.rules {
		if !hasKeyword(lower, r.Keywords) {
			continue
		}
		budget := d.MaxMatchesPerRule
		for _, sp := range spans {
			if budget <= 0 {
				break
			}
			for _, m := range r.re.FindAllSubmatchIndex(content[sp[0]:sp[1]], budget) {
				budget--
				start, end := m[2*r.SecretGroup], m[2*r.SecretGroup+1]
				if start < 0 || end <= start {
					continue
				}
				start, end = start+sp[0], end+sp[0]
				secret := content[start:end]
				if !r.accepts(secret) || overlaps(taken, start, end) {
					continue
				}
				taken = append(taken, [2]int{start, end})
				if lineStarts == nil {
					lineStarts = lineStartOffsets(content)
				}
				startLine, endLine := lineOf(lineStarts, start), lineOf(lineStarts, end-1)
				findings = append(findings, Finding{
					RuleID:    r.ID,
					StartLine: startLine,
					EndLine:   endLine,
					StartCol:  start - lineStarts[startLine-1] + 1,
					secret:    secret,
				})
			}
		}
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].StartLine != findings[j].StartLine {
			return findings[i].StartLine < findings[j].StartLine
		}
		return findings[i].StartCol < findings[j].StartCol
	})
	return findings
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

func overlaps(taken [][2]int, start, end int) bool {
	for _, t := range taken {
		if start < t[1] && t[0] < end {
			return true
		}
	}
	return false
}

// spans returns the byte ranges to scan: the content, minus the part of every
// line beyond MaxLineLength. Lines that are short enough stay in one range, so
// patterns across lines (PEM) still match.
func (d *Detector) spans(content []byte) [][2]int {
	maxLen := d.MaxLineLength
	if maxLen <= 0 {
		return [][2]int{{0, len(content)}}
	}
	var spans [][2]int
	begin := 0
	for pos := 0; pos < len(content); {
		next := len(content)
		if nl := bytes.IndexByte(content[pos:], '\n'); nl >= 0 {
			next = pos + nl + 1
		}
		if next-pos > maxLen {
			if pos+maxLen > begin {
				spans = append(spans, [2]int{begin, pos + maxLen})
			}
			begin = next
		}
		pos = next
	}
	if begin < len(content) {
		spans = append(spans, [2]int{begin, len(content)})
	}
	return spans
}

// lineStartOffsets lists the offset of the first byte of every line. Lines end
// at '\n', so CRLF files count like LF ones.
func lineStartOffsets(content []byte) []int {
	starts := []int{0}
	for pos := 0; ; {
		nl := bytes.IndexByte(content[pos:], '\n')
		if nl < 0 {
			return starts
		}
		pos += nl + 1
		starts = append(starts, pos)
	}
}

// lineOf returns the 1-based line of a byte offset.
func lineOf(starts []int, off int) int {
	return sort.SearchInts(starts, off+1)
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
