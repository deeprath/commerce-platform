// Package index owns the OpenSearch "products" index: its mapping, and
// upsert/delete of product documents. The search service is the only writer,
// and it writes only from Kafka events.
//
// It talks to OpenSearch over raw HTTP through the transport client so the
// code doesn't depend on a particular opensearch-go high-level API surface.
package index

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/opensearch-project/opensearch-go/v4"

	catalogv1 "github.com/deeprath/commerce-platform/gen/go/commerce/catalog/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
)

const IndexName = "products"

// Doc is the shape stored in OpenSearch.
type Doc struct {
	ProductID       string            `json:"product_id"`
	Slug            string            `json:"slug"`
	Title           string            `json:"title"`
	Description     string            `json:"description"`
	CategoryID      string            `json:"category_id"`
	PriceUnits      int64             `json:"price_units"`
	PriceNanos      int32             `json:"price_nanos"`
	Currency        string            `json:"currency"`
	PrimaryMediaKey string            `json:"primary_media_key"`
	Attributes      map[string]string `json:"attributes"`
	IndexedAt       time.Time         `json:"indexed_at"`
}

type Client struct{ os *opensearch.Client }

func New(addresses []string) (*Client, error) {
	c, err := opensearch.NewClient(opensearch.Config{Addresses: addresses})
	if err != nil {
		return nil, errs.Wrap(err, errs.KindInternal, "OS_INIT", "cannot init opensearch client")
	}
	return &Client{os: c}, nil
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, path, r)
	if err != nil {
		return nil, errs.Wrap(err, errs.KindInternal, "OS_REQ", "cannot build request")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.os.Perform(req)
	if err != nil {
		return nil, errs.Wrap(err, errs.KindUnavailable, "OS_UNREACHABLE", "opensearch request failed")
	}
	return resp, nil
}

// EnsureIndex creates the index with the mapping if it does not exist.
func (c *Client) EnsureIndex(ctx context.Context) error {
	head, err := c.do(ctx, http.MethodHead, "/"+IndexName, nil)
	if err != nil {
		return err
	}
	_ = head.Body.Close()
	if head.StatusCode == http.StatusOK {
		return nil
	}
	resp, err := c.do(ctx, http.MethodPut, "/"+IndexName, []byte(mappingJSON))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return errs.New(errs.KindInternal, "OS_CREATE_FAILED", "create index: "+string(b))
	}
	return nil
}

// UpsertFromEvent indexes an ACTIVE product, or deletes the doc for any other status.
func (c *Client) UpsertFromEvent(ctx context.Context, evt *catalogv1.ProductChanged) error {
	p := evt.GetProduct()
	if p == nil || p.GetStatus() != catalogv1.ProductStatus_PRODUCT_STATUS_ACTIVE {
		return c.Delete(ctx, evt.GetProductId())
	}
	d := Doc{
		ProductID: p.GetId(), Slug: p.GetSlug(), Title: p.GetTitle(),
		Description: p.GetDescription(), CategoryID: p.GetCategoryId(),
		PriceUnits: p.GetListPrice().GetUnits(), PriceNanos: p.GetListPrice().GetNanos(),
		Currency:   p.GetListPrice().GetCurrencyCode(),
		Attributes: p.GetAttributes(), IndexedAt: time.Now().UTC(),
	}
	if mk := p.GetMediaKeys(); len(mk) > 0 {
		d.PrimaryMediaKey = mk[0]
	}
	buf, err := json.Marshal(d)
	if err != nil {
		return errs.Wrap(err, errs.KindInternal, "DOC_MARSHAL", "cannot marshal doc")
	}
	resp, err := c.do(ctx, http.MethodPut, "/"+IndexName+"/_doc/"+d.ProductID+"?refresh=wait_for", buf)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return errs.New(errs.KindInternal, "OS_INDEX_ERR", "index: "+string(b))
	}
	return nil
}

// Delete removes a doc; a 404 is not an error (idempotent).
func (c *Client) Delete(ctx context.Context, productID string) error {
	resp, err := c.do(ctx, http.MethodDelete, "/"+IndexName+"/_doc/"+productID+"?refresh=wait_for", nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		b, _ := io.ReadAll(resp.Body)
		return errs.New(errs.KindInternal, "OS_DELETE_ERR", "delete: "+string(b))
	}
	return nil
}

// RawSearch runs a query body and returns the decoded response.
func (c *Client) RawSearch(ctx context.Context, body []byte) (*SearchResult, error) {
	resp, err := c.do(ctx, http.MethodPost, "/"+IndexName+"/_search", body)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, errs.New(errs.KindInternal, "OS_SEARCH_ERR", "search: "+string(b))
	}
	var out SearchResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, errs.Wrap(err, errs.KindInternal, "OS_DECODE", "cannot decode search response")
	}
	return &out, nil
}

// SearchResult is the slice of the OpenSearch response the service needs.
type SearchResult struct {
	Hits         hitsSection            `json:"hits"`
	Aggregations map[string]aggregation `json:"aggregations"`
}

type hitsSection struct {
	Total totalCount `json:"total"`
	Hits  []hit      `json:"hits"`
}

type totalCount struct {
	Value int64 `json:"value"`
}

type hit struct {
	ID     string  `json:"_id"`
	Score  float64 `json:"_score"`
	Source Doc     `json:"_source"`
}

type aggregation struct {
	Buckets []bucket `json:"buckets"`
}

type bucket struct {
	Key      any   `json:"key"`
	DocCount int64 `json:"doc_count"`
}

const mappingJSON = `{
  "settings": { "number_of_shards": 1, "number_of_replicas": 0 },
  "mappings": {
    "properties": {
      "product_id":        { "type": "keyword" },
      "slug":              { "type": "keyword" },
      "title":             { "type": "text", "fields": { "raw": { "type": "keyword" }, "suggest": { "type": "search_as_you_type" } } },
      "description":       { "type": "text" },
      "category_id":       { "type": "keyword" },
      "price_units":       { "type": "long" },
      "price_nanos":       { "type": "integer" },
      "currency":          { "type": "keyword" },
      "primary_media_key": { "type": "keyword" },
      "attributes":        { "type": "flat_object" },
      "indexed_at":        { "type": "date" }
    }
  }
}`
