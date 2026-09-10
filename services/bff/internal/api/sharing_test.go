package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/deeprath/commerce-platform/services/bff/internal/clients"
)

func newSharingServer(fo *fakeOrder) *Server {
	return &Server{cl: &clients.Set{Order: fo}}
}

func doReq(t *testing.T, s *Server, method, path, body string, authed bool) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if authed {
		r.Header.Set("Authorization", "Bearer test-token")
	}
	rec := httptest.NewRecorder()
	s.Router([]string{"*"}).ServeHTTP(rec, r)
	return rec
}

func TestShareOrder_ForwardsGrantee(t *testing.T) {
	fo := &fakeOrder{}
	rec := doReq(t, newSharingServer(fo), http.MethodPost, "/api/v1/orders/o-1/share", `{"grantee":"friend"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if fo.lastShare.GetOrderId() != "o-1" || fo.lastShare.GetGranteeSubject() != "friend" {
		t.Fatalf("forwarded %+v", fo.lastShare)
	}
}

func TestShareOrder_RequiresAuth(t *testing.T) {
	fo := &fakeOrder{}
	rec := doReq(t, newSharingServer(fo), http.MethodPost, "/api/v1/orders/o-1/share", `{"grantee":"friend"}`, false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if fo.lastShare != nil {
		t.Fatal("gRPC called despite missing auth")
	}
}

func TestRevokeOrderShare_ForwardsPathParams(t *testing.T) {
	fo := &fakeOrder{}
	rec := doReq(t, newSharingServer(fo), http.MethodDelete, "/api/v1/orders/o-9/share/ex-friend", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if fo.lastRevoke.GetOrderId() != "o-9" || fo.lastRevoke.GetGranteeSubject() != "ex-friend" {
		t.Fatalf("forwarded %+v", fo.lastRevoke)
	}
}

func TestListOrderShares_ReturnsSubjects(t *testing.T) {
	fo := &fakeOrder{}
	rec := doReq(t, newSharingServer(fo), http.MethodGet, "/api/v1/orders/o-1/shares", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "friend-a") || !strings.Contains(rec.Body.String(), "friend-b") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if fo.lastShares.GetOrderId() != "o-1" {
		t.Fatalf("order id not forwarded: %+v", fo.lastShares)
	}
}

func TestShareOrder_MapsGrpcError(t *testing.T) {
	fo := &fakeOrder{err: status.Error(codes.NotFound, "ORDER_NOT_FOUND")}
	rec := doReq(t, newSharingServer(fo), http.MethodPost, "/api/v1/orders/o-x/share", `{"grantee":"f"}`, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
