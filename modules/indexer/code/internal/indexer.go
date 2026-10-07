// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package internal

import (
	"context"
	"errors"
	"fmt"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/indexer/internal"
)

// Indexer defines an interface to index and search code contents
type Indexer interface {
	internal.Indexer
	Index(ctx context.Context, repo *repo_model.Repository, branch, sha string, isDefault bool, changes *RepoChanges) error
	Delete(ctx context.Context, repoID int64) error
	DeleteBranch(ctx context.Context, repoID int64, branch string) error
	Search(ctx context.Context, opts *SearchOptions) (int64, []*SearchResult, []*SearchResultLanguages, error)
	Formatter() ResultFormatter
}

type CodeSearchMode int

const (
	CodeSearchModeExact CodeSearchMode = iota
	CodeSearchModeUnion
	CodeSearchModeFuzzy
	CodeSearchModeRegex
)

const (
	// MaxKeywordLength caps the keyword and the path of a search, for every mode.
	MaxKeywordLength = 256
	// MaxSearchWindow caps skip+take of a search: results past it are not reachable.
	MaxSearchWindow = 1000
)

var (
	// ErrInvalidQuery is wrapped by the errors of a query that can't be searched for.
	ErrInvalidQuery = errors.New("invalid query")
	// ErrRegexNotSupported is returned for the regex mode on an indexer that has none.
	ErrRegexNotSupported = fmt.Errorf("%w: regex search is not supported by this indexer", ErrInvalidQuery)
	// ErrQueryTooExpensive is returned when a search runs into its time limit.
	ErrQueryTooExpensive = fmt.Errorf("%w: query too expensive", ErrInvalidQuery)
)

// RegexSearcher is implemented by indexers that can search by regular expression.
type RegexSearcher interface {
	SupportsRegex() bool
}

// BranchIndexer is implemented by indexers that index all branches of a repo in one go,
// instead of one Index call per branch. The default branch comes first.
type BranchIndexer interface {
	IndexBranches(ctx context.Context, repo *repo_model.Repository, defaultBranch string, branches []BranchFiles) error
}

func (mode CodeSearchMode) String() string {
	switch mode {
	case CodeSearchModeFuzzy:
		return "fuzzy"
	case CodeSearchModeUnion:
		return "union"
	case CodeSearchModeRegex:
		return "regexp"
	default:
		return "exact"
	}
}

type SearchOptions struct {
	RepoIDs  []int64
	Keyword  string
	Language string
	Filename string

	Mode CodeSearchMode

	// Branch limits the search to one indexed branch; empty is the default branch of each repo.
	Branch string
	// AllBranches searches every indexed branch, and ignores Branch.
	AllBranches bool

	db.Paginator
}

// NewDummyIndexer returns a dummy indexer
func NewDummyIndexer() Indexer {
	return &dummyIndexer{
		Indexer: internal.NewDummyIndexer(),
	}
}

type dummyIndexer struct {
	internal.Indexer
}

func (d *dummyIndexer) Index(ctx context.Context, repo *repo_model.Repository, branch, sha string, isDefault bool, changes *RepoChanges) error {
	return errors.New("indexer is not ready")
}

func (d *dummyIndexer) Delete(ctx context.Context, repoID int64) error {
	return errors.New("indexer is not ready")
}

func (d *dummyIndexer) DeleteBranch(ctx context.Context, repoID int64, branch string) error {
	return errors.New("indexer is not ready")
}

func (d *dummyIndexer) Search(ctx context.Context, opts *SearchOptions) (int64, []*SearchResult, []*SearchResultLanguages, error) {
	return 0, nil, nil, errors.New("indexer is not ready")
}

func (d *dummyIndexer) Formatter() ResultFormatter {
	return nil
}
