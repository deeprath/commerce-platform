package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc"

	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	"github.com/deeprath/commerce-platform/services/bff/internal/clients"
)

type checkoutOrder struct {
	orderv1.OrderServiceClient
	last *orderv1.CreateOrderRequest
}

func (f *checkoutOrder) CreateOrder(_ context.Context, in *orderv1.CreateOrderRequest, _ ...grpc.CallOption) (*orderv1.CreateOrderResponse, error) {
	f.last = in
	return &orderv1.CreateOrderResponse{OrderId: "order-1", PaymentId: "pay-1"}, nil
}

func postCheckout(t *testing.T, s *Server, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/checkout", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer shopper-token")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return serve(s, r)
}

const checkoutJSON = `{"ship_to":{"full_name":"Ada","line1":"1 Main","city":"Springfield","country_code":"US"},"payment_method_token":"pm_card_ok"}`

// The header is where clients and proxies conventionally put the key, so it has
// to reach the order service.
func TestCheckout_ForwardsTheIdempotencyKeyHeader(t *testing.T) {
	fo := &checkoutOrder{}
	s := &Server{cl: &clients.Set{Order: fo}}

	rec := postCheckout(t, s, checkoutJSON, map[string]string{"Idempotency-Key": "key-abc"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if fo.last.GetIdempotencyKey() != "key-abc" {
		t.Fatalf("forwarded key = %q, want key-abc", fo.last.GetIdempotencyKey())
	}
}

// Callers that cannot set headers can send it in the body instead.
func TestCheckout_AcceptsTheKeyFromTheBody(t *testing.T) {
	fo := &checkoutOrder{}
	s := &Server{cl: &clients.Set{Order: fo}}

	body := `{"ship_to":{"full_name":"Ada","line1":"1 Main","city":"Springfield","country_code":"US"},"payment_method_token":"pm_card_ok","idempotency_key":"from-body"}`
	if rec := postCheckout(t, s, body, nil); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if fo.last.GetIdempotencyKey() != "from-body" {
		t.Fatalf("forwarded key = %q, want from-body", fo.last.GetIdempotencyKey())
	}
}

// When both are present the header wins — it is the one a retrying client or an
// intermediary will have preserved.
func TestCheckout_HeaderWinsOverTheBodyKey(t *testing.T) {
	fo := &checkoutOrder{}
	s := &Server{cl: &clients.Set{Order: fo}}

	body := `{"ship_to":{"full_name":"Ada","line1":"1 Main","city":"Springfield","country_code":"US"},"payment_method_token":"pm_card_ok","idempotency_key":"from-body"}`
	postCheckout(t, s, body, map[string]string{"Idempotency-Key": "from-header"})
	if fo.last.GetIdempotencyKey() != "from-header" {
		t.Fatalf("forwarded key = %q, want the header to win", fo.last.GetIdempotencyKey())
	}
}

// Surrounding whitespace must not make two spellings of the same key look
// different to the order service.
func TestCheckout_TrimsTheKey(t *testing.T) {
	fo := &checkoutOrder{}
	s := &Server{cl: &clients.Set{Order: fo}}

	postCheckout(t, s, checkoutJSON, map[string]string{"Idempotency-Key": "  padded-key\t"})
	if fo.last.GetIdempotencyKey() != "padded-key" {
		t.Fatalf("forwarded key = %q, want it trimmed", fo.last.GetIdempotencyKey())
	}
}

// No key keeps the old behaviour, so clients that do not send one still work.
func TestCheckout_NoKeyForwardsAnEmptyOne(t *testing.T) {
	fo := &checkoutOrder{}
	s := &Server{cl: &clients.Set{Order: fo}}

	if rec := postCheckout(t, s, checkoutJSON, nil); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if fo.last.GetIdempotencyKey() != "" {
		t.Fatalf("forwarded key = %q, want empty", fo.last.GetIdempotencyKey())
	}
}

// A blank header must not be preferred over a real body key.
func TestCheckout_BlankHeaderFallsBackToTheBody(t *testing.T) {
	fo := &checkoutOrder{}
	s := &Server{cl: &clients.Set{Order: fo}}

	body := `{"ship_to":{"full_name":"Ada","line1":"1 Main","city":"Springfield","country_code":"US"},"payment_method_token":"pm_card_ok","idempotency_key":"from-body"}`
	postCheckout(t, s, body, map[string]string{"Idempotency-Key": "   "})
	if fo.last.GetIdempotencyKey() != "from-body" {
		t.Fatalf("forwarded key = %q, want the body key", fo.last.GetIdempotencyKey())
	}
}

func TestCheckout_RequiresAuthentication(t *testing.T) {
	s := &Server{cl: &clients.Set{Order: &checkoutOrder{}}}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/checkout", strings.NewReader(checkoutJSON))
	r.Header.Set("Content-Type", "application/json")
	if rec := serve(s, r); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without a bearer token", rec.Code)
	}
}
