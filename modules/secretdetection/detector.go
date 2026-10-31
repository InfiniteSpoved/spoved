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
	// MaxWindowBytes is how much of the content a rule with Rule.Window runs on.
	MaxWindowBytes = 1 << 20

	binarySniffLen = 8000 // as git
	lfsPointerHead = "version https://git-lfs.github.com/spec/v1"
	lfsBlobMax     = 1024 // as modules/lfs BlobSizeCutoff
)

// Finding is a secret found at a place. It never holds the secret: only its
// fingerprint, so printing or logging a Finding, however it is nested, can't
// leak it. Callers must never store or log the secret they scanned either.
type Finding struct {
	RuleID    string
	StartLine int // 1-based, line of the first byte of the secret; lines end at LF, as in git
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

// Scan returns the secrets in content, ordered by position, and whether it
// looked at everything: complete is false when the content was cut at
// MaxContentSize, a rule with a Window reached MaxWindowBytes, or a rule
// reached MaxMatchesPerRule (so there may be more findings). Callers record
// that a scan was partial. Binary content (a NUL byte in the first 8000
// bytes, as git decides) and git LFS pointer files give nothing and count as
// complete. UTF-16 text with a byte order mark is decoded first.
func (d *Detector) Scan(content []byte) (findings []Finding, complete bool) {
	maxSize := d.MaxContentSize
	if maxSize <= 0 {
		maxSize = DefaultMaxContentSize
	}
	maxFindings := d.MaxMatchesPerRule
	if maxFindings <= 0 {
		maxFindings = DefaultMaxMatchesPerRule
	}
	content, complete = text(content, maxSize)
	if len(content) == 0 {
		return nil, complete
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
		ranges, ok := r.ranges(lower)
		complete = complete && ok
		accepted := 0
	rangeLoop:
		for _, rg := range ranges {
			// One match at a time, so memory stays O(findings) however many
			// matches the rule rejects.
			for pos := rg[0]; pos < rg[1]; {
				m := r.re.FindSubmatchIndex(content[pos:rg[1]])
				if m == nil {
					break
				}
				base := pos
				pos += max(m[1], 1)
				start, end := m[2*r.SecretGroup], m[2*r.SecretGroup+1]
				if start < 0 || end <= start {
					continue
				}
				start, end = start+base, end+base
				if end-start > maxLen || !r.boundaryOK(content, start, end) {
					continue
				}
				secret := content[start:end]
				if r.Normalize != nil {
					if secret = r.Normalize(secret); len(secret) == 0 {
						continue
					}
				}
				if len(secret) < r.MinSecretLen || !r.accepts(secret) || overlaps(found, start, end) {
					continue
				}
				if macKey == nil {
					macKey = keying.MACKey(keying.SecretScanning)
				}
				found = append(found, rawFinding{r, start, end, fingerprint(macKey, secret)})
				if accepted++; accepted >= maxFindings {
					complete = false
					break rangeLoop
				}
			}
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].start < found[j].start })

	// One forward pass over the content turns offsets into lines, so memory
	// stays O(findings) however many lines there are.
	findings = make([]Finding, len(found))
	pos, line, lineStart := 0, 1, 0
	for i, f := range found {
		line += bytes.Count(content[pos:f.start], []byte("\n"))
		if nl := bytes.LastIndexByte(content[pos:f.start], '\n'); nl >= 0 {
			lineStart = pos + nl + 1
		}
		pos = f.start
		findings[i] = Finding{
			RuleID:      f.rule.ID,
			StartLine:   line,
			EndLine:     line + bytes.Count(content[f.start:f.end-1], []byte("\n")),
			StartCol:    f.start - lineStart + 1,
			Fingerprint: f.fingerprint,
		}
	}
	return findings, complete
}

// text returns the content to scan as UTF-8, cut to maxSize bytes (complete is
// false then), or nothing if there is nothing to scan: binary, an LFS pointer
// or empty.
func text(content []byte, maxSize int) (out []byte, complete bool) {
	if len(content) >= 2 && (content[0] == 0xFF && content[1] == 0xFE || content[0] == 0xFE && content[1] == 0xFF) {
		// UTF-16 only if the rest decodes cleanly, else it is bytes like any other.
		cut := len(content) > maxSize+2
		if decoded, ok := decodeUTF16(content[2:min(len(content), maxSize+2)], content[0] == 0xFE, cut); ok {
			if len(decoded) > maxSize {
				return decoded[:maxSize], false
			}
			return decoded, !cut
		}
	}
	if bytes.IndexByte(content[:min(len(content), binarySniffLen)], 0) >= 0 || isLFSPointer(content) {
		return nil, true
	}
	if len(content) > maxSize {
		return content[:maxSize], false
	}
	return content, true
}

// decodeUTF16 decodes b (after the byte order mark) to UTF-8. It fails on an
// odd length or an unpaired surrogate, so a file that merely starts with FF FE
// is not taken for UTF-16. If cut, b ends where the size limit fell: a last
// unit that is a lone high surrogate, or an odd last byte, is dropped.
func decodeUTF16(b []byte, bigEndian, cut bool) ([]byte, bool) {
	if cut {
		b = b[:len(b)&^1]
	}
	if len(b)%2 != 0 {
		return nil, false
	}
	unit := func(i int) rune {
		if bigEndian {
			return rune(b[2*i])<<8 | rune(b[2*i+1])
		}
		return rune(b[2*i+1])<<8 | rune(b[2*i])
	}
	n := len(b) / 2
	out := make([]byte, 0, len(b)*3/2) // at most 3 bytes per unit
	for i := 0; i < n; i++ {
		u := unit(i)
		switch {
		case utf16.IsSurrogate(u) && i+1 < n:
			r := utf16.DecodeRune(u, unit(i+1))
			if r == utf8.RuneError {
				return nil, false
			}
			out = utf8.AppendRune(out, r)
			i++
		case utf16.IsSurrogate(u):
			if !cut {
				return nil, false
			}
		default:
			out = utf8.AppendRune(out, u)
		}
	}
	return out, true
}

// isLFSPointer is true for what git-lfs writes as a pointer (the checks of
// modules/lfs ReadPointerFromBuffer): at most 1024 bytes, the version line
// exactly, an "oid sha256:<64 hex>" line and a "size <n>" line. Anything else
// that starts with the version line is a file like any other.
func isLFSPointer(content []byte) bool {
	if len(content) > lfsBlobMax || !bytes.HasPrefix(content, []byte(lfsPointerHead)) {
		return false
	}
	lines := bytes.Split(content, []byte("\n"))
	if len(lines) < 3 || len(lines) > 4 || len(lines) == 4 && len(lines[3]) > 0 ||
		string(lines[0]) != lfsPointerHead ||
		!bytes.HasPrefix(lines[1], []byte("oid sha256:")) || len(lines[1]) != len("oid sha256:")+64 {
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

// ranges returns the byte ranges the rule's regex runs on: all of the content,
// or for a rule with a Window the merged windows around keyword hits, at most
// MaxWindowBytes of them (ok is false when more were left out).
func (r *compiledRule) ranges(lower []byte) (out [][2]int, ok bool) {
	if r.Window <= 0 {
		return [][2]int{{0, len(lower)}}, true
	}
	ok = true
	for _, k := range r.Keywords {
		var cur [2]int
		have, total := false, 0
		for off := 0; ; {
			i := bytes.Index(lower[off:], []byte(k))
			if i < 0 {
				break
			}
			p := off + i
			off = p + 1
			lo, hi := max(0, p-r.Window), min(len(lower), p+len(k)+r.Window)
			if have && lo <= cur[1] {
				cur[1] = hi
				continue
			}
			if have {
				out = append(out, cur)
				total += cur[1] - cur[0]
			}
			cur, have = [2]int{lo, hi}, true
			if total > MaxWindowBytes {
				ok, have = false, false
				break
			}
		}
		if have {
			out = append(out, cur)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	merged, total := out[:0], 0
	for _, w := range out {
		if n := len(merged); n > 0 && w[0] <= merged[n-1][1] {
			merged[n-1][1] = max(merged[n-1][1], w[1])
			continue
		}
		merged = append(merged, w)
	}
	for i, w := range merged {
		if total += w[1] - w[0]; total > MaxWindowBytes {
			return merged[:i], false
		}
	}
	return merged, ok
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
