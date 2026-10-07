// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package elasticsearch

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/analyze"
	"forgejo.org/modules/charset"
	"forgejo.org/modules/git"
	"forgejo.org/modules/gitrepo"
	"forgejo.org/modules/indexer/code/internal"
	indexer_internal "forgejo.org/modules/indexer/internal"
	inner_elasticsearch "forgejo.org/modules/indexer/internal/elasticsearch"
	"forgejo.org/modules/json"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/typesniffer"

	"github.com/go-enry/go-enry/v2"
	"github.com/olivere/elastic/v7"
)

const (
	esRepoIndexerLatestVersion = 3
	// multi-match-types, currently only 2 types are used
	// Reference: https://www.elastic.co/guide/en/elasticsearch/reference/7.0/query-dsl-multi-match-query.html#multi-match-types
	esMultiMatchTypeBestFields = "best_fields"
	esMultiMatchTypePhrase     = "phrase"
)

var _ internal.Indexer = &Indexer{}

// Indexer implements Indexer interface
type Indexer struct {
	inner                    *inner_elasticsearch.Indexer
	indexer_internal.Indexer // do not composite inner_elasticsearch.Indexer directly to avoid exposing too much
}

// NewIndexer creates a new elasticsearch indexer
func NewIndexer(url, indexerName string) *Indexer {
	inner := inner_elasticsearch.NewIndexer(url, indexerName, esRepoIndexerLatestVersion, defaultMapping)
	indexer := &Indexer{
		inner:   inner,
		Indexer: inner,
	}
	return indexer
}

const (
	defaultMapping = `{
		"settings": {
			"analysis": {
				"analyzer": {
					"custom_path_tree": {
						"tokenizer": "custom_hierarchy"
					},
					"code": {
						"tokenizer": "code_tokens",
						"filter": ["lowercase"]
					}
				},
				"tokenizer": {
					"code_tokens": {
						"type": "pattern",
						"pattern": "\\w+|[^\\w\\s]",
						"group": 0
					},
					"custom_hierarchy": {
						"type": "path_hierarchy",
						"delimiter": "/"
					}
				}
			}
		},
		"mappings": {
			"properties": {
				"repo_id": {
					"type": "long",
					"index": true
				},
				"content": {
					"type": "text",
					"term_vector": "with_positions_offsets",
					"index": true,
					"fields": {
						"code": {
							"type": "text",
							"analyzer": "code",
							"term_vector": "with_positions_offsets"
						}
					}
				},
				"branch": {
					"type": "keyword",
					"index": true
				},
				"is_default": {
					"type": "boolean"
				},
				"commit_id": {
					"type": "keyword",
					"index": true
				},
				"filename": {
					"type": "text",
					"fields": {
						"tree": {
							"type": "text",
							"analyzer": "custom_path_tree"
						}
					}
				},
				"language": {
					"type": "keyword",
					"index": true
				},
				"updated_at": {
					"type": "long",
					"index": true
				}
			}
		}
	}`
)

func (b *Indexer) addUpdate(ctx context.Context, batchWriter io.Writer, batchReader *bufio.Reader, branch, sha string, isDefault bool, update internal.FileUpdate, repo *repo_model.Repository) ([]elastic.BulkableRequest, error) {
	// Ignore vendored files in code search
	if setting.Indexer.ExcludeVendored && analyze.IsVendor(update.Filename) {
		return nil, nil
	}

	size := update.Size
	var err error
	if !update.Sized {
		var stdout string
		stdout, _, err = git.NewCommand(ctx, "cat-file", "-s").AddDynamicArguments(update.BlobSha).RunStdString(&git.RunOpts{Dir: repo.RepoPath()})
		if err != nil {
			return nil, err
		}
		if size, err = strconv.ParseInt(strings.TrimSpace(stdout), 10, 64); err != nil {
			return nil, fmt.Errorf("misformatted git cat-file output: %w", err)
		}
	}

	if size > setting.Indexer.MaxIndexerFileSize {
		return []elastic.BulkableRequest{b.addDelete(branch, update.Filename, repo)}, nil
	}

	if _, err := batchWriter.Write([]byte(update.BlobSha + "\n")); err != nil {
		return nil, err
	}

	_, _, size, err = git.ReadBatchLine(batchReader)
	if err != nil {
		return nil, err
	}

	fileContents, err := io.ReadAll(io.LimitReader(batchReader, size))
	if err != nil {
		return nil, err
	} else if !typesniffer.DetectContentType(fileContents, update.Filename).IsText() {
		// FIXME: UTF-16 files will probably fail here
		return nil, nil
	}

	if _, err = batchReader.Discard(1); err != nil {
		return nil, err
	}
	id := internal.FilenameIndexerID(repo.ID, branch, update.Filename)

	return []elastic.BulkableRequest{
		elastic.NewBulkIndexRequest().
			Index(b.inner.VersionedIndexName()).
			Id(id).
			Doc(map[string]any{
				"repo_id":    repo.ID,
				"branch":     branch,
				"is_default": isDefault,
				"content":    string(charset.ToUTF8DropErrors(fileContents, charset.ConvertOpts{})),
				"commit_id":  sha,
				"filename":   update.Filename,
				"language":   analyze.GetCodeLanguage(update.Filename, fileContents),
				"updated_at": timeutil.TimeStampNow(),
			}),
	}, nil
}

func (b *Indexer) addDelete(branch, filename string, repo *repo_model.Repository) elastic.BulkableRequest {
	id := internal.FilenameIndexerID(repo.ID, branch, filename)
	return elastic.NewBulkDeleteRequest().
		Index(b.inner.VersionedIndexName()).
		Id(id)
}

// Index will save the index data
func (b *Indexer) Index(ctx context.Context, repo *repo_model.Repository, branch, sha string, isDefault bool, changes *internal.RepoChanges) error {
	reqs := make([]elastic.BulkableRequest, 0)
	if len(changes.Updates) > 0 {
		r, err := gitrepo.OpenRepository(ctx, repo)
		if err != nil {
			return err
		}
		defer r.Close()

		if err := r.WithCatFileBatch(ctx, func(w io.Writer, r *bufio.Reader) error {
			for _, update := range changes.Updates {
				updateReqs, err := b.addUpdate(ctx, w, r, branch, sha, isDefault, update, repo)
				if err != nil {
					return err
				}
				if len(updateReqs) > 0 {
					reqs = append(reqs, updateReqs...)
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}

	for _, filename := range changes.RemovedFilenames {
		reqs = append(reqs, b.addDelete(branch, filename, repo))
	}

	return b.sendBulk(ctx, reqs)
}

// sendBulk sends the requests in batches. A refused document (e.g. an ID over 512 bytes) fails the
// whole call, so that the indexed commit isn't stored.
func (b *Indexer) sendBulk(ctx context.Context, reqs []elastic.BulkableRequest) error {
	const esBatchSize = 50
	for i := 0; i < len(reqs); i += esBatchSize {
		resp, err := b.inner.Client.Bulk().
			Index(b.inner.VersionedIndexName()).
			Add(reqs[i:min(i+esBatchSize, len(reqs))]...).
			Do(ctx)
		if err != nil {
			return err
		}
		if resp.Errors {
			for _, item := range resp.Failed() {
				return fmt.Errorf("elasticsearch refused document %q: %v", item.Id, item.Error)
			}
		}
	}
	return nil
}

// Delete entries by repoId
func (b *Indexer) Delete(ctx context.Context, repoID int64) error {
	return b.deleteWithRetry(ctx, repoID, "")
}

// DeleteBranch deletes the entries of a branch, found by their fields and never by an ID prefix
func (b *Indexer) DeleteBranch(ctx context.Context, repoID int64, branch string) error {
	return b.deleteWithRetry(ctx, repoID, branch)
}

func (b *Indexer) deleteWithRetry(ctx context.Context, repoID int64, branch string) error {
	if err := b.doDelete(ctx, repoID, branch); err != nil {
		// Maybe there is a conflict during the delete operation, so we should retry after a refresh
		log.Warn("Deletion of entries of repo %v within index %v was erroneous. Trying to refresh index before trying again", repoID, b.inner.VersionedIndexName(), err)
		if err := b.refreshIndex(ctx); err != nil {
			return err
		}
		if err := b.doDelete(ctx, repoID, branch); err != nil {
			log.Error("Could not delete entries of repo %v within index %v", repoID, b.inner.VersionedIndexName())
			return err
		}
	}
	return nil
}

func (b *Indexer) refreshIndex(ctx context.Context) error {
	if _, err := b.inner.Client.Refresh(b.inner.VersionedIndexName()).Do(ctx); err != nil {
		log.Error("Error while trying to refresh index %v", b.inner.VersionedIndexName(), err)
		return err
	}

	return nil
}

// Delete entries by repoId
func (b *Indexer) doDelete(ctx context.Context, repoID int64, branch string) error {
	query := elastic.NewBoolQuery().Filter(elastic.NewTermsQuery("repo_id", repoID))
	if branch != "" {
		query = query.Filter(elastic.NewTermQuery("branch", branch))
	}
	_, err := b.inner.Client.DeleteByQuery(b.inner.VersionedIndexName()).
		Query(query).
		Do(ctx)
	return err
}

// indexPos find words positions for start and the following end on content. It will
// return the beginning position of the first start and the ending position of the
// first end following the start string.
// If not found any of the positions, it will return -1, -1.
func indexPos(content, start, end string) (int, int) {
	startIdx := strings.Index(content, start)
	if startIdx < 0 {
		return -1, -1
	}
	endIdx := strings.Index(content[startIdx+len(start):], end)
	if endIdx < 0 {
		return -1, -1
	}
	return startIdx, startIdx + len(start) + endIdx + len(end)
}

func convertResult(searchResult *elastic.SearchResult, kw, hlField string, pageSize int) (int64, []*internal.SearchResult, []*internal.SearchResultLanguages, error) {
	hits := make([]*internal.SearchResult, 0, pageSize)
	for _, hit := range searchResult.Hits.Hits {
		// FIXME: There is no way to get the position the keyword on the content currently on the same request.
		// So we get it from content, this may made the query slower. See
		// https://discuss.elastic.co/t/fetching-position-of-keyword-in-matched-document/94291
		var startIndex, endIndex int
		c, ok := hit.Highlight[hlField]
		if ok && len(c) > 0 {
			// FIXME: Since the highlighting content will include <em> and </em> for the keywords,
			// now we should find the positions. But how to avoid html content which contains the
			// <em> and </em> tags? If elastic search has handled that?
			startIndex, endIndex = indexPos(c[0], "<em>", "</em>")
			if startIndex == -1 {
				panic(fmt.Sprintf("1===%s,,,%#v,,,%s", kw, hit.Highlight, c[0]))
			}
		} else {
			panic(fmt.Sprintf("2===%#v", hit.Highlight))
		}

		res := make(map[string]any)
		if err := json.Unmarshal(hit.Source, &res); err != nil {
			return 0, nil, nil, err
		}

		language := res["language"].(string)

		hits = append(hits, &internal.SearchResult{
			RepoID:      int64(res["repo_id"].(float64)),
			Branch:      res["branch"].(string),
			Filename:    res["filename"].(string),
			CommitID:    res["commit_id"].(string),
			Content:     res["content"].(string),
			UpdatedUnix: timeutil.TimeStamp(res["updated_at"].(float64)),
			Language:    language,
			StartIndex:  startIndex,
			EndIndex:    endIndex - 9, // remove the length <em></em> since we give Content the original data
			Color:       enry.GetColor(language),
		})
	}

	return searchResult.TotalHits(), hits, extractAggs(searchResult), nil
}

func extractAggs(searchResult *elastic.SearchResult) []*internal.SearchResultLanguages {
	var searchResultLanguages []*internal.SearchResultLanguages
	agg, found := searchResult.Aggregations.Terms("language")
	if found {
		searchResultLanguages = make([]*internal.SearchResultLanguages, 0, 10)

		for _, bucket := range agg.Buckets {
			searchResultLanguages = append(searchResultLanguages, &internal.SearchResultLanguages{
				Language: bucket.Key.(string),
				Color:    enry.GetColor(bucket.Key.(string)),
				Count:    int(bucket.DocCount),
			})
		}
	}
	return searchResultLanguages
}

// Search searches for codes and language stats by given conditions.
func (b *Indexer) Search(ctx context.Context, opts *internal.SearchOptions) (int64, []*internal.SearchResult, []*internal.SearchResultLanguages, error) {
	searchType := esMultiMatchTypePhrase
	if opts.Mode == internal.CodeSearchModeUnion || opts.Mode == internal.CodeSearchModeFuzzy {
		searchType = esMultiMatchTypeBestFields
	}

	// Keywords with punctuation are matched as a phrase of words and single
	// punctuation characters, the others like before (camel case aware).
	contentField := "content"
	if opts.Mode == internal.CodeSearchModeExact && internal.HasPunctuation(opts.Keyword) {
		contentField = "content.code"
	}

	kwQuery := elastic.NewMultiMatchQuery(opts.Keyword, contentField).Type(searchType)
	if opts.Mode == internal.CodeSearchModeFuzzy {
		kwQuery = kwQuery.Fuzziness("AUTO")
	}
	query := elastic.NewBoolQuery()
	query = query.Must(kwQuery)
	if len(opts.RepoIDs) > 0 {
		repoStrs := make([]any, 0, len(opts.RepoIDs))
		for _, repoID := range opts.RepoIDs {
			repoStrs = append(repoStrs, repoID)
		}
		repoQuery := elastic.NewTermsQuery("repo_id", repoStrs...)
		query = query.Must(repoQuery)
	}
	switch {
	case opts.AllBranches:
	case opts.Branch != "":
		query = query.Filter(elastic.NewTermQuery("branch", opts.Branch))
	default:
		query = query.Filter(elastic.NewTermQuery("is_default", true))
	}
	if len(opts.Filename) > 0 {
		query = query.Filter(elastic.NewTermsQuery("filename.tree", opts.Filename))
	}

	var (
		start, pageSize = opts.GetSkipTake()
		kw              = "<em>" + opts.Keyword + "</em>"
		aggregation     = elastic.NewTermsAggregation().Field("language").Size(10).OrderByCountDesc()
	)

	if len(opts.Language) == 0 {
		searchResult, err := b.inner.Client.Search().
			Index(b.inner.VersionedIndexName()).
			Aggregation("language", aggregation).
			Query(query).
			Highlight(
				elastic.NewHighlight().
					Field(contentField).
					NumOfFragments(0). // return all highting content on fragments
					HighlighterType("fvh"),
			).
			Sort("_score", false).
			Sort("updated_at", true).
			From(start).Size(pageSize).
			Do(ctx)
		if err != nil {
			return 0, nil, nil, err
		}

		return convertResult(searchResult, kw, contentField, pageSize)
	}

	langQuery := elastic.NewMatchQuery("language", opts.Language)
	countResult, err := b.inner.Client.Search().
		Index(b.inner.VersionedIndexName()).
		Aggregation("language", aggregation).
		Query(query).
		Size(0). // We only need stats information
		Do(ctx)
	if err != nil {
		return 0, nil, nil, err
	}

	query = query.Must(langQuery)
	searchResult, err := b.inner.Client.Search().
		Index(b.inner.VersionedIndexName()).
		Query(query).
		Highlight(
			elastic.NewHighlight().
				Field(contentField).
				NumOfFragments(0). // return all highting content on fragments
				HighlighterType("fvh"),
		).
		Sort("_score", false).
		Sort("updated_at", true).
		From(start).Size(pageSize).
		Do(ctx)
	if err != nil {
		return 0, nil, nil, err
	}

	total, hits, _, err := convertResult(searchResult, kw, contentField, pageSize)

	return total, hits, extractAggs(countResult), err
}

func (b *Indexer) Formatter() internal.ResultFormatter {
	return nil
}
