// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package elasticsearch

import (
	"net/http"
	"net/http/httptest"
	"testing"

	inner_elasticsearch "forgejo.org/modules/indexer/internal/elasticsearch"

	"github.com/olivere/elastic/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIndexPos(t *testing.T) {
	startIdx, endIdx := indexPos("test index start and end", "start", "end")
	assert.Equal(t, 11, startIdx)
	assert.Equal(t, 24, endIdx)
}

// A document that Elasticsearch refuses (e.g. an ID over 512 bytes) fails the bulk request,
// so the indexed commit is not stored.
func TestSendBulkReportsFailedItems(t *testing.T) {
	respond := func(body string) *Indexer {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(ts.Close)
		client, err := elastic.NewClient(elastic.SetURL(ts.URL), elastic.SetSniff(false), elastic.SetHealthcheck(false))
		require.NoError(t, err)
		inner := inner_elasticsearch.NewIndexer(ts.URL, "test", esRepoIndexerLatestVersion, defaultMapping)
		inner.Client = client
		return &Indexer{inner: inner, Indexer: inner}
	}
	reqs := []elastic.BulkableRequest{elastic.NewBulkDeleteRequest().Index("test").Id("a"), elastic.NewBulkDeleteRequest().Index("test").Id("b")}

	ok := respond(`{"took":1,"errors":false,"items":[{"delete":{"_id":"a","status":200}},{"delete":{"_id":"b","status":200}}]}`)
	require.NoError(t, ok.sendBulk(t.Context(), reqs))

	failed := respond(`{"took":1,"errors":true,"items":[{"delete":{"_id":"a","status":200}},{"delete":{"_id":"b","status":400,"error":{"type":"illegal_argument_exception","reason":"id is too long"}}}]}`)
	err := failed.sendBulk(t.Context(), reqs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "id is too long")
}
