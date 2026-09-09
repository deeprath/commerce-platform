package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fulfillmentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/fulfillment/v1"
	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	"github.com/deeprath/commerce-platform/services/bff/internal/clients"
)

// --- fakes ---------------------------------------------------------------

type fakeOrder struct {
	orderv1.OrderServiceClient
	lastList   *orderv1.ListOrdersRequest
	lastGet    *orderv1.GetOrderRequest
	lastReturn *orderv1.ListReturnsRequest
	lastDecide *orderv1.DecideReturnRequest
	err        error // when set, every method returns it
}

func (f *fakeOrder) ListOrders(_ context.Context, in *orderv1.ListOrdersRequest, _ ...grpc.CallOption) (*orderv1.ListOrdersResponse, error) {
	f.lastList = in
	if f.err != nil {
		return nil, f.err
	}
	return &orderv1.ListOrdersResponse{Orders: []*orderv1.Order{{Id: "o1"}}}, nil
}
func (f *fakeOrder) GetOrder(_ context.Context, in *orderv1.GetOrderRequest, _ ...grpc.CallOption) (*orderv1.Order, error) {
	f.lastGet = in
	if f.err != nil {
		return nil, f.err
	}
	return &orderv1.Order{Id: in.GetId()}, nil
}
func (f *fakeOrder) ListReturns(_ context.Context, in *orderv1.ListReturnsRequest, _ ...grpc.CallOption) (*orderv1.ListReturnsResponse, error) {
	f.lastReturn = in
	if f.err != nil {
		return nil, f.err
	}
	return &orderv1.ListReturnsResponse{Returns: []*orderv1.Return{{Id: "r1", Status: orderv1.ReturnStatus_RETURN_STATUS_REQUESTED}}}, nil
}
func (f *fakeOrder) DecideReturn(_ context.Context, in *orderv1.DecideReturnRequest, _ ...grpc.CallOption) (*orderv1.Return, error) {
	f.lastDecide = in
	if f.err != nil {
		return nil, f.err
	}
	return &orderv1.Return{Id: in.GetId(), Status: orderv1.ReturnStatus_RETURN_STATUS_APPROVED}, nil
}

type fakeFulfillment struct {
	fulfillmentv1.FulfillmentServiceClient
	lastList    *fulfillmentv1.ListShipmentsRequest
	lastShip    *fulfillmentv1.MarkShippedRequest
	lastDeliver *fulfillmentv1.MarkDeliveredRequest
	lastCancel  *fulfillmentv1.CancelShipmentRequest
}

func (f *fakeFulfillment) ListShipments(_ context.Context, in *fulfillmentv1.ListShipmentsRequest, _ ...grpc.CallOption) (*fulfillmentv1.ListShipmentsResponse, error) {
	f.lastList = in
	return &fulfillmentv1.ListShipmentsResponse{Shipments: []*fulfillmentv1.Shipment{{Id: "s1"}}}, nil
}
func (f *fakeFulfillment) MarkShipped(_ context.Context, in *fulfillmentv1.MarkShippedRequest, _ ...grpc.CallOption) (*fulfillmentv1.Shipment, error) {
	f.lastShip = in
	return &fulfillmentv1.Shipment{Id: in.GetId(), Status: fulfillmentv1.ShipmentStatus_SHIPMENT_STATUS_SHIPPED}, nil
}
func (f *fakeFulfillment) MarkDelivered(_ context.Context, in *fulfillmentv1.MarkDeliveredRequest, _ ...grpc.CallOption) (*fulfillmentv1.Shipment, error) {
	f.lastDeliver = in
	return &fulfillmentv1.Shipment{Id: in.GetId(), Status: fulfillmentv1.ShipmentStatus_SHIPMENT_STATUS_DELIVERED}, nil
}
func (f *fakeFulfillment) CancelShipment(_ context.Context, in *fulfillmentv1.CancelShipmentRequest, _ ...grpc.CallOption) (*fulfillmentv1.Shipment, error) {
	f.lastCancel = in
	return &fulfillmentv1.Shipment{Id: in.GetId(), Status: fulfillmentv1.ShipmentStatus_SHIPMENT_STATUS_CANCELLED}, nil
}

// --- helpers -----------------------------------------------------------------

func adminReq(method, path, body string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set("Authorization", "Bearer operator-token")
	return r
}

func adminServer() (*Server, *fakeOrder, *fakeFulfillment) {
	fo, ff := &fakeOrder{}, &fakeFulfillment{}
	return &Server{cl: &clients.Set{Order: fo, Fulfillment: ff}}, fo, ff
}

// --- tests ---------------------------------------------------------------

func TestAdmin_RequiresToken(t *testing.T) {
	s, _, _ := adminServer()
	for _, p := range []string{
		"/api/v1/admin/orders", "/api/v1/admin/orders/o1", "/api/v1/admin/returns",
		"/api/v1/admin/shipments",
	} {
		rec := serve(s, httptest.NewRequest(http.MethodGet, p, nil)) // no Authorization header
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s without a token: status %d, want 401", p, rec.Code)
		}
	}
}

func TestAdmin_Orders(t *testing.T) {
	s, fo, _ := adminServer()

	rec := serve(s, adminReq(http.MethodGet, "/api/v1/admin/orders?owner_id=cust-a&status=FULFILLED&page_size=5", ""))
	if rec.Code != 200 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if fo.lastList.GetOwnerId() != "cust-a" || fo.lastList.GetStatus() != "FULFILLED" ||
		fo.lastList.GetPage().GetPageSize() != 5 {
		t.Fatalf("filters not forwarded: %+v", fo.lastList)
	}
	if !strings.Contains(rec.Body.String(), `"id":"o1"`) {
		t.Fatalf("response body missing the order: %s", rec.Body.String())
	}

	rec = serve(s, adminReq(http.MethodGet, "/api/v1/admin/orders/order-42", ""))
	if rec.Code != 200 || fo.lastGet.GetId() != "order-42" {
		t.Fatalf("get: %d id=%q", rec.Code, fo.lastGet.GetId())
	}
}

// A downstream gRPC error is mapped to its HTTP status by every admin handler.
func TestAdmin_DownstreamErrorsAreMapped(t *testing.T) {
	s, fo, _ := adminServer()
	fo.err = status.Error(codes.NotFound, "ORDER_NOT_FOUND")

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/admin/orders", ""},
		{http.MethodGet, "/api/v1/admin/orders/x", ""},
		{http.MethodGet, "/api/v1/admin/returns", ""},
		{http.MethodPost, "/api/v1/admin/returns/x/decide", `{"approve":true}`},
	} {
		rec := serve(s, adminReq(tc.method, tc.path, tc.body))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s: status %d, want 404", tc.method, tc.path, rec.Code)
		}
	}
}

func TestAdmin_ReturnsQueueAndDecision(t *testing.T) {
	s, fo, _ := adminServer()

	rec := serve(s, adminReq(http.MethodGet, "/api/v1/admin/returns?status=REQUESTED", ""))
	if rec.Code != 200 || fo.lastReturn.GetStatus() != "REQUESTED" {
		t.Fatalf("returns queue: %d status=%q", rec.Code, fo.lastReturn.GetStatus())
	}

	rec = serve(s, adminReq(http.MethodPost, "/api/v1/admin/returns/r-7/decide", `{"approve":true,"note":"ok"}`))
	if rec.Code != 200 {
		t.Fatalf("decide: %d %s", rec.Code, rec.Body.String())
	}
	if fo.lastDecide.GetId() != "r-7" || !fo.lastDecide.GetApprove() || fo.lastDecide.GetNote() != "ok" {
		t.Fatalf("decide not forwarded: %+v", fo.lastDecide)
	}

	// A malformed body is a 400 before the RPC.
	fo.lastDecide = nil
	rec = serve(s, adminReq(http.MethodPost, "/api/v1/admin/returns/r-7/decide", `{not json`))
	if rec.Code != 400 || fo.lastDecide != nil {
		t.Fatalf("bad json: status %d, decide called=%v", rec.Code, fo.lastDecide != nil)
	}
}

func TestAdmin_Shipments(t *testing.T) {
	s, _, ff := adminServer()

	rec := serve(s, adminReq(http.MethodGet, "/api/v1/admin/shipments?status=PENDING&order_id=o1", ""))
	if rec.Code != 200 || ff.lastList.GetStatus() != "PENDING" || ff.lastList.GetOrderId() != "o1" {
		t.Fatalf("list: %d %+v", rec.Code, ff.lastList)
	}

	rec = serve(s, adminReq(http.MethodPost, "/api/v1/admin/shipments/s-1/ship", `{"carrier":"UPS","tracking_number":"1Z9"}`))
	if rec.Code != 200 || ff.lastShip.GetId() != "s-1" || ff.lastShip.GetCarrier() != "UPS" || ff.lastShip.GetTrackingNumber() != "1Z9" {
		t.Fatalf("ship: %d %+v", rec.Code, ff.lastShip)
	}

	rec = serve(s, adminReq(http.MethodPost, "/api/v1/admin/shipments/s-1/deliver", ""))
	if rec.Code != 200 || ff.lastDeliver.GetId() != "s-1" {
		t.Fatalf("deliver: %d id=%q", rec.Code, ff.lastDeliver.GetId())
	}

	rec = serve(s, adminReq(http.MethodPost, "/api/v1/admin/shipments/s-1/cancel", `{"reason":"lost"}`))
	if rec.Code != 200 || ff.lastCancel.GetId() != "s-1" || ff.lastCancel.GetReason() != "lost" {
		t.Fatalf("cancel: %d %+v", rec.Code, ff.lastCancel)
	}
}
