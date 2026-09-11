package grpcsvc_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	fulfillmentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/fulfillment/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/fulfillment/internal/domain"
	"github.com/deeprath/commerce-platform/services/fulfillment/internal/grpcsvc"
	"github.com/deeprath/commerce-platform/services/fulfillment/internal/store"
)

func spinUp(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; skipped with -short")
	}
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("fulfillment"),
		tcpostgres.WithUsername("t"), tcpostgres.WithPassword("t"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(ctx) })

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if err := pgx.Migrate(ctx, dsn, store.Migrations, "migrations"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgx.NewPool(ctx, pgx.PoolConfig{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func customer(sub string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Subject: sub, Roles: []string{"customer"}})
}

func manager(sub string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Subject: sub, Roles: []string{"customer", "order_manager"}})
}

func seed(t *testing.T, st *store.Store, orderID, owner string) *domain.Shipment {
	t.Helper()
	created, err := st.CreateFromOrder(context.Background(), orderID, owner,
		domain.Address{FullName: "Buyer", Line1: "1 Main St", City: "Springfield", Region: "IL", PostalCode: "62701", CountryCode: "US"},
		[]store.ShopItems{{Items: []domain.Item{{ProductID: "p1", Title: "Desk Lamp", Quantity: 2}}}}, "seed:"+orderID)
	if err != nil || len(created) != 1 {
		t.Fatalf("seed shipment: %v %+v", err, created)
	}
	return created[0]
}

func TestGetAndList_OwnerScoping(t *testing.T) {
	st := store.New(spinUp(t))
	s := grpcsvc.New(st)
	mine := seed(t, st, "order-a", "owner-1")
	_ = seed(t, st, "order-b", "owner-2")

	// Owner sees their own shipment.
	got, err := s.GetShipment(customer("owner-1"), &fulfillmentv1.GetShipmentRequest{Id: mine.ID})
	if err != nil {
		t.Fatalf("get own: %v", err)
	}
	if got.GetOrderId() != "order-a" || got.GetStatus() != fulfillmentv1.ShipmentStatus_SHIPMENT_STATUS_PENDING {
		t.Fatalf("bad shipment proto: %+v", got)
	}
	if len(got.GetItems()) != 1 || got.GetItems()[0].GetTitle() != "Desk Lamp" || got.GetShipTo().GetCity() != "Springfield" {
		t.Fatalf("proto not mapped from row: %+v", got)
	}

	// A different customer cannot read it.
	if _, err := s.GetShipment(customer("owner-2"), &fulfillmentv1.GetShipmentRequest{Id: mine.ID}); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("cross-owner read: want NotFound, got %v", err)
	}
	// A manager can read any shipment.
	if _, err := s.GetShipment(manager("staff"), &fulfillmentv1.GetShipmentRequest{Id: mine.ID}); err != nil {
		t.Fatalf("manager read: %v", err)
	}
	// Unauthenticated is rejected.
	if _, err := s.GetShipment(context.Background(), &fulfillmentv1.GetShipmentRequest{Id: mine.ID}); !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatalf("anon read: want Unauthenticated, got %v", err)
	}

	// List is scoped to the caller and filterable by order.
	list, err := s.ListShipments(customer("owner-1"), &fulfillmentv1.ListShipmentsRequest{})
	if err != nil || len(list.GetShipments()) != 1 || list.GetShipments()[0].GetOrderId() != "order-a" {
		t.Fatalf("list own: %v %+v", err, list)
	}
	empty, _ := s.ListShipments(customer("owner-1"), &fulfillmentv1.ListShipmentsRequest{OrderId: "order-b"})
	if len(empty.GetShipments()) != 0 {
		t.Fatalf("list filtered to another owner's order should be empty, got %d", len(empty.GetShipments()))
	}

	// A manager sees every customer's shipments; the status filter is honoured.
	all, err := s.ListShipments(manager("staff"), &fulfillmentv1.ListShipmentsRequest{})
	if err != nil || len(all.GetShipments()) != 2 {
		t.Fatalf("operator list: %v n=%d", err, len(all.GetShipments()))
	}
	pending, _ := s.ListShipments(manager("staff"), &fulfillmentv1.ListShipmentsRequest{Status: "PENDING"})
	if len(pending.GetShipments()) != 2 {
		t.Fatalf("PENDING queue = %d, want 2", len(pending.GetShipments()))
	}
}

func TestErrorPaths(t *testing.T) {
	st := store.New(spinUp(t))
	s := grpcsvc.New(st)
	const missing = "00000000-0000-0000-0000-000000000000"

	// ListShipments needs a principal.
	if _, err := s.ListShipments(context.Background(), &fulfillmentv1.ListShipmentsRequest{}); !errs.Is(err, errs.KindUnauthenticated) {
		t.Fatalf("anon list: want Unauthenticated, got %v", err)
	}
	// GetShipment for a nonexistent id is a clean NotFound.
	if _, err := s.GetShipment(customer("x"), &fulfillmentv1.GetShipmentRequest{Id: missing}); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("get missing: want NotFound, got %v", err)
	}
	// Admin transition on a nonexistent id surfaces the store's NotFound.
	if _, err := s.MarkShipped(manager("staff"), &fulfillmentv1.MarkShippedRequest{Id: missing}); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("ship missing: want NotFound, got %v", err)
	}
	if _, err := s.MarkDelivered(manager("staff"), &fulfillmentv1.MarkDeliveredRequest{Id: missing}); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("deliver missing: want NotFound, got %v", err)
	}
	if _, err := s.CancelShipment(manager("staff"), &fulfillmentv1.CancelShipmentRequest{Id: missing}); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("cancel missing: want NotFound, got %v", err)
	}
	// MarkDelivered / CancelShipment are also role-gated.
	if _, err := s.MarkDelivered(customer("c"), &fulfillmentv1.MarkDeliveredRequest{Id: missing}); !errs.Is(err, errs.KindPermissionDenied) {
		t.Fatalf("customer MarkDelivered: want PermissionDenied, got %v", err)
	}
	if _, err := s.CancelShipment(customer("c"), &fulfillmentv1.CancelShipmentRequest{Id: missing}); !errs.Is(err, errs.KindPermissionDenied) {
		t.Fatalf("customer CancelShipment: want PermissionDenied, got %v", err)
	}
}

func TestAdminTransitions_RoleGatedAndMapped(t *testing.T) {
	st := store.New(spinUp(t))
	s := grpcsvc.New(st)
	sh := seed(t, st, "order-c", "owner-3")

	// A plain customer cannot drive transitions.
	if _, err := s.MarkShipped(customer("owner-3"), &fulfillmentv1.MarkShippedRequest{Id: sh.ID}); !errs.Is(err, errs.KindPermissionDenied) {
		t.Fatalf("customer MarkShipped: want PermissionDenied, got %v", err)
	}

	// Manager ships it; carrier/tracking default when omitted.
	shipped, err := s.MarkShipped(manager("staff"), &fulfillmentv1.MarkShippedRequest{Id: sh.ID})
	if err != nil {
		t.Fatalf("MarkShipped: %v", err)
	}
	if shipped.GetStatus() != fulfillmentv1.ShipmentStatus_SHIPMENT_STATUS_SHIPPED ||
		shipped.GetCarrier() == "" || shipped.GetTrackingNumber() == "" || shipped.GetShippedAt() == "" {
		t.Fatalf("bad shipped proto: %+v", shipped)
	}

	delivered, err := s.MarkDelivered(manager("staff"), &fulfillmentv1.MarkDeliveredRequest{Id: sh.ID})
	if err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}
	if delivered.GetStatus() != fulfillmentv1.ShipmentStatus_SHIPMENT_STATUS_DELIVERED || delivered.GetDeliveredAt() == "" {
		t.Fatalf("bad delivered proto: %+v", delivered)
	}

	// Cancelling a delivered shipment is refused by the state machine.
	if _, err := s.CancelShipment(manager("staff"), &fulfillmentv1.CancelShipmentRequest{Id: sh.ID}); err == nil {
		t.Fatalf("cancel after delivery should fail")
	}

	// Cancel works on a fresh PENDING shipment and records the reason.
	fresh := seed(t, st, "order-d", "owner-4")
	cancelled, err := s.CancelShipment(manager("staff"), &fulfillmentv1.CancelShipmentRequest{Id: fresh.ID, Reason: "ADDRESS_INVALID"})
	if err != nil {
		t.Fatalf("CancelShipment: %v", err)
	}
	if cancelled.GetStatus() != fulfillmentv1.ShipmentStatus_SHIPMENT_STATUS_CANCELLED || cancelled.GetCancelReason() != "ADDRESS_INVALID" {
		t.Fatalf("bad cancelled proto: %+v", cancelled)
	}
}
