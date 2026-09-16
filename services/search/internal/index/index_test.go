package index_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	catalogv1 "github.com/deeprath/commerce-platform/gen/go/commerce/catalog/v1"
	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	"github.com/deeprath/commerce-platform/services/search/internal/index"
)

// seen is one request the client made to OpenSearch.
type seen struct {
	method string
	path   string
	query  string
	body   string
	ctype  string
}

// fakeOS stands in for OpenSearch, recording what it was asked to do. reply
// decides the status and body per request; nil means 200 with an empty object.
type fakeOS struct {
	mu    sync.Mutex
	reqs  []seen
	reply func(seen) (int, string)
}

func newFakeOS(t *testing.T, reply func(seen) (int, string)) (*fakeOS, *index.Client) {
	t.Helper()
	f := &fakeOS{reply: reply}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := seen{
			method: r.Method, path: r.URL.Path, query: r.URL.RawQuery,
			body: string(body), ctype: r.Header.Get("Content-Type"),
		}
		f.mu.Lock()
		f.reqs = append(f.reqs, s)
		f.mu.Unlock()

		status, out := 200, `{}`
		if f.reply != nil {
			status, out = f.reply(s)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(out))
	}))
	t.Cleanup(srv.Close)

	c, err := index.New([]string{srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return f, c
}

func (f *fakeOS) calls() []seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seen(nil), f.reqs...)
}

func (f *fakeOS) only(t *testing.T) seen {
	t.Helper()
	got := f.calls()
	if len(got) != 1 {
		t.Fatalf("made %d requests, want exactly one: %+v", len(got), got)
	}
	return got[0]
}

func activeProduct() *catalogv1.Product {
	return &catalogv1.Product{
		Id: "prod-1", Slug: "blue-mug", Title: "Blue Mug",
		Description: "A mug, in blue.", CategoryId: "cat-9",
		Status:     catalogv1.ProductStatus_PRODUCT_STATUS_ACTIVE,
		ListPrice:  &commonv1.Money{CurrencyCode: "USD", Units: 12, Nanos: 500000000},
		MediaKeys:  []string{"media/first.jpg", "media/second.jpg"},
		Attributes: map[string]string{"colour": "blue"},
	}
}

// An ACTIVE product is indexed with every field the storefront renders lifted
// off the event.
func TestUpsertFromEvent_IndexesAnActiveProduct(t *testing.T) {
	f, c := newFakeOS(t, nil)

	err := c.UpsertFromEvent(context.Background(), &catalogv1.ProductChanged{
		ProductId: "prod-1", Product: activeProduct(),
	})
	if err != nil {
		t.Fatalf("UpsertFromEvent: %v", err)
	}

	got := f.only(t)
	if got.method != http.MethodPut {
		t.Errorf("method = %s, want PUT", got.method)
	}
	if want := "/" + index.IndexName + "/_doc/prod-1"; got.path != want {
		t.Errorf("path = %s, want %s", got.path, want)
	}
	// The consumer's next read must see the write; without this the storefront
	// can show a product that search does not yet return.
	if got.query != "refresh=wait_for" {
		t.Errorf("query = %q, want refresh=wait_for", got.query)
	}
	if got.ctype != "application/json" {
		t.Errorf("Content-Type = %q", got.ctype)
	}

	var doc index.Doc
	if err := json.Unmarshal([]byte(got.body), &doc); err != nil {
		t.Fatalf("indexed body is not a Doc: %v", err)
	}
	for _, tc := range []struct{ field, got, want string }{
		{"ProductID", doc.ProductID, "prod-1"},
		{"Slug", doc.Slug, "blue-mug"},
		{"Title", doc.Title, "Blue Mug"},
		{"Description", doc.Description, "A mug, in blue."},
		{"CategoryID", doc.CategoryID, "cat-9"},
		{"Currency", doc.Currency, "USD"},
		// Only the first media key is stored: it is the thumbnail the results
		// grid shows, not the gallery.
		{"PrimaryMediaKey", doc.PrimaryMediaKey, "media/first.jpg"},
		{"Attributes[colour]", doc.Attributes["colour"], "blue"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}
	// Money is split across two fields; conflating them silently changes price.
	if doc.PriceUnits != 12 || doc.PriceNanos != 500000000 {
		t.Errorf("price = %d units %d nanos, want 12/500000000", doc.PriceUnits, doc.PriceNanos)
	}
	if doc.IndexedAt.IsZero() {
		t.Error("IndexedAt not stamped")
	}
}

// Anything that is not ACTIVE must leave the index, not sit in it stale. A
// product pulled from sale that stays searchable is the visible failure here.
func TestUpsertFromEvent_NonActiveStatusesAreRemoved(t *testing.T) {
	for _, status := range []catalogv1.ProductStatus{
		catalogv1.ProductStatus_PRODUCT_STATUS_UNSPECIFIED,
		catalogv1.ProductStatus_PRODUCT_STATUS_DRAFT,
		catalogv1.ProductStatus_PRODUCT_STATUS_ARCHIVED,
	} {
		t.Run(status.String(), func(t *testing.T) {
			f, c := newFakeOS(t, nil)
			p := activeProduct()
			p.Status = status

			if err := c.UpsertFromEvent(context.Background(),
				&catalogv1.ProductChanged{ProductId: "prod-1", Product: p}); err != nil {
				t.Fatalf("UpsertFromEvent: %v", err)
			}

			got := f.only(t)
			if got.method != http.MethodDelete {
				t.Fatalf("method = %s, want DELETE so the product leaves the index", got.method)
			}
			if want := "/" + index.IndexName + "/_doc/prod-1"; got.path != want {
				t.Errorf("path = %s, want %s", got.path, want)
			}
		})
	}
}

// change == DELETED carries no product snapshot, so the id has to come off the
// event itself.
func TestUpsertFromEvent_NilProductDeletesByEventID(t *testing.T) {
	f, c := newFakeOS(t, nil)

	if err := c.UpsertFromEvent(context.Background(), &catalogv1.ProductChanged{
		ProductId: "prod-1", Change: catalogv1.ChangeType_CHANGE_TYPE_DELETED,
	}); err != nil {
		t.Fatalf("UpsertFromEvent: %v", err)
	}

	got := f.only(t)
	if got.method != http.MethodDelete {
		t.Fatalf("method = %s, want DELETE", got.method)
	}
	if !strings.HasSuffix(got.path, "/prod-1") {
		t.Errorf("path = %s, want it to address prod-1", got.path)
	}
}

// The doc is written under product.id but removed under the event's product_id.
// They are different fields, so a doc indexed under one and deleted under the
// other would survive its own deletion and keep appearing in results.
func TestUpsertFromEvent_IndexAndDeleteAddressTheSameDoc(t *testing.T) {
	f, c := newFakeOS(t, nil)
	evt := &catalogv1.ProductChanged{ProductId: "prod-1", Product: activeProduct()}

	if err := c.UpsertFromEvent(context.Background(), evt); err != nil {
		t.Fatalf("index: %v", err)
	}
	evt.Product.Status = catalogv1.ProductStatus_PRODUCT_STATUS_ARCHIVED
	if err := c.UpsertFromEvent(context.Background(), evt); err != nil {
		t.Fatalf("archive: %v", err)
	}

	got := f.calls()
	if len(got) != 2 {
		t.Fatalf("made %d requests, want 2", len(got))
	}
	if got[0].path != got[1].path {
		t.Errorf("indexed at %s but deleted at %s; the doc would outlive its deletion",
			got[0].path, got[1].path)
	}
}

func TestUpsertFromEvent_MissingOptionalFields(t *testing.T) {
	f, c := newFakeOS(t, nil)

	// No price, no media, no attributes — all optional in the proto.
	if err := c.UpsertFromEvent(context.Background(), &catalogv1.ProductChanged{
		ProductId: "prod-2",
		Product: &catalogv1.Product{
			Id: "prod-2", Slug: "bare", Title: "Bare",
			Status: catalogv1.ProductStatus_PRODUCT_STATUS_ACTIVE,
		},
	}); err != nil {
		t.Fatalf("UpsertFromEvent: %v", err)
	}

	var doc index.Doc
	if err := json.Unmarshal([]byte(f.only(t).body), &doc); err != nil {
		t.Fatalf("body: %v", err)
	}
	if doc.PriceUnits != 0 || doc.PriceNanos != 0 || doc.Currency != "" {
		t.Errorf("absent price became %+v", doc)
	}
	if doc.PrimaryMediaKey != "" {
		t.Errorf("PrimaryMediaKey = %q, want empty with no media keys", doc.PrimaryMediaKey)
	}
}

func TestUpsertFromEvent_IndexRejectionIsAnError(t *testing.T) {
	_, c := newFakeOS(t, func(seen) (int, string) {
		return 400, `{"error":"mapper_parsing_exception"}`
	})

	err := c.UpsertFromEvent(context.Background(), &catalogv1.ProductChanged{
		ProductId: "prod-1", Product: activeProduct(),
	})
	if err == nil {
		t.Fatal("a rejected index write was reported as success")
	}
	// The consumer needs the reason in the log to act on it.
	if !strings.Contains(err.Error(), "mapper_parsing_exception") {
		t.Errorf("err = %v, want it to carry the reason OpenSearch gave", err)
	}
}

// Delivery is at-least-once, so the same delete arrives twice. The second one
// finds nothing, and that is success — treating it as an error would retry the
// record forever and eventually park a delete that already happened.
func TestDelete_NotFoundIsSuccess(t *testing.T) {
	_, c := newFakeOS(t, func(seen) (int, string) {
		return 404, `{"result":"not_found"}`
	})

	if err := c.Delete(context.Background(), "gone"); err != nil {
		t.Fatalf("a repeated delete failed: %v", err)
	}
}

func TestDelete_OtherFailuresSurface(t *testing.T) {
	for _, status := range []int{400, 409, 500, 503} {
		_, c := newFakeOS(t, func(seen) (int, string) { return status, `{"error":"nope"}` })
		if err := c.Delete(context.Background(), "prod-1"); err == nil {
			t.Errorf("status %d was treated as a successful delete", status)
		}
	}
}

func TestEnsureIndex_ExistingIndexIsLeftAlone(t *testing.T) {
	f, c := newFakeOS(t, func(s seen) (int, string) {
		if s.method == http.MethodHead {
			return 200, ""
		}
		return 200, `{}`
	})

	if err := c.EnsureIndex(context.Background()); err != nil {
		t.Fatalf("EnsureIndex: %v", err)
	}
	got := f.only(t)
	if got.method != http.MethodHead {
		t.Fatalf("made a %s request; an existing index must not be rewritten", got.method)
	}
}

func TestEnsureIndex_CreatesWithTheMappingWhenAbsent(t *testing.T) {
	f, c := newFakeOS(t, func(s seen) (int, string) {
		if s.method == http.MethodHead {
			return 404, ""
		}
		return 200, `{"acknowledged":true}`
	})

	if err := c.EnsureIndex(context.Background()); err != nil {
		t.Fatalf("EnsureIndex: %v", err)
	}
	got := f.calls()
	if len(got) != 2 || got[1].method != http.MethodPut {
		t.Fatalf("requests = %+v, want a HEAD then a PUT", got)
	}

	var mapping struct {
		Mappings struct {
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
		} `json:"mappings"`
	}
	if err := json.Unmarshal([]byte(got[1].body), &mapping); err != nil {
		t.Fatalf("mapping is not valid JSON: %v", err)
	}
	// Every field the Doc writes must be mapped, or OpenSearch guesses a type
	// for it and the guess is wrong in ways that only show up in queries —
	// a keyword inferred as text stops matching exact filters.
	for field, want := range map[string]string{
		"product_id":        "keyword",
		"slug":              "keyword",
		"title":             "text",
		"category_id":       "keyword",
		"price_units":       "long",
		"price_nanos":       "integer",
		"currency":          "keyword",
		"primary_media_key": "keyword",
		"indexed_at":        "date",
	} {
		if got := mapping.Mappings.Properties[field].Type; got != want {
			t.Errorf("mapping for %s = %q, want %q", field, got, want)
		}
	}
}

func TestEnsureIndex_CreateFailureSurfaces(t *testing.T) {
	_, c := newFakeOS(t, func(s seen) (int, string) {
		if s.method == http.MethodHead {
			return 404, ""
		}
		return 400, `{"error":"invalid_index_name_exception"}`
	})

	err := c.EnsureIndex(context.Background())
	if err == nil {
		t.Fatal("a failed index creation was reported as success")
	}
	if !strings.Contains(err.Error(), "invalid_index_name_exception") {
		t.Errorf("err = %v, want the reason OpenSearch gave", err)
	}
}

func TestRawSearch_DecodesHitsAndAggregations(t *testing.T) {
	f, c := newFakeOS(t, func(seen) (int, string) {
		return 200, `{
			"hits": {
				"total": {"value": 2},
				"hits": [
					{"_id":"prod-1","_score":1.5,"_source":{"product_id":"prod-1","title":"Blue Mug"}},
					{"_id":"prod-2","_score":0.5,"_source":{"product_id":"prod-2","title":"Red Mug"}}
				]
			},
			"aggregations": {"by_category": {"buckets": [{"key":"cat-9","doc_count":2}]}}
		}`
	})

	out, err := c.RawSearch(context.Background(), []byte(`{"query":{"match_all":{}}}`))
	if err != nil {
		t.Fatalf("RawSearch: %v", err)
	}
	if out.Hits.Total.Value != 2 {
		t.Errorf("total = %d, want 2", out.Hits.Total.Value)
	}
	if len(out.Hits.Hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(out.Hits.Hits))
	}
	if len(out.Aggregations["by_category"].Buckets) != 1 {
		t.Errorf("aggregations = %+v", out.Aggregations)
	}

	got := f.only(t)
	if got.method != http.MethodPost || got.path != "/"+index.IndexName+"/_search" {
		t.Errorf("%s %s, want POST to the index's _search", got.method, got.path)
	}
	if got.body != `{"query":{"match_all":{}}}` {
		t.Errorf("query body was altered in transit: %s", got.body)
	}
}

func TestRawSearch_ErrorsAndGarbage(t *testing.T) {
	t.Run("rejected query", func(t *testing.T) {
		_, c := newFakeOS(t, func(seen) (int, string) {
			return 400, `{"error":"parsing_exception"}`
		})
		if _, err := c.RawSearch(context.Background(), []byte(`{`)); err == nil {
			t.Fatal("a rejected query was reported as success")
		}
	})
	t.Run("undecodable response", func(t *testing.T) {
		_, c := newFakeOS(t, func(seen) (int, string) { return 200, `not json` })
		if _, err := c.RawSearch(context.Background(), []byte(`{}`)); err == nil {
			t.Fatal("a non-JSON response decoded without error")
		}
	})
}

// Every method must report a dead OpenSearch rather than appearing to succeed.
func TestUnreachableOpenSearch(t *testing.T) {
	c, err := index.New([]string{"http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	if err := c.EnsureIndex(ctx); err == nil {
		t.Error("EnsureIndex succeeded against nothing")
	}
	if err := c.Delete(ctx, "prod-1"); err == nil {
		t.Error("Delete succeeded against nothing")
	}
	if err := c.UpsertFromEvent(ctx, &catalogv1.ProductChanged{
		ProductId: "prod-1", Product: activeProduct(),
	}); err == nil {
		t.Error("UpsertFromEvent succeeded against nothing")
	}
	if _, err := c.RawSearch(ctx, []byte(`{}`)); err == nil {
		t.Error("RawSearch succeeded against nothing")
	}
}
