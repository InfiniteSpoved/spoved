// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package zoekt

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"forgejo.org/modules/indexer/code/internal"

	"github.com/sourcegraph/zoekt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertZoektResult_BasicMatch(t *testing.T) {
	content := "hello world\nsecond line\n"

	f := chunkFile(content, [2]int{6, 11}) // "world"
	f.RepositoryID, f.FileName, f.Version, f.Language = 1, "main.go", "commit123", "Go"

	results := convertZoektResult([]zoekt.FileMatch{f})
	require.Len(t, results, 1)

	res := results[0]

	assert.Equal(t, int64(1), res.RepoID)
	assert.Equal(t, "main.go", res.Filename)
	assert.Equal(t, "commit123", res.CommitID)
	assert.Equal(t, "Go", res.Language)
	assert.Equal(t, content, res.Content)

	require.Len(t, res.Matches, 1)
	assert.Equal(t, 6, res.Matches[0].Start)
	assert.Equal(t, 11, res.Matches[0].End)
	assert.Equal(t, 1, res.Matches[0].LineNumber)

	assert.Equal(t, []int{1, 2}, res.LineNumbers)
	assert.Equal(t, []int{0, 12}, res.LineOffsets)
}

func TestConvertZoektResult_PlainTextFallback(t *testing.T) {
	f := chunkFile("just text", [2]int{0, 4})
	f.RepositoryID, f.FileName, f.Version = 2, "README", "commit456"

	results := convertZoektResult([]zoekt.FileMatch{f})
	require.Len(t, results, 1)

	assert.Equal(t, "Plain Text", results[0].Language)
}

func TestGetSearchResultLanguages(t *testing.T) {
	searchResult := &zoekt.SearchResult{
		Files: []zoekt.FileMatch{
			{Language: "Go"},
			{Language: "Go"},
			{Language: "Rust"},
			{Language: ""},
		},
	}

	stats := getSearchResultLanguages(searchResult)

	require.Len(t, stats, 3)

	assert.Equal(t, "Go", stats[0].Language)
	assert.Equal(t, 2, stats[0].Count)

	assert.Equal(t, "Plain Text", stats[1].Language)
	assert.Equal(t, 1, stats[1].Count)

	assert.Equal(t, "Rust", stats[2].Language)
	assert.Equal(t, 1, stats[2].Count)
}

func TestGenerateZoektQuery_Union(t *testing.T) {
	indexer := &Indexer{}

	opts := &internal.SearchOptions{
		Keyword: "foo bar",
		Mode:    internal.CodeSearchModeUnion,
		RepoIDs: []int64{1, 2},
	}

	q, err := indexer.generateZoektQuery(opts)
	require.NoError(t, err)
	require.NotNil(t, q)
}

func TestGenerateZoektQuery_SpecialCharacters(t *testing.T) {
	indexer := &Indexer{}

	opts := &internal.SearchOptions{
		Keyword: `foo.bar+baz[qux]`,
		Mode:    internal.CodeSearchModeExact,
	}

	q, err := indexer.generateZoektQuery(opts)
	require.NoError(t, err)
	require.NotNil(t, q)
}

func TestZoektFormatter_Format(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		content       string
		lineNumber    int
		offset        int // offset relative to the start of the file
		matchLength   int
		expectedLines []int
		expectedText  string
	}{
		{
			name:          "single line without trailing newline",
			content:       "test",
			lineNumber:    1,
			offset:        0,
			matchLength:   4,
			expectedLines: []int{1},
			expectedText:  "test",
		},
		{
			name:          "match on the last line without trailing newline",
			content:       "one\ntwo\ntest",
			lineNumber:    3,
			offset:        8,
			matchLength:   4,
			expectedLines: []int{2, 3},
			expectedText:  "test",
		},
		{
			name:          "match on the last line with trailing newline",
			content:       "one\ntwo\ntest\n",
			lineNumber:    3,
			offset:        8,
			matchLength:   4,
			expectedLines: []int{2, 3},
			expectedText:  "test",
		},
		{
			name:          "match on the first line",
			content:       "test\ntwo\nthree",
			lineNumber:    1,
			offset:        0,
			matchLength:   4,
			expectedLines: []int{1, 2},
			expectedText:  "test",
		},
		{
			name:          "match on a line in the middle",
			content:       "one\ntest\nthree\nfour",
			lineNumber:    2,
			offset:        4,
			matchLength:   4,
			expectedLines: []int{1, 2, 3},
			expectedText:  "test",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			f := chunkFile(testCase.content, [2]int{testCase.offset, testCase.offset + testCase.matchLength})
			f.RepositoryID, f.FileName, f.Version = 1, "example.txt", "commit123"
			files := []zoekt.FileMatch{f}

			results := convertZoektResult(files)
			require.Len(t, results, 1)

			formatter := &zoektFormatter{}
			result, err := formatter.Format(results[0])
			require.NoError(t, err)

			lineNumbers := make([]int, 0, len(result.Lines))
			for _, line := range result.Lines {
				lineNumbers = append(lineNumbers, line.Num)
			}
			assert.Equal(t, testCase.expectedLines, lineNumbers)

			var matchedLine string
			for _, line := range result.Lines {
				if line.Num == testCase.lineNumber {
					matchedLine = string(line.FormattedContent)
				}
			}
			assert.Contains(t, matchedLine, `<span class="search-highlight">`+testCase.expectedText+`</span>`)
		})
	}
}

func TestConvertZoektResult_IgnoresFilenameMatches(t *testing.T) {
	files := []zoekt.FileMatch{
		{
			RepositoryID: 1,
			FileName:     "foo.go",
			Version:      "commit123",
			ChunkMatches: []zoekt.ChunkMatch{
				{FileName: true, Content: []byte("foo.go"), Ranges: []zoekt.Range{{End: zoekt.Location{ByteOffset: 3}}}},
			},
		},
	}

	results := convertZoektResult(files)
	assert.Empty(t, results)
}

// chunkFile returns a file with one chunk holding the whole content from line 1, matching the given byte ranges.
func chunkFile(content string, ranges ...[2]int) zoekt.FileMatch {
	return zoekt.FileMatch{ChunkMatches: []zoekt.ChunkMatch{chunkAt(content, 1, 0, ranges...)}}
}

// chunkAt returns a chunk whose content starts at the given line and byte offset of the file;
// ranges are file offsets.
func chunkAt(content string, line, off int, ranges ...[2]int) zoekt.ChunkMatch {
	loc := func(o int) zoekt.Location {
		l := line + strings.Count(content[:min(max(o-off, 0), len(content))], "\n")
		return zoekt.Location{ByteOffset: uint32(o), LineNumber: uint32(l)}
	}
	cm := zoekt.ChunkMatch{Content: []byte(content), ContentStart: zoekt.Location{ByteOffset: uint32(off), LineNumber: uint32(line), Column: 1}}
	for _, r := range ranges {
		cm.Ranges = append(cm.Ranges, zoekt.Range{Start: loc(r[0]), End: loc(r[1])})
	}
	return cm
}

func assertMatchesInside(t *testing.T, res *internal.SearchResult) {
	t.Helper()
	for _, m := range res.Matches {
		assert.LessOrEqual(t, m.Start, m.End)
		assert.LessOrEqual(t, m.End, len(res.Content))
	}
	assert.True(t, utf8.ValidString(res.Content))
}

// the per-file match count and content size are bounded, whatever the file holds
func TestConvertZoektResult_BoundsMatchesAndContent(t *testing.T) {
	var sb strings.Builder
	var ranges [][2]int
	for range 5000 {
		ranges = append(ranges, [2]int{sb.Len(), sb.Len() + 1})
		sb.WriteString("a\n")
	}
	// one chunk with thousands of matches
	res := convertZoektResult([]zoekt.FileMatch{chunkFile(sb.String(), ranges...)})
	require.Len(t, res, 1)
	assert.Len(t, res[0].Matches, maxMatchesPerFile)
	assert.LessOrEqual(t, len(res[0].Content), maxResultContentBytes)
	assertMatchesInside(t, res[0])

	// many chunks: the cap is per file, not per chunk
	f := zoekt.FileMatch{}
	off := 0
	for i := range 500 {
		f.ChunkMatches = append(f.ChunkMatches, chunkAt("a\n", i+1, off, [2]int{off, off + 1}))
		off += 2
	}
	res = convertZoektResult([]zoekt.FileMatch{f})
	require.Len(t, res, 1)
	assert.Len(t, res[0].Matches, maxMatchesPerFile)
	assert.Len(t, res[0].LineNumbers, maxMatchesPerFile)
	assert.LessOrEqual(t, len(res[0].Content), maxResultContentBytes)
}

// a huge single line is cut; matches past the cut are dropped, a straddling one too
func TestConvertZoektResult_CutsHugeLine(t *testing.T) {
	line := strings.Repeat("é", 5<<20) // 10 MiB, multi-byte runes
	n := len(line)
	res := convertZoektResult([]zoekt.FileMatch{chunkFile(line, [2]int{0, 2}, [2]int{maxChunkBytes - 1, maxChunkBytes + 1}, [2]int{n - 2, n})})
	require.Len(t, res, 1)
	assert.LessOrEqual(t, len(res[0].Content), maxChunkBytes)
	require.Len(t, res[0].Matches, 1) // only the first one is fully inside
	assert.Equal(t, 0, res[0].Matches[0].Start)
	assertMatchesInside(t, res[0])

	// all matches are past the cut: no result at all
	assert.Empty(t, convertZoektResult([]zoekt.FileMatch{chunkFile(line, [2]int{n - 2, n})}))
}

// a regex like (?s). yields ranges across lines: they are clamped to their first line
func TestConvertZoektResult_MultiLineRange(t *testing.T) {
	res := convertZoektResult([]zoekt.FileMatch{chunkFile("one\ntwo\nthree\n", [2]int{2, 10})})
	require.Len(t, res, 1)
	require.Len(t, res[0].Matches, 1)
	assert.Equal(t, internal.Match{Start: 2, End: 3, LineNumber: 1}, res[0].Matches[0])
}

// zoekt orders ranges by score: the cap keeps the first ones by position
func TestConvertZoektResult_SortsRanges(t *testing.T) {
	var sb strings.Builder
	var ranges [][2]int
	for range 2 * maxMatchesPerFile {
		ranges = append(ranges, [2]int{sb.Len(), sb.Len() + 1})
		sb.WriteString("a\n")
	}
	slices.Reverse(ranges)
	res := convertZoektResult([]zoekt.FileMatch{chunkFile(sb.String(), ranges...)})
	require.Len(t, res, 1)
	require.Len(t, res[0].Matches, maxMatchesPerFile)
	for i, m := range res[0].Matches {
		assert.Equal(t, i+1, m.LineNumber)
	}
}

// sparse chunks keep their real line numbers; the formatter shows two separate blocks
func TestZoektFormatter_SparseChunks(t *testing.T) {
	f := zoekt.FileMatch{FileName: "a.txt", ChunkMatches: []zoekt.ChunkMatch{
		chunkAt("l99\nfoo\nl101\n", 99, 1000, [2]int{1004, 1007}),
		chunkAt("m499\nbar\n", 499, 5000, [2]int{5005, 5008}),
	}}
	res := convertZoektResult([]zoekt.FileMatch{f})
	require.Len(t, res, 1)
	assert.Equal(t, []int{99, 100, 101, 499, 500}, res[0].LineNumbers)
	assert.Equal(t, 100, res[0].Matches[0].LineNumber)
	assert.Equal(t, 500, res[0].Matches[1].LineNumber)

	out, err := (&zoektFormatter{}).Format(res[0])
	require.NoError(t, err)
	var nums []int
	for _, l := range out.Lines {
		nums = append(nums, l.Num)
		if l.Num == 100 || l.Num == 500 {
			assert.Contains(t, string(l.FormattedContent), `<span class="search-highlight">`)
		}
	}
	assert.Equal(t, []int{99, 100, 101, 499, 500}, nums)
}

// a match whose line (or a context line) is not in the result is skipped, not a panic
func TestZoektFormatter_MissingLines(t *testing.T) {
	r := &internal.SearchResult{
		Content: "foo\n", LineNumbers: []int{7}, LineOffsets: []int{0},
		Matches: []internal.Match{{Start: 0, End: 3, LineNumber: 7}, {Start: 0, End: 3, LineNumber: 99}, {Start: 90, End: 99, LineNumber: 0}},
	}
	out, err := (&zoektFormatter{}).Format(r)
	require.NoError(t, err)
	require.Len(t, out.Lines, 1)
	assert.Equal(t, 7, out.Lines[0].Num)
}

// a chunk without a final newline (cut, or the end of the file) does not glue onto the next chunk
func TestConvertZoektResult_ChunksStayOnOwnLines(t *testing.T) {
	f := zoekt.FileMatch{FileName: "a.txt", ChunkMatches: []zoekt.ChunkMatch{
		chunkAt(strings.Repeat("a", 10000)+"\n", 1, 0, [2]int{0, 3}),
		chunkAt("foo NEEDLE bar\n", 20, 20000, [2]int{20004, 20010}),
	}}
	res := convertZoektResult([]zoekt.FileMatch{f})
	require.Len(t, res, 1)
	assert.Equal(t, strings.Repeat("a", maxChunkBytes)+"\nfoo NEEDLE bar\n", res[0].Content)
	assert.Equal(t, []int{1, 20}, res[0].LineNumbers)
	assert.Equal(t, []int{0, maxChunkBytes + 1}, res[0].LineOffsets)
	assert.Equal(t, internal.Match{Start: maxChunkBytes + 5, End: maxChunkBytes + 11, LineNumber: 20}, res[0].Matches[1])

	out, err := (&zoektFormatter{}).Format(res[0])
	require.NoError(t, err)
	var nums []int
	for _, l := range out.Lines {
		nums = append(nums, l.Num)
		if l.Num == 20 {
			assert.Contains(t, string(l.FormattedContent), `<span class="search-highlight">NEEDLE</span>`)
		}
	}
	assert.Equal(t, []int{1, 20}, nums)
}
