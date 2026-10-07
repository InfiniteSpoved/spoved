// Copyright 2017 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package code

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"

	"forgejo.org/models/db"
	"forgejo.org/modules/indexer/code/internal"
)

type Result = internal.Result

type ResultLine = internal.ResultLine

type SearchResultLanguages = internal.SearchResultLanguages

type SearchOptions = internal.SearchOptions

// llu:TrKeysSuffix search.
var CodeSearchOptions = []string{"exact", "union", "fuzzy"}

type SearchMode = internal.CodeSearchMode

const (
	SearchModeExact = internal.CodeSearchModeExact
	SearchModeUnion = internal.CodeSearchModeUnion
	SearchModeFuzzy = internal.CodeSearchModeFuzzy
	SearchModeRegex = internal.CodeSearchModeRegex
)

// ErrInvalidQuery is wrapped by the errors of a query that can't be searched for.
var ErrInvalidQuery = internal.ErrInvalidQuery

// SupportsRegex tells if the indexer in use can search by regular expression.
func SupportsRegex() bool {
	r, ok := (*globalIndexer.Load()).(internal.RegexSearcher)
	return ok && r.SupportsRegex()
}

type Results []*Result

// Get the set of repo IDs from a list of search results
func (res Results) RepoIDs() []int64 {
	ids := make([]int64, len(res))
	for _, r := range res {
		if !slices.Contains(ids, r.RepoID) {
			ids = append(ids, r.RepoID)
		}
	}
	return ids
}

func indices(content string, selectionStartIndex, selectionEndIndex int) (int, int) {
	startIndex := selectionStartIndex
	numLinesBefore := 0
	for ; startIndex > 0; startIndex-- {
		if content[startIndex-1] == '\n' {
			if numLinesBefore == 1 {
				break
			}
			numLinesBefore++
		}
	}

	endIndex := selectionEndIndex
	numLinesAfter := 0
	for ; endIndex < len(content); endIndex++ {
		if content[endIndex] == '\n' {
			if numLinesAfter == 1 {
				break
			}
			numLinesAfter++
		}
	}

	return startIndex, endIndex
}

func searchResult(result *internal.SearchResult, startIndex, endIndex int) (*Result, error) {
	formatter := (*globalIndexer.Load()).Formatter()
	if formatter != nil {
		return formatter.Format(result)
	}
	return searchResultCommon(result, startIndex, endIndex)
}

func searchResultCommon(result *internal.SearchResult, startIndex, endIndex int) (*Result, error) {
	startLineNum := 1 + strings.Count(result.Content[:startIndex], "\n")

	var formattedLinesBuffer bytes.Buffer

	contentLines := strings.SplitAfter(result.Content[startIndex:endIndex], "\n")
	lineNums := make([]int, 0, len(contentLines))
	index := startIndex
	var highlightRanges [][3]int
	for i, line := range contentLines {
		var err error
		if index < result.EndIndex &&
			result.StartIndex < index+len(line) &&
			result.StartIndex < result.EndIndex {
			openActiveIndex := max(result.StartIndex-index, 0)
			closeActiveIndex := min(result.EndIndex-index, len(line))
			highlightRanges = append(highlightRanges, [3]int{i, openActiveIndex, closeActiveIndex})
			err = internal.WriteStrings(&formattedLinesBuffer,
				line[:openActiveIndex],
				line[openActiveIndex:closeActiveIndex],
				line[closeActiveIndex:],
			)
		} else {
			err = internal.WriteStrings(&formattedLinesBuffer, line)
		}
		if err != nil {
			return nil, err
		}

		lineNums = append(lineNums, startLineNum+i)
		index += len(line)
	}

	return &Result{
		RepoID:      result.RepoID,
		Branch:      result.Branch,
		Filename:    result.Filename,
		CommitID:    result.CommitID,
		UpdatedUnix: result.UpdatedUnix,
		Language:    result.Language,
		Color:       result.Color,
		Lines:       internal.HighlightSearchResultCode(result.Filename, lineNums, highlightRanges, formattedLinesBuffer.String()),
	}, nil
}

// PerformSearch perform a search on a repository
func PerformSearch(ctx context.Context, opts *SearchOptions) (int, Results, []*SearchResultLanguages, error) {
	if opts == nil || len(opts.Keyword) == 0 {
		return 0, nil, nil, nil
	}
	if len(opts.Keyword) > internal.MaxKeywordLength || len(opts.Filename) > internal.MaxKeywordLength {
		return 0, nil, nil, fmt.Errorf("%w: longer than %d characters", internal.ErrInvalidQuery, internal.MaxKeywordLength)
	}
	if opts.Mode == SearchModeRegex && !SupportsRegex() {
		return 0, nil, nil, internal.ErrRegexNotSupported
	}
	if opts.Paginator != nil {
		skip, take := opts.GetSkipTake()
		if skip >= internal.MaxSearchWindow {
			return 0, nil, nil, nil
		}
		if skip+take > internal.MaxSearchWindow {
			opts.Paginator = db.NewAbsoluteListOptions(skip, internal.MaxSearchWindow-skip)
		}
	}

	total, results, resultLanguages, err := (*globalIndexer.Load()).Search(ctx, opts)
	if err != nil {
		return 0, nil, nil, err
	}

	displayResults := make([]*Result, len(results))

	for i, result := range results {
		startIndex, endIndex := indices(result.Content, result.StartIndex, result.EndIndex)
		displayResults[i], err = searchResult(result, startIndex, endIndex)
		if err != nil {
			return 0, nil, nil, err
		}
	}
	return int(total), displayResults, resultLanguages, nil
}
