package consumer_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	catalogv1 "github.com/deeprath/commerce-platform/gen/go/commerce/catalog/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/search/internal/consumer"
	"github.com/deeprath/commerce-platform/services/search/internal/index"
)

type fakeIndex struct {
	got []*catalogv1.ProductChanged
	err error
}

func (f *fakeIndex) UpsertFromEvent(_ context.Context, evt *catalogv1.ProductChanged) error {
	f.got = append(f.got, evt)
	return f.err
}

func record(t *testing.T, evt *catalogv1.ProductChanged) *kgo.Record {
	t.Helper()
	b, err := proto.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &kgo.Record{Topic: "commerce.catalog.product_changed", Offset: 7, Value: b}
}

func TestHandler_HandsTheDecodedEventToTheIndex(t *testing.T) {
	idx := &fakeIndex{}
	evt := &catalogv1.ProductChanged{
		ProductId: "prod-1",
		Change:    catalogv1.ChangeType_CHANGE_TYPE_UPDATED,
		Product:   &catalogv1.Product{Id: "prod-1", Title: "Blue Mug"},
	}

	if err := consumer.Handler(idx)(context.Background(), record(t, evt)); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if len(idx.got) != 1 || !proto.Equal(idx.got[0], evt) {
		t.Fatalf("index received %v, want %v", idx.got, evt)
	}
}

func TestHandler_UndecodableRecordIsSkipped(t *testing.T) {
	idx := &fakeIndex{}
	if err := consumer.Handler(idx)(context.Background(),
		&kgo.Record{Value: []byte{0x0a, 0xff}}); err != nil {
		t.Fatalf("err = %v, want nil so the partition keeps moving", err)
	}
	if len(idx.got) != 0 {
		t.Fatalf("garbage reached the index: %v", idx.got)
	}
}

// Every indexing failure must reach pkg/kafka. Before, only Unavailable did;
// the rest were logged and committed, so the event was gone and the index
// stayed wrong until the product next changed.
func TestHandler_ReturnsEveryIndexingFailure(t *testing.T) {
	for _, want := range []error{
		errs.New(errs.KindUnavailable, "OS_UNREACHABLE", "down"),
		errs.New(errs.KindInternal, "OS_INDEX_ERR", "mapper_parsing_exception"),
		errors.New("anything else"),
	} {
		idx := &fakeIndex{err: want}
		err := consumer.Handler(idx)(context.Background(),
			record(t, &catalogv1.ProductChanged{ProductId: "prod-1"}))
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v returned rather than swallowed", err, want)
		}
	}
}

// The whole path, with a real index client: what OpenSearch answers decides
// whether the record waits in Kafka or is parked. A cluster that is down or
// shedding load is a late event, not a bad one; parking it would move valid
// catalog changes to the DLQ every time OpenSearch had a bad minute.
func TestHandler_OpenSearchStatusDecidesHoldOrPark(t *testing.T) {
	for _, tc := range []struct {
		status int
		hold   bool
		why    string
	}{
		{http.StatusTooManyRequests, true, "write queue full"},
		{http.StatusInternalServerError, true, "cluster error"},
		{http.StatusServiceUnavailable, true, "no primary shard"},
		{http.StatusBadRequest, false, "a document OpenSearch will never accept"},
		{http.StatusConflict, false, "a request problem, not a cluster one"},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":"x"}`))
			}))
			t.Cleanup(srv.Close)
			idx, err := index.New([]string{srv.URL})
			if err != nil {
				t.Fatalf("index.New: %v", err)
			}

			err = consumer.Handler(idx)(context.Background(), record(t, &catalogv1.ProductChanged{
				ProductId: "prod-1",
				Product: &catalogv1.Product{
					Id: "prod-1", Status: catalogv1.ProductStatus_PRODUCT_STATUS_ACTIVE,
				},
			}))
			if err == nil {
				t.Fatalf("a %d from OpenSearch was committed as success", tc.status)
			}
			if got := consumer.Retryable(err); got != tc.hold {
				t.Errorf("Retryable = %v, want %v (%s)", got, tc.hold, tc.why)
			}
		})
	}
}

// A connection that never opens is the plainest outage there is.
func TestHandler_UnreachableOpenSearchIsHeld(t *testing.T) {
	idx, err := index.New([]string{"http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("index.New: %v", err)
	}
	err = consumer.Handler(idx)(context.Background(),
		record(t, &catalogv1.ProductChanged{ProductId: "prod-1"}))
	if err == nil || !consumer.Retryable(err) {
		t.Fatalf("err = %v, Retryable = %v; an unreachable cluster must hold the offset",
			err, err != nil && consumer.Retryable(err))
	}
}
