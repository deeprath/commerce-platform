// Package grpcsvc implements SearchService on top of the OpenSearch index.
// Read-only and fully anonymous — browse is public.
package grpcsvc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	searchv1 "github.com/deeprath/commerce-platform/gen/go/commerce/search/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/search/internal/index"
)

const (
	maxPageSize     = 50
	defaultPageSize = 20
	maxOffset       = 10_000 // OpenSearch from+size window
)

// searcher is the slice of *index.Client that Search/Autocomplete actually
// call. Narrowing to an interface here (rather than depending on the
// concrete client) lets tests fake OpenSearch's response without spinning
// one up or reaching into index's unexported response types — they just
// json.Unmarshal a fixture into an *index.SearchResult, exactly like the
// real client's RawSearch does.
type searcher interface {
	RawSearch(ctx context.Context, body []byte) (*index.SearchResult, error)
}

type Server struct {
	searchv1.UnimplementedSearchServiceServer
	idx searcher
}

func New(i searcher) *Server { return &Server{idx: i} }

func (s *Server) Search(ctx context.Context, req *searchv1.SearchRequest) (*searchv1.SearchResponse, error) {
	size := int(req.GetPage().GetPageSize())
	if size <= 0 || size > maxPageSize {
		size = defaultPageSize
	}
	from, err := decodeOffset(req.GetPage().GetPageToken())
	if err != nil {
		return nil, err
	}
	if from+size > maxOffset {
		return nil, errs.New(errs.KindInvalidArgument, "PAGE_TOO_DEEP", "pagination window exceeded; narrow the query")
	}

	body, err := json.Marshal(buildQuery(req, from, size))
	if err != nil {
		return nil, errs.Wrap(err, errs.KindInternal, "QUERY_MARSHAL", "cannot build query")
	}
	res, err := s.idx.RawSearch(ctx, body)
	if err != nil {
		return nil, err
	}

	hits := make([]*searchv1.Hit, 0, len(res.Hits.Hits))
	for _, h := range res.Hits.Hits {
		d := h.Source
		hits = append(hits, &searchv1.Hit{
			ProductId: d.ProductID, Slug: d.Slug, Title: d.Title,
			CategoryId: d.CategoryID,
			ListPrice: &commonv1.Money{
				CurrencyCode: d.Currency, Units: d.PriceUnits, Nanos: d.PriceNanos,
			},
			PrimaryMediaKey: d.PrimaryMediaKey,
			Score:           h.Score,
		})
	}

	var next string
	if int64(from+size) < res.Hits.Total.Value {
		next = encodeOffset(from + size)
	}

	return &searchv1.SearchResponse{
		Hits:   hits,
		Facets: extractFacets(res),
		Page:   &commonv1.PageResponse{NextPageToken: next, TotalSize: res.Hits.Total.Value},
	}, nil
}

func (s *Server) Autocomplete(ctx context.Context, req *searchv1.AutocompleteRequest) (*searchv1.AutocompleteResponse, error) {
	limit := int(req.GetLimit())
	if limit <= 0 || limit > 10 {
		limit = 5
	}
	prefix := req.GetPrefix()
	if prefix == "" {
		return &searchv1.AutocompleteResponse{}, nil
	}
	q := map[string]any{
		"size":    limit,
		"_source": []string{"title"},
		"query": map[string]any{
			"multi_match": map[string]any{
				"query":  prefix,
				"type":   "bool_prefix",
				"fields": []string{"title.suggest", "title.suggest._2gram", "title.suggest._3gram"},
			},
		},
	}
	body, _ := json.Marshal(q)
	res, err := s.idx.RawSearch(ctx, body)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]string, 0, limit)
	for _, h := range res.Hits.Hits {
		if t := h.Source.Title; t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return &searchv1.AutocompleteResponse{Suggestions: out}, nil
}

// --- query building ---

func buildQuery(req *searchv1.SearchRequest, from, size int) map[string]any {
	var must []any
	filter := []any{}

	if q := req.GetQuery(); q != "" {
		must = append(must, map[string]any{
			"multi_match": map[string]any{
				"query":     q,
				"fields":    []string{"title^3", "description"},
				"fuzziness": "AUTO",
			},
		})
	}
	if c := req.GetCategoryId(); c != "" {
		filter = append(filter, term("category_id", c))
	}
	for k, v := range req.GetAttributes() {
		filter = append(filter, term("attributes."+k, v))
	}
	if req.GetMinPriceUnits() > 0 || req.GetMaxPriceUnits() > 0 {
		rng := map[string]any{}
		if req.GetMinPriceUnits() > 0 {
			rng["gte"] = req.GetMinPriceUnits()
		}
		if req.GetMaxPriceUnits() > 0 {
			rng["lte"] = req.GetMaxPriceUnits()
		}
		filter = append(filter, map[string]any{"range": map[string]any{"price_units": rng}})
	}
	if len(must) == 0 {
		must = append(must, map[string]any{"match_all": map[string]any{}})
	}

	return map[string]any{
		"from": from,
		"size": size,
		"query": map[string]any{
			"bool": map[string]any{"must": must, "filter": filter},
		},
		"sort": sortClause(req.GetSort()),
		"aggs": map[string]any{
			"category_id": map[string]any{"terms": map[string]any{"field": "category_id", "size": 20}},
		},
	}
}

func term(field, value string) map[string]any {
	return map[string]any{"term": map[string]any{field: value}}
}

func sortClause(o searchv1.SortOrder) []any {
	switch o {
	case searchv1.SortOrder_SORT_ORDER_NEWEST:
		return []any{map[string]any{"indexed_at": "desc"}, "_doc"}
	case searchv1.SortOrder_SORT_ORDER_PRICE_ASC:
		return []any{map[string]any{"price_units": "asc"}, "_doc"}
	case searchv1.SortOrder_SORT_ORDER_PRICE_DESC:
		return []any{map[string]any{"price_units": "desc"}, "_doc"}
	default:
		return []any{"_score", "_doc"}
	}
}

func extractFacets(res *index.SearchResult) []*searchv1.Facet {
	var out []*searchv1.Facet
	for field, agg := range res.Aggregations {
		f := &searchv1.Facet{Field: field}
		for _, b := range agg.Buckets {
			f.Values = append(f.Values, &searchv1.FacetValue{
				Value: toString(b.Key), Count: b.DocCount,
			})
		}
		if len(f.Values) > 0 {
			out = append(out, f)
		}
	}
	return out
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	default:
		return ""
	}
}

func encodeOffset(n int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(n)))
}

func decodeOffset(tok string) (int, error) {
	if tok == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return 0, errs.New(errs.KindInvalidArgument, "BAD_CURSOR", "page_token is not valid")
	}
	n, err := strconv.Atoi(string(b))
	if err != nil || n < 0 {
		return 0, errs.New(errs.KindInvalidArgument, "BAD_CURSOR", "page_token is malformed")
	}
	return n, nil
}
