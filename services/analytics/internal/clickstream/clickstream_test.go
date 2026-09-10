package clickstream_test

import (
	"context"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	clickstreamv1 "github.com/deeprath/commerce-platform/gen/go/commerce/clickstream/v1"
	"github.com/deeprath/commerce-platform/services/analytics/internal/clickhouse"
	"github.com/deeprath/commerce-platform/services/analytics/internal/clickstream"
)

type fakeSink struct {
	rows []clickhouse.Click
	err  error
}

func (f *fakeSink) AddClick(_ context.Context, c clickhouse.Click) error {
	if f.err != nil {
		return f.err
	}
	f.rows = append(f.rows, c)
	return nil
}

func rec(t *testing.T, m proto.Message) *kgo.Record {
	t.Helper()
	v, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return &kgo.Record{Topic: "commerce.clickstream.tracked", Value: v}
}

func TestTopics(t *testing.T) {
	got := clickstream.Topics()
	if len(got) != 1 || got[0] != "commerce.clickstream.tracked" {
		t.Fatalf("Topics() = %v", got)
	}
}

func TestHandler_MapsEveryEventType(t *testing.T) {
	when := "2026-09-10T12:00:00Z"
	cases := []struct {
		in   clickstreamv1.EventType
		want string
	}{
		{clickstreamv1.EventType_EVENT_TYPE_PAGE_VIEW, "page_view"},
		{clickstreamv1.EventType_EVENT_TYPE_PRODUCT_VIEW, "product_view"},
		{clickstreamv1.EventType_EVENT_TYPE_SEARCH, "search"},
		{clickstreamv1.EventType_EVENT_TYPE_ADD_TO_CART, "add_to_cart"},
		{clickstreamv1.EventType_EVENT_TYPE_BEGIN_CHECKOUT, "begin_checkout"},
		{clickstreamv1.EventType_EVENT_TYPE_PURCHASE, "purchase"},
	}
	for _, c := range cases {
		fs := &fakeSink{}
		err := clickstream.Handler(fs)(context.Background(), rec(t, &clickstreamv1.ClientEvent{
			Type: c.in, AnonymousId: "a1", SessionId: "s1", Path: "/x",
			ReceivedAt: when, ClientTime: when, ValueMinor: 100, CurrencyCode: "USD",
		}))
		if err != nil {
			t.Fatalf("%s: %v", c.want, err)
		}
		if len(fs.rows) != 1 || fs.rows[0].Type != c.want {
			t.Fatalf("type %v -> %+v, want %q", c.in, fs.rows, c.want)
		}
		if !fs.rows[0].OccurredAt.Equal(fs.rows[0].ClientTime) {
			t.Errorf("timestamps not parsed: %+v", fs.rows[0])
		}
		if fs.rows[0].ValueMinor != 100 || fs.rows[0].Currency != "USD" {
			t.Errorf("value/currency not carried: %+v", fs.rows[0])
		}
	}
}

func TestHandler_SkipsUnknownAndUndecodable(t *testing.T) {
	fs := &fakeSink{}
	h := clickstream.Handler(fs)

	// UNSPECIFIED type -> dropped, no error.
	if err := h(context.Background(), rec(t, &clickstreamv1.ClientEvent{})); err != nil {
		t.Fatalf("unspecified: %v", err)
	}
	// Garbage bytes -> dropped, no error (poison message must not stall).
	if err := h(context.Background(), &kgo.Record{
		Topic: "commerce.clickstream.tracked", Value: []byte{0xff, 0xff, 0xff},
	}); err != nil {
		t.Fatalf("undecodable: %v", err)
	}
	if len(fs.rows) != 0 {
		t.Fatalf("buffered %d rows, want 0", len(fs.rows))
	}
}

func TestHandler_TimestampFallbacks(t *testing.T) {
	fs := &fakeSink{}
	before := time.Now().UTC().Add(-time.Second)
	err := clickstream.Handler(fs)(context.Background(), rec(t, &clickstreamv1.ClientEvent{
		Type: clickstreamv1.EventType_EVENT_TYPE_PAGE_VIEW,
		// no ReceivedAt -> now(); bad ClientTime -> falls back to OccurredAt
		ClientTime: "garbage",
	}))
	if err != nil {
		t.Fatal(err)
	}
	r := fs.rows[0]
	if r.OccurredAt.Before(before) {
		t.Errorf("OccurredAt not defaulted to now: %v", r.OccurredAt)
	}
	if !r.ClientTime.Equal(r.OccurredAt) {
		t.Errorf("ClientTime should fall back to OccurredAt, got %v vs %v", r.ClientTime, r.OccurredAt)
	}
}

func TestHandler_PropagatesSinkError(t *testing.T) {
	fs := &fakeSink{err: context.DeadlineExceeded}
	err := clickstream.Handler(fs)(context.Background(), rec(t, &clickstreamv1.ClientEvent{
		Type: clickstreamv1.EventType_EVENT_TYPE_PURCHASE,
	}))
	if err == nil {
		t.Fatal("want the sink error propagated for retry")
	}
}
