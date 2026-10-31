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
	// MaxRejectedPerRule is how many matches of one rule a Scan may throw away
	// (placeholders, boundaries, low entropy) before the rule stops.
	MaxRejectedPerRule = 10000

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
// MaxContentSize, a rule with a WindowBudget used it up, or a rule reached
// MaxMatchesPerRule or a custom rule MaxRejectedPerRule (so there may be more findings).
// Callers record that a scan was partial and must not treat it as clean.
// Binary content (a NUL byte in the first 8000 bytes, as git decides) and git
// LFS pointer files give nothing and count as complete. UTF-16 text (a NUL
// in the first 8000 bytes and a byte order mark, or NULs on one side of every
// byte pair) is decoded first.
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
		accepted, rejected := 0, 0
		// handle takes one match (offsets relative to base) and says whether
		// the rule is done for this Scan.
		handle := func(base int, m []int) (stop bool) {
			start, end := m[2*r.SecretGroup], m[2*r.SecretGroup+1]
			if start < 0 || end <= start {
				return false
			}
			start, end = start+base, end+base
			secret := content[start:end]
			ok := (!r.KeywordInMatch || hasKeyword(lower[base+m[0]:base+m[1]], r.Keywords)) &&
				end-start <= maxLen && r.boundaryOK(content, start, end)
			if ok && r.Normalize != nil {
				secret = r.Normalize(secret)
				ok = len(secret) > 0
			}
			if ok {
				ok = len(secret) >= r.MinSecretLen && r.accepts(secret) && !overlaps(found, start, end)
			}
			if !ok {
				if rejected++; r.RejectCap > 0 && rejected >= r.RejectCap {
					complete = false
					return true
				}
				return false
			}
			if macKey == nil {
				macKey = keying.MACKey(keying.SecretScanning)
			}
			found = append(found, rawFinding{r, start, end, fingerprint(macKey, secret)})
			if accepted++; accepted >= maxFindings {
				complete = false
				return true
			}
			return false
		}
		if r.Anchored {
			// The match starts at a keyword hit: try the anchored regex on
			// the bytes after it, which costs a few bytes per hit and not a
			// pass over everything between hits.
		anchoredLoop:
			for _, k := range r.Keywords {
				for next, off := 0, 0; ; {
					i := bytes.Index(lower[off:], []byte(k))
					if i < 0 {
						break
					}
					p := off + i
					off = p + 1
					start := p - r.KeywordOffset
					if p < next || start < 0 || r.HitCheck != nil && !r.HitCheck(content, p) {
						continue
					}
					m := r.anchored.FindSubmatchIndex(content[start:min(len(content), p+len(k)+r.Window)])
					if m == nil {
						continue
					}
					next = start + max(m[1], 1)
					if handle(start, m) {
						break anchoredLoop
					}
				}
			}
			continue
		}
		ranges, ok := r.ranges(lower)
		complete = complete && ok
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
				if handle(base, m) {
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
// or empty. Only a file with a NUL in its first 8000 bytes is tried as UTF-16
// (with a byte order mark, or without one when the NULs sit on one side of
// the byte pairs, as in UTF-16 ASCII text); one without is read as bytes, so
// a Latin-1 file that starts with FF FE is not turned into garbage.
func text(content []byte, maxSize int) (out []byte, complete bool) {
	if bytes.IndexByte(content[:min(len(content), binarySniffLen)], 0) >= 0 {
		bigEndian, bom, ok := utf16Order(content)
		if !ok {
			return nil, true // binary
		}
		body := content[2*boolInt(bom):]
		cut := len(body) > maxSize
		decoded := decodeUTF16(body[:min(len(body), maxSize)], bigEndian, cut)
		if len(decoded) == 0 {
			return nil, false // there was a body and nothing could be read from it
		}
		if len(decoded) > maxSize {
			return decoded[:maxSize], false
		}
		return decoded, !cut
	}
	if isLFSPointer(content) {
		return nil, true
	}
	if len(content) > maxSize {
		return content[:maxSize], false
	}
	return content, true
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// utf16Order says whether content (which has a NUL in its first 8000 bytes)
// is UTF-16, and its byte order: a byte order mark decides, else the NULs
// must be in the high (or low) byte of at least a quarter of the byte pairs
// and almost never in the other byte. UTF-32 and other binary data fail both.
func utf16Order(content []byte) (bigEndian, bom, ok bool) {
	if len(content) >= 2 {
		switch {
		case content[0] == 0xFF && content[1] == 0xFE:
			return false, true, true
		case content[0] == 0xFE && content[1] == 0xFF:
			return true, true, true
		}
	}
	sniff := content[:min(len(content), binarySniffLen)]
	pairs, evenNUL, oddNUL := len(sniff)/2, 0, 0
	for i := 0; i+1 < len(sniff); i += 2 {
		if sniff[i] == 0 {
			evenNUL++
		}
		if sniff[i+1] == 0 {
			oddNUL++
		}
	}
	switch {
	case oddNUL*4 >= pairs && evenNUL*10 <= oddNUL:
		return false, false, true
	case evenNUL*4 >= pairs && oddNUL*10 <= evenNUL:
		return true, false, true
	}
	return false, false, false
}

// decodeUTF16 decodes b to UTF-8 the way readers with replacement do: an odd
// last byte is dropped and an unpaired surrogate becomes U+FFFD, so a key in
// the valid part of a damaged file is still found. If cut, b ends where the
// size limit fell and a high surrogate in the last unit is dropped.
func decodeUTF16(b []byte, bigEndian, cut bool) []byte {
	n := len(b) / 2
	unit := func(i int) rune {
		if bigEndian {
			return rune(b[2*i])<<8 | rune(b[2*i+1])
		}
		return rune(b[2*i+1])<<8 | rune(b[2*i])
	}
	out := make([]byte, 0, n*3/2+4) // at most 3 bytes per unit
	for i := 0; i < n; i++ {
		switch u := unit(i); {
		case 0xD800 <= u && u < 0xDC00:
			switch {
			case i+1 < n && 0xDC00 <= unit(i+1) && unit(i+1) < 0xE000:
				out = utf8.AppendRune(out, utf16.DecodeRune(u, unit(i+1)))
				i++
			case i+1 == n && cut:
			default:
				out = utf8.AppendRune(out, utf8.RuneError)
			}
		case 0xDC00 <= u && u < 0xE000:
			out = utf8.AppendRune(out, utf8.RuneError)
		default:
			out = utf8.AppendRune(out, u)
		}
	}
	return out
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
// or for a rule with a Window the windows around keyword hits, merged once. A
// rule with a WindowBudget gets at most that many bytes: the window that
// crosses it is cut (never dropped) and ok is false.
func (r *compiledRule) ranges(lower []byte) (out [][2]int, ok bool) {
	if r.Window <= 0 {
		return [][2]int{{0, len(lower)}}, true
	}
	ok = true
	budget := r.WindowBudget
	for _, k := range r.Keywords {
		// Windows of one keyword, merged as they come. With a budget this
		// stops once they alone fill it: the union can't be smaller.
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
			if budget > 0 && total >= budget {
				ok = false
				break
			}
		}
		if have {
			out = append(out, cur)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	merged := out[:0]
	for _, w := range out {
		if n := len(merged); n > 0 && w[0] <= merged[n-1][1] {
			merged[n-1][1] = max(merged[n-1][1], w[1])
			continue
		}
		merged = append(merged, w)
	}
	if budget > 0 {
		total := 0
		for i, w := range merged {
			if n := w[1] - w[0]; total+n > budget {
				merged[i][1] = w[0] + budget - total
				if merged[i][1] == merged[i][0] {
					return merged[:i], false
				}
				return merged[:i+1], false
			}
			total += w[1] - w[0]
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
