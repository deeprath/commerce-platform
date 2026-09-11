package grpcsvc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	searchv1 "github.com/deeprath/commerce-platform/gen/go/commerce/search/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/search/internal/index"
)

// fakeSearcher serves a canned RawSearch response/error, and records the
// query body it was last called with so tests can assert on what Search/
// Autocomplete actually asked OpenSearch for.
type fakeSearcher struct {
	result  *index.SearchResult
	err     error
	lastReq map[string]any // decoded copy of the last request body
}

func (f *fakeSearcher) RawSearch(_ context.Context, body []byte) (*index.SearchResult, error) {
	f.lastReq = map[string]any{}
	if err := json.Unmarshal(body, &f.lastReq); err != nil {
		panic(err) // a bug in buildQuery, not something a test should assert around
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

// resultFrom builds an *index.SearchResult the same way the real client
// does: decode it from JSON. index's response types are unexported, so this
// is also the only way to populate one from outside the package.
func resultFrom(t *testing.T, jsonBody string) *index.SearchResult {
	t.Helper()
	var r index.SearchResult
	if err := json.Unmarshal([]byte(jsonBody), &r); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return &r
}

func errKind(t *testing.T, err error) errs.Kind {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("expected an *errs.Error, got %T: %v", err, err)
	}
	return e.Kind
}

func TestSearch_MapsHitsFacetsAndNextPageToken(t *testing.T) {
	f := &fakeSearcher{result: resultFrom(t, `{
		"hits": {
			"total": {"value": 30},
			"hits": [
				{"_score": 1.5, "_source": {"product_id": "p1", "slug": "trail-cap", "title": "Trail Cap", "category_id": "apparel", "price_units": 24, "price_nanos": 500000000, "currency": "USD", "primary_media_key": "k1"}}
			]
		},
		"aggregations": {
			"category_id": {"buckets": [{"key": "apparel", "doc_count": 12}, {"key": "camping", "doc_count": 3}]}
		}
	}`)}
	s := New(f)

	res, err := s.Search(context.Background(), &searchv1.SearchRequest{
		Query: "cap",
		Page:  &commonv1.PageRequest{PageSize: 10},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(res.GetHits()) != 1 {
		t.Fatalf("hits = %+v", res.GetHits())
	}
	h := res.GetHits()[0]
	if h.GetProductId() != "p1" || h.GetSlug() != "trail-cap" || h.GetScore() != 1.5 {
		t.Fatalf("hit mismapped: %+v", h)
	}
	if h.GetListPrice().GetUnits() != 24 || h.GetListPrice().GetCurrencyCode() != "USD" {
		t.Fatalf("list_price mismapped: %+v", h.GetListPrice())
	}

	if len(res.GetFacets()) != 1 || res.GetFacets()[0].GetField() != "category_id" {
		t.Fatalf("facets = %+v", res.GetFacets())
	}
	values := res.GetFacets()[0].GetValues()
	if len(values) != 2 || values[0].GetValue() != "apparel" || values[0].GetCount() != 12 {
		t.Fatalf("facet values = %+v", values)
	}

	// from(0) + size(10) < total(30) => there's a next page.
	if res.GetPage().GetNextPageToken() == "" {
		t.Fatal("expected a next_page_token, got none")
	}
	if res.GetPage().GetTotalSize() != 30 {
		t.Fatalf("total_size = %d", res.GetPage().GetTotalSize())
	}
}

func TestSearch_NoNextPageTokenOnTheLastPage(t *testing.T) {
	f := &fakeSearcher{result: resultFrom(t, `{"hits": {"total": {"value": 1}, "hits": []}}`)}
	s := New(f)

	res, err := s.Search(context.Background(), &searchv1.SearchRequest{Page: &commonv1.PageRequest{PageSize: 10}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.GetPage().GetNextPageToken() != "" {
		t.Fatalf("next_page_token = %q, want none", res.GetPage().GetNextPageToken())
	}
}

func TestSearch_PageSizeDefaultedAndClamped(t *testing.T) {
	for _, tc := range []struct {
		name     string
		size     int32
		wantSize float64
	}{
		{"zero uses the default", 0, defaultPageSize},
		{"negative uses the default", -5, defaultPageSize},
		{"over the max uses the default", maxPageSize + 1, defaultPageSize},
		{"within range is kept as-is", 5, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSearcher{result: resultFrom(t, `{"hits": {"total": {"value": 0}, "hits": []}}`)}
			s := New(f)
			if _, err := s.Search(context.Background(), &searchv1.SearchRequest{Page: &commonv1.PageRequest{PageSize: tc.size}}); err != nil {
				t.Fatalf("Search: %v", err)
			}
			if f.lastReq["size"] != tc.wantSize {
				t.Fatalf("size = %v, want %v", f.lastReq["size"], tc.wantSize)
			}
		})
	}
}

func TestSearch_RejectsAMalformedPageToken(t *testing.T) {
	f := &fakeSearcher{}
	s := New(f)
	_, err := s.Search(context.Background(), &searchv1.SearchRequest{Page: &commonv1.PageRequest{PageToken: "not-base64url!!"}})
	if err == nil {
		t.Fatal("expected an error for a malformed page_token")
	}
	if got := errKind(t, err); got != errs.KindInvalidArgument {
		t.Fatalf("kind = %v, want KindInvalidArgument", got)
	}
}

func TestSearch_RejectsAWindowTooDeep(t *testing.T) {
	f := &fakeSearcher{}
	s := New(f)
	_, err := s.Search(context.Background(), &searchv1.SearchRequest{
		Page: &commonv1.PageRequest{PageToken: encodeOffset(maxOffset - 1), PageSize: 50},
	})
	if err == nil {
		t.Fatal("expected an error for a too-deep pagination window")
	}
	if got := errKind(t, err); got != errs.KindInvalidArgument {
		t.Fatalf("kind = %v, want KindInvalidArgument", got)
	}
}

func TestSearch_PropagatesTheIndexError(t *testing.T) {
	want := errs.New(errs.KindUnavailable, "OS_UNREACHABLE", "opensearch request failed")
	f := &fakeSearcher{err: want}
	s := New(f)
	_, err := s.Search(context.Background(), &searchv1.SearchRequest{})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestSearch_BuildsTheExpectedQueryShape(t *testing.T) {
	f := &fakeSearcher{result: resultFrom(t, `{"hits": {"total": {"value": 0}, "hits": []}}`)}
	s := New(f)
	_, err := s.Search(context.Background(), &searchv1.SearchRequest{
		Query:         "cap",
		CategoryId:    "apparel",
		Attributes:    map[string]string{"color": "red"},
		MinPriceUnits: 10,
		MaxPriceUnits: 50,
		Sort:          searchv1.SortOrder_SORT_ORDER_PRICE_DESC,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	query, _ := f.lastReq["query"].(map[string]any)
	boolQ, _ := query["bool"].(map[string]any)
	must, _ := boolQ["must"].([]any)
	if len(must) != 1 {
		t.Fatalf("must = %+v, want a single multi_match clause for a non-empty query", must)
	}
	filter, _ := boolQ["filter"].([]any)
	if len(filter) != 3 { // category_id, attributes.color, price range
		t.Fatalf("filter = %+v, want 3 clauses (category + attribute + price range)", filter)
	}

	sort, _ := f.lastReq["sort"].([]any)
	if len(sort) == 0 {
		t.Fatal("expected a sort clause")
	}
	sortField, _ := sort[0].(map[string]any)
	if _, ok := sortField["price_units"]; !ok {
		t.Fatalf("sort = %+v, want price_units first for SORT_ORDER_PRICE_DESC", sort)
	}
}

func TestSearch_EmptyQueryFallsBackToMatchAll(t *testing.T) {
	f := &fakeSearcher{result: resultFrom(t, `{"hits": {"total": {"value": 0}, "hits": []}}`)}
	s := New(f)
	if _, err := s.Search(context.Background(), &searchv1.SearchRequest{}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	query, _ := f.lastReq["query"].(map[string]any)
	boolQ, _ := query["bool"].(map[string]any)
	must, _ := boolQ["must"].([]any)
	if len(must) != 1 {
		t.Fatalf("must = %+v", must)
	}
	clause, _ := must[0].(map[string]any)
	if _, ok := clause["match_all"]; !ok {
		t.Fatalf("must[0] = %+v, want match_all for an empty query", clause)
	}
}

func TestAutocomplete_ReturnsUniqueTitlesInOrder(t *testing.T) {
	f := &fakeSearcher{result: resultFrom(t, `{
		"hits": {"total": {"value": 2}, "hits": [
			{"_source": {"title": "Trail Cap"}},
			{"_source": {"title": "Trail Cap"}},
			{"_source": {"title": "Trail Pants"}}
		]}
	}`)}
	s := New(f)

	res, err := s.Autocomplete(context.Background(), &searchv1.AutocompleteRequest{Prefix: "trail"})
	if err != nil {
		t.Fatalf("Autocomplete: %v", err)
	}
	if got := res.GetSuggestions(); len(got) != 2 || got[0] != "Trail Cap" || got[1] != "Trail Pants" {
		t.Fatalf("suggestions = %v, want [Trail Cap, Trail Pants] (deduped, in order)", got)
	}
}

func TestAutocomplete_EmptyPrefixSkipsTheIndexEntirely(t *testing.T) {
	f := &fakeSearcher{}
	s := New(f)
	res, err := s.Autocomplete(context.Background(), &searchv1.AutocompleteRequest{Prefix: ""})
	if err != nil {
		t.Fatalf("Autocomplete: %v", err)
	}
	if len(res.GetSuggestions()) != 0 {
		t.Fatalf("suggestions = %v, want none", res.GetSuggestions())
	}
	if f.lastReq != nil {
		t.Fatal("RawSearch should never have been called for an empty prefix")
	}
}

func TestAutocomplete_LimitDefaultedAndClamped(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   int32
		want float64
	}{
		{"zero uses the default of 5", 0, 5},
		{"negative uses the default of 5", -1, 5},
		{"over 10 uses the default of 5", 11, 5},
		{"within range is kept as-is", 3, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSearcher{result: resultFrom(t, `{"hits": {"total": {"value": 0}, "hits": []}}`)}
			s := New(f)
			if _, err := s.Autocomplete(context.Background(), &searchv1.AutocompleteRequest{Prefix: "x", Limit: tc.in}); err != nil {
				t.Fatalf("Autocomplete: %v", err)
			}
			if f.lastReq["size"] != tc.want {
				t.Fatalf("size = %v, want %v", f.lastReq["size"], tc.want)
			}
		})
	}
}

func TestAutocomplete_PropagatesTheIndexError(t *testing.T) {
	want := errs.New(errs.KindUnavailable, "OS_UNREACHABLE", "opensearch request failed")
	f := &fakeSearcher{err: want}
	s := New(f)
	_, err := s.Autocomplete(context.Background(), &searchv1.AutocompleteRequest{Prefix: "x"})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestSortClause(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      searchv1.SortOrder
		wantKey string
	}{
		{"newest sorts by indexed_at desc", searchv1.SortOrder_SORT_ORDER_NEWEST, "indexed_at"},
		{"price_asc sorts by price_units", searchv1.SortOrder_SORT_ORDER_PRICE_ASC, "price_units"},
		{"price_desc sorts by price_units", searchv1.SortOrder_SORT_ORDER_PRICE_DESC, "price_units"},
		{"relevance falls back to _score", searchv1.SortOrder_SORT_ORDER_RELEVANCE, "_score"},
		{"unspecified falls back to _score", searchv1.SortOrder_SORT_ORDER_UNSPECIFIED, "_score"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clause := sortClause(tc.in)
			if len(clause) != 2 {
				t.Fatalf("sortClause = %+v, want 2 elements", clause)
			}
			switch first := clause[0].(type) {
			case string:
				if first != tc.wantKey {
					t.Fatalf("sortClause[0] = %q, want %q", first, tc.wantKey)
				}
			case map[string]any:
				if _, ok := first[tc.wantKey]; !ok {
					t.Fatalf("sortClause[0] = %+v, want a %q key", first, tc.wantKey)
				}
			default:
				t.Fatalf("sortClause[0] has unexpected type %T", first)
			}
		})
	}
}

func TestToString(t *testing.T) {
	if got := toString("apparel"); got != "apparel" {
		t.Fatalf("toString(string) = %q", got)
	}
	if got := toString(float64(7)); got != "7" {
		t.Fatalf("toString(float64) = %q", got)
	}
	if got := toString(true); got != "" {
		t.Fatalf("toString(unsupported type) = %q, want empty", got)
	}
}

func TestDecodeOffset(t *testing.T) {
	for _, tc := range []struct {
		name    string
		token   string
		want    int
		wantErr bool
	}{
		{"empty token is offset 0", "", 0, false},
		{"round-trips through encodeOffset", encodeOffset(42), 42, false},
		{"not valid base64url", "!!!", 0, true},
		{"decodes to a non-integer", base64.RawURLEncoding.EncodeToString([]byte("nope")), 0, true},
		{"decodes to a negative integer", base64.RawURLEncoding.EncodeToString([]byte("-1")), 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeOffset(tc.token)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				if kind := errKind(t, err); kind != errs.KindInvalidArgument {
					t.Fatalf("kind = %v, want KindInvalidArgument", kind)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeOffset(%q): %v", tc.token, err)
			}
			if got != tc.want {
				t.Fatalf("decodeOffset(%q) = %d, want %d", tc.token, got, tc.want)
			}
		})
	}
}
