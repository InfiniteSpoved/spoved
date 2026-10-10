// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build unix && !openbsd

package zoekt

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/analyze"
	"forgejo.org/modules/charset"
	"forgejo.org/modules/git"
	"forgejo.org/modules/gitrepo"
	"forgejo.org/modules/indexer/code/internal"
	indexer_internal "forgejo.org/modules/indexer/internal"
	inner_zoekt "forgejo.org/modules/indexer/internal/zoekt"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/typesniffer"

	"github.com/go-enry/go-enry/v2"
	"github.com/sourcegraph/zoekt"
	"github.com/sourcegraph/zoekt/index"
	"github.com/sourcegraph/zoekt/query"
)

var errSkipIndexing = errors.New("skip indexing")

const repoIndexerLatestVersion = 1

type zoektFormatter struct{}

type Indexer struct {
	indexer_internal.Indexer // do not composite inner_zoekt.Indexer directly to avoid exposing too much
	inner                    *inner_zoekt.Indexer
	indexDir                 string
}

func NewIndexer(indexDir string) *Indexer {
	idxer := inner_zoekt.NewIndexer(indexDir, repoIndexerLatestVersion)
	return &Indexer{
		Indexer:  idxer,
		inner:    idxer,
		indexDir: indexDir,
	}
}

func zoektOptions(indexDir string, repoID int64, branches []zoekt.RepositoryBranch, isDelta bool) index.Options {
	return index.Options{
		IndexDir: indexDir,
		SizeMax:  int(setting.Indexer.MaxIndexerFileSize),
		ShardMax: 512 * 1024 * 1024, // 512 MB max shard
		IsDelta:  isDelta,
		RepositoryDescription: zoekt.Repository{
			ID:       uint32(repoID),
			Name:     strconv.FormatInt(repoID, 10),
			Branches: branches,
		},
	}
}

func newZoektIndexBuilder(indexDir string, repo *repo_model.Repository, targetSHA string) (*index.Builder, error) {
	opts := zoektOptions(indexDir, repo.ID, []zoekt.RepositoryBranch{{Name: "HEAD", Version: targetSHA}}, true)

	if opts.IncrementalSkipIndexing() {
		return nil, errSkipIndexing
	}

	opts.SetDefaults()

	builder, err := index.NewBuilder(opts)
	if err != nil {
		return nil, fmt.Errorf("index.newZoektIndexBuilder: %w", err)
	}

	return builder, nil
}

func (b *Indexer) addDelete(builder *index.Builder, filename string) {
	builder.MarkFileAsChangedOrRemoved(filename)
}

func (b *Indexer) addUpdate(ctx context.Context, builder *index.Builder, batchWriter io.Writer, batchReader *bufio.Reader, update internal.FileUpdate, repo *repo_model.Repository, branches []string) error {
	// Ignore vendored files in code search
	if setting.Indexer.ExcludeVendored && analyze.IsVendor(update.Filename) {
		return nil
	}

	size := update.Size
	var err error
	if !update.Sized {
		var stdout string
		stdout, _, err = git.NewCommand(ctx, "cat-file", "-s").AddDynamicArguments(update.BlobSha).RunStdString(&git.RunOpts{Dir: repo.RepoPath()})
		if err != nil {
			return err
		}
		if size, err = strconv.ParseInt(strings.TrimSpace(stdout), 10, 64); err != nil {
			return fmt.Errorf("misformatted git cat-file output: %w", err)
		}
	}
	if size > setting.Indexer.MaxIndexerFileSize {
		b.addDelete(builder, update.Filename)
		return nil
	}

	if _, err := batchWriter.Write([]byte(update.BlobSha + "\n")); err != nil {
		return err
	}

	_, _, size, err = git.ReadBatchLine(batchReader)
	if err != nil {
		return err
	}

	fileContents, err := io.ReadAll(io.LimitReader(batchReader, size))
	if err != nil {
		return err
	} else if !typesniffer.DetectContentType(fileContents, update.Filename).IsText() {
		// FIXME: UTF-16 files will probably fail here
		return nil
	}

	if _, err = batchReader.Discard(1); err != nil {
		return err
	}

	builder.MarkFileAsChangedOrRemoved(update.Filename)

	err = builder.Add(
		index.Document{
			Name:     update.Filename,
			Content:  charset.ToUTF8DropErrors(fileContents, charset.ConvertOpts{}),
			Branches: branches,
			Language: detectLanguage(update.Filename, fileContents),
		},
	)
	if err != nil {
		return fmt.Errorf("error adding document with name %s: %w", update.Filename, err)
	}

	return nil
}

func detectLanguage(filename string, content []byte) string {
	lang := enry.GetLanguage(filename, content)
	return normalizeLanguage(lang)
}

// Index will save the index data
func (b *Indexer) Index(ctx context.Context, repo *repo_model.Repository, branch, sha string, isDefault bool, changes *internal.RepoChanges) error {
	// the delta index is for repos that only have the default branch, see IndexBranches for the others
	builder, err := newZoektIndexBuilder(b.indexDir, repo, sha)
	if errors.Is(err, errSkipIndexing) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("error creating builder: %w", err)
	}

	if len(changes.Updates) > 0 {
		r, err := gitrepo.OpenRepository(ctx, repo)
		if err != nil {
			return err
		}
		defer r.Close()

		if err := r.WithCatFileBatch(ctx, func(w io.Writer, r *bufio.Reader) error {
			for _, update := range changes.Updates {
				err := b.addUpdate(ctx, builder, w, r, update, repo, []string{"HEAD"})
				if err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}

	for _, filename := range changes.RemovedFilenames {
		b.addDelete(builder, filename)
	}

	return builder.Finish()
}

// DeleteBranch does nothing: IndexBranches rebuilds the repo without the branch.
func (b *Indexer) DeleteBranch(_ context.Context, _ int64, _ string) error {
	return nil
}

// SupportsRegex implements internal.RegexSearcher.
func (b *Indexer) SupportsRegex() bool {
	return true
}

type branchDoc struct {
	update   internal.FileUpdate
	branches []string
}

// groupBranchFiles lists each file once per (path, blob) with the branches it is on, so identical
// files on several branches are a single document. The default branch is HEAD.
func groupBranchFiles(branches []internal.BranchFiles, defaultBranch string) []*branchDoc {
	var docs []*branchDoc
	byKey := map[[2]string]*branchDoc{}
	for _, br := range branches {
		name := br.Name
		if name == defaultBranch {
			name = "HEAD"
		}
		for _, f := range br.Files {
			key := [2]string{f.Filename, f.BlobSha}
			doc, ok := byKey[key]
			if !ok {
				doc = &branchDoc{update: f}
				byKey[key] = doc
				docs = append(docs, doc)
			}
			doc.branches = append(doc.branches, name)
		}
	}
	return docs
}

// IndexBranches implements internal.BranchIndexer: a full, non-delta build, which replaces the
// shards of the repo, with the files of every branch.
func (b *Indexer) IndexBranches(ctx context.Context, repo *repo_model.Repository, defaultBranch string, branches []internal.BranchFiles) error {
	description := make([]zoekt.RepositoryBranch, 0, len(branches))
	for _, br := range branches {
		name := br.Name
		if name == defaultBranch {
			name = "HEAD"
		}
		description = append(description, zoekt.RepositoryBranch{Name: name, Version: br.CommitID})
	}
	opts := zoektOptions(b.indexDir, repo.ID, description, false)
	opts.SetDefaults()
	builder, err := index.NewBuilder(opts)
	if err != nil {
		return fmt.Errorf("index.NewBuilder: %w", err)
	}

	r, err := gitrepo.OpenRepository(ctx, repo)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := r.WithCatFileBatch(ctx, func(w io.Writer, r *bufio.Reader) error {
		for _, doc := range groupBranchFiles(branches, defaultBranch) {
			if err := b.addUpdate(ctx, builder, w, r, doc.update, repo, doc.branches); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return builder.Finish()
}

func (b *Indexer) Delete(ctx context.Context, repoID int64) error {
	prefix := strconv.FormatInt(repoID, 10) + "_v"

	dir, err := os.Open(b.indexDir)
	if err != nil {
		return fmt.Errorf("open index dir: %w", err)
	}
	defer dir.Close()

	names, err := dir.Readdirnames(-1)
	if err != nil {
		return fmt.Errorf("read dir: %w", err)
	}

	prefixLen := len(prefix)

	for _, name := range names {
		if len(name) < prefixLen {
			continue
		}
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if strings.HasSuffix(name, ".tmp") || strings.Contains(name, ".zoekt") {
			_ = os.Remove(filepath.Join(b.indexDir, name))
		}
	}

	return nil
}

func TransToZoektContentQueryString(s string) string {
	return fmt.Sprintf("content:\"%s\"", s)
}

// generateZoektQuery creates a Zoekt query object based on search options
func (b *Indexer) generateZoektQuery(opts *internal.SearchOptions) (query.Q, error) {
	keyword := opts.Keyword
	var contentQuery query.Q
	var err error

	if len(keyword) > internal.MaxKeywordLength {
		return nil, fmt.Errorf("%w: longer than %d characters", internal.ErrInvalidQuery, internal.MaxKeywordLength)
	}

	// Zoekt does not support true fuzzy search.
	// CodeSearchModeFuzzy is therefore treated as a union (OR) search
	// to preserve previous behavior.
	switch opts.Mode {
	case internal.CodeSearchModeRegex:
		// built directly: the keyword never goes through the query parser
		re, err := syntax.Parse(keyword, syntax.Perl)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", internal.ErrInvalidQuery, err)
		}
		contentQuery = &query.Regexp{Regexp: re, Content: true}
	case internal.CodeSearchModeUnion, internal.CodeSearchModeFuzzy:
		fields := strings.Fields(keyword)
		if len(fields) == 0 {
			return nil, errors.New("empty keyword")
		}
		contentQuery, err = query.Parse(
			TransToZoektContentQueryString(regexp.QuoteMeta(fields[0])),
		)
		if err != nil {
			return nil, err
		}
		for _, f := range fields[1:] {
			q, err := query.Parse(
				TransToZoektContentQueryString(regexp.QuoteMeta(f)),
			)
			if err != nil {
				return nil, err
			}
			contentQuery = query.NewOr(contentQuery, q)
		}
	default:
		// Exact match
		contentQuery, err = query.Parse(
			TransToZoektContentQueryString(regexp.QuoteMeta(keyword)),
		)
		if err != nil {
			return nil, err
		}
	}

	finalQuery := contentQuery

	if len(opts.RepoIDs) > 0 {
		repoIDs := make([]uint32, len(opts.RepoIDs))
		for i, id := range opts.RepoIDs {
			repoIDs[i] = uint32(id)
		}
		finalQuery = query.NewAnd(finalQuery, query.NewRepoIDs(repoIDs...))
	}

	switch {
	case opts.AllBranches:
	case opts.Branch != "":
		finalQuery = query.NewAnd(finalQuery, &query.Branch{Pattern: opts.Branch, Exact: true})
	default:
		finalQuery = query.NewAnd(finalQuery, &query.Branch{Pattern: "HEAD", Exact: true})
	}

	if opts.Filename != "" {
		if len(opts.Filename) > internal.MaxKeywordLength {
			return nil, fmt.Errorf("%w: path longer than %d characters", internal.ErrInvalidQuery, internal.MaxKeywordLength)
		}
		prefix := strings.TrimPrefix(opts.Filename, "/")

		re, err := syntax.Parse(
			"^"+regexp.QuoteMeta(prefix),
			syntax.Perl,
		)
		if err != nil {
			return nil, err
		}

		fileQuery := &query.Regexp{
			Regexp:   re,
			FileName: true,
			Content:  false,
		}

		finalQuery = query.NewAnd(finalQuery, fileQuery)
	}

	if opts.Language != "" {
		lang := opts.Language
		if lang == "Plain Text" {
			lang = ""
		}
		finalQuery = query.NewAnd(finalQuery, &query.Language{Language: lang})
	}

	return finalQuery, nil
}

// paginateResults returns a slice of results starting from `skip` index up to `take` number of items.
func paginateResults[T any](results []T, skip, take int) []T {
	if skip >= len(results) {
		return nil
	}
	end := min(skip+take, len(results))
	return results[skip:end]
}

func getSearchResultLanguages(searchResult *zoekt.SearchResult) []*internal.SearchResultLanguages {
	languages := make(map[string]int)

	for _, file := range searchResult.Files {
		lang := normalizeLanguage(file.Language)
		languages[lang]++
	}

	searchResultLanguages := make([]*internal.SearchResultLanguages, 0, len(languages))

	for lang, count := range languages {
		searchResultLanguages = append(searchResultLanguages, &internal.SearchResultLanguages{
			Language: lang,
			Count:    count,
			Color:    enry.GetColor(lang),
		})
	}

	slices.SortFunc(searchResultLanguages, func(a, b *internal.SearchResultLanguages) int {
		if a.Count != b.Count {
			return cmp.Compare(b.Count, a.Count)
		}
		return cmp.Compare(a.Language, b.Language)
	})

	return searchResultLanguages
}

// Limits of what one hit keeps of a file, whatever the file holds.
const (
	maxMatchesPerFile     = 100
	maxChunkBytes         = 4 << 10
	maxResultContentBytes = 32 << 10
)

// convertZoektResult builds the hits from chunk matches: Content is the kept chunks joined,
// LineNumbers the file line number of each of its lines, and Matches point into Content.
func convertZoektResult(files []zoekt.FileMatch) []*internal.SearchResult {
	results := make([]*internal.SearchResult, 0, len(files))

	for _, f := range files {
		var (
			content      []byte
			lineNumbers  []int
			lineOffsets  []int
			matches      []internal.Match
			lastLine     = 0
			chunks       = slices.Clone(f.ChunkMatches)
			contentStart = func(c zoekt.ChunkMatch) uint32 { return c.ContentStart.ByteOffset }
		)
		slices.SortStableFunc(chunks, func(a, b zoekt.ChunkMatch) int { return cmp.Compare(contentStart(a), contentStart(b)) })

		for _, cm := range chunks {
			room := min(maxChunkBytes, maxResultContentBytes-len(content))
			startLine := int(cm.ContentStart.LineNumber)
			if len(matches) >= maxMatchesPerFile || room <= 0 {
				break
			}
			if cm.FileName || len(cm.Ranges) == 0 || startLine <= lastLine {
				continue // a chunk starting inside the previous one cannot be placed
			}

			chunk := cm.Content
			if len(chunk) > room { // cut at a line end, else (one huge line) at a rune start
				cut := room
				if nl := bytes.LastIndexByte(chunk[:cut], '\n'); nl >= 0 {
					cut = nl + 1
				} else {
					for cut > 0 && !utf8.RuneStart(chunk[cut]) {
						cut--
					}
				}
				chunk = chunk[:cut]
			}

			ranges := slices.Clone(cm.Ranges) // zoekt orders them by score
			slices.SortFunc(ranges, func(a, b zoekt.Range) int { return cmp.Compare(a.Start.ByteOffset, b.Start.ByteOffset) })

			base, first := len(content), len(matches)
			for _, r := range ranges {
				if len(matches) >= maxMatchesPerFile {
					break
				}
				if r.Start.ByteOffset < cm.ContentStart.ByteOffset || r.End.ByteOffset < r.Start.ByteOffset {
					continue
				}
				start := int(r.Start.ByteOffset - cm.ContentStart.ByteOffset)
				end := int(r.End.ByteOffset - cm.ContentStart.ByteOffset)
				if start >= len(chunk) || end > len(cm.Content) || (len(chunk) < len(cm.Content) && end > len(chunk)) {
					continue // past the cut
				}
				if nl := bytes.IndexByte(chunk[start:], '\n'); nl >= 0 {
					end = min(end, start+nl) // a range over several lines keeps its first line
				}
				matches = append(matches, internal.Match{
					Start:      base + start,
					End:        base + end,
					LineNumber: startLine + bytes.Count(chunk[:start], []byte{'\n'}),
				})
			}
			if len(matches) == first {
				continue
			}

			content = append(content, chunk...)
			offset := base
			for i, line := range bytes.SplitAfter(chunk, []byte{'\n'}) {
				if len(line) == 0 {
					break
				}
				lineNumbers = append(lineNumbers, startLine+i)
				lineOffsets = append(lineOffsets, offset)
				offset += len(line)
				lastLine = startLine + i
			}
		}

		if len(matches) == 0 {
			continue
		}

		lang := normalizeLanguage(f.Language)

		results = append(results, &internal.SearchResult{
			RepoID:      int64(f.RepositoryID),
			Branch:      firstBranch(f.Branches),
			Filename:    f.FileName,
			CommitID:    f.Version,
			Content:     string(content),
			Language:    lang,
			Color:       enry.GetColor(lang),
			Matches:     matches,
			LineNumbers: lineNumbers,
			LineOffsets: lineOffsets,
		})
	}

	return results
}

func firstBranch(branches []string) string {
	if len(branches) == 0 {
		return ""
	}
	return branches[0]
}

func normalizeLanguage(lang string) string {
	if lang == "" {
		return "Plain Text"
	}
	return lang
}

const (
	shardMaxMatchCount   = 100000
	totalMaxMatchCount   = 10000
	maxMatchDisplayCount = 10000
	regexTimeout         = 5 * time.Second
	// zoekt builds every match of a file before the display caps apply, so the searches that run at once are bounded.
	maxConcurrentSearches = 8
)

var (
	searchSlots     = make(chan struct{}, maxConcurrentSearches)
	searchQueueWait = 5 * time.Second
)

func (b *Indexer) Search(ctx context.Context, opts *internal.SearchOptions) (int64, []*internal.SearchResult, []*internal.SearchResultLanguages, error) {
	q, err := b.generateZoektQuery(opts)
	if err != nil {
		return 0, nil, nil, err
	}

	if opts.Mode == internal.CodeSearchModeRegex {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, regexTimeout)
		defer cancel()
	}

	wait := time.NewTimer(searchQueueWait)
	defer wait.Stop()
	select {
	case searchSlots <- struct{}{}:
		defer func() { <-searchSlots }()
	case <-wait.C:
		return 0, nil, nil, internal.ErrQueryTooExpensive
	case <-ctx.Done():
		return 0, nil, nil, ctx.Err()
	}

	skip, take := opts.GetSkipTake()
	result, err := b.inner.Searcher.Search(ctx, q, &zoekt.SearchOptions{
		ChunkMatches:         true, // matching lines with one context line, not the whole file
		NumContextLines:      1,
		ShardMaxMatchCount:   shardMaxMatchCount,
		TotalMaxMatchCount:   totalMaxMatchCount,
		MaxDocDisplayCount:   min(skip+take, internal.MaxSearchWindow),
		MaxMatchDisplayCount: maxMatchDisplayCount,
		MaxWallTime:          regexTimeout,
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return 0, nil, nil, internal.ErrQueryTooExpensive
		}
		return 0, nil, nil, err
	}

	pagedHits := convertZoektResult(paginateResults(result.Files, skip, take))
	if opts.Branch != "" && !opts.AllBranches {
		for _, hit := range pagedHits { // a file shared with another branch lists both
			hit.Branch = opts.Branch
		}
	}

	return int64(result.FileCount), pagedHits, getSearchResultLanguages(result), nil
}

func (b *Indexer) Formatter() internal.ResultFormatter {
	return &zoektFormatter{}
}

func (f *zoektFormatter) Format(r *internal.SearchResult) (*internal.Result, error) {
	// Sort matches by start position
	slices.SortFunc(r.Matches, func(a, b internal.Match) int { return a.Start - b.Start })

	// Precompute line offsets once to avoid repeated string slicing
	lineOffsets := []int{0} // starting index of the first line
	for i, c := range r.Content {
		if c == '\n' {
			lineOffsets = append(lineOffsets, i+1)
		}
	}
	lineOffsets = append(lineOffsets, len(r.Content)) // end offset for the last line

	// Content holds only the kept chunks: its i-th line is line r.LineNumbers[i] of the file
	totalLines := min(len(lineOffsets)-1, len(r.LineNumbers))

	// Collect the lines to display (+/- 1 line around each match) as indexes into Content
	sortedLines := make([]int, 0, len(r.Matches)*3)
	for _, m := range r.Matches {
		idx, ok := slices.BinarySearch(r.LineNumbers[:totalLines], m.LineNumber)
		if !ok {
			continue
		}
		for i := max(idx-1, 0); i <= min(idx+1, totalLines-1); i++ {
			if r.LineNumbers[i] >= m.LineNumber-1 && r.LineNumbers[i] <= m.LineNumber+1 { // not a far line of another chunk
				sortedLines = append(sortedLines, i)
			}
		}
	}

	// Sort lines and remove duplicates
	slices.Sort(sortedLines)
	sortedLines = slices.Compact(sortedLines)

	// Group lines into blocks (break block if the lines are not adjacent in the file)
	var blocks [][]int
	var currentBlock []int
	for _, line := range sortedLines {
		if len(currentBlock) > 0 {
			prev := currentBlock[len(currentBlock)-1]
			if line != prev+1 || r.LineNumbers[line] != r.LineNumbers[prev]+1 {
				blocks = append(blocks, currentBlock)
				currentBlock = nil
			}
		}
		currentBlock = append(currentBlock, line)
	}
	if len(currentBlock) > 0 {
		blocks = append(blocks, currentBlock)
	}

	var resultLines []internal.ResultLine

	// Iterate over blocks to generate ResultLines
	for _, block := range blocks {
		startIdx := block[0]
		endIdx := block[len(block)-1]

		// Slice block content directly from r.Content using precomputed offsets
		blockContent := r.Content[lineOffsets[startIdx]:lineOffsets[endIdx+1]]
		blockLines := make([]int, len(block))
		for i, idx := range block {
			blockLines[i] = r.LineNumbers[idx]
		}

		// Map for highlights per line within the block
		highlightByLine := make(map[int][][2]int)
		for _, match := range r.Matches {
			idx, ok := slices.BinarySearch(r.LineNumbers[:totalLines], match.LineNumber)
			if !ok || idx < startIdx || idx > endIdx {
				continue
			}
			highlightByLine[idx-startIdx] = append(highlightByLine[idx-startIdx], [2]int{match.Start - lineOffsets[idx], match.End - lineOffsets[idx]})
		}

		// Merge overlapping highlight ranges
		var highlightRanges [][3]int
		for lineIdx, ranges := range highlightByLine {
			if len(ranges) == 0 {
				continue
			}
			slices.SortFunc(ranges, func(a, b [2]int) int { return a[0] - b[0] })
			merged := make([][2]int, 0, len(ranges))
			current := ranges[0]
			for _, r := range ranges[1:] {
				if r[0] <= current[1] {
					if r[1] > current[1] {
						current[1] = r[1]
					}
				} else {
					merged = append(merged, current)
					current = r
				}
			}
			merged = append(merged, current)

			for _, r := range merged {
				highlightRanges = append(highlightRanges, [3]int{lineIdx, r[0], r[1]})
			}
		}

		// Generate the formatted lines with highlighting
		resultLines = append(resultLines, internal.HighlightSearchResultCode(
			r.Filename,
			blockLines,
			highlightRanges,
			blockContent,
		)...)
	}

	// Return the final search result
	return &internal.Result{
		RepoID:      r.RepoID,
		Branch:      r.Branch,
		Filename:    r.Filename,
		CommitID:    r.CommitID,
		UpdatedUnix: r.UpdatedUnix,
		Language:    r.Language,
		Color:       r.Color,
		Lines:       resultLines,
	}, nil
}
