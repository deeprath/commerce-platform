package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	fulfillmentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/fulfillment/v1"
	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/kafka"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/order/internal/consumer"
	"github.com/deeprath/commerce-platform/services/order/internal/domain"
	"github.com/deeprath/commerce-platform/services/order/internal/saga"
	"github.com/deeprath/commerce-platform/services/order/internal/store"
)

func spinUp(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; skipped with -short")
	}
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("order"),
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

func seedPending(t *testing.T, st *store.Store) *domain.Order {
	return seedPendingFor(t, st, "owner-1")
}

func seedPendingFor(t *testing.T, st *store.Store, owner string) *domain.Order {
	t.Helper()
	usd := func(c int64) domain.Money { return domain.Money{Currency: "USD", Cents: c} }
	o := &domain.Order{
		ID:      uuid.NewString(),
		OwnerID: owner,
		Status:  domain.StatusPendingPayment,
		Lines: []domain.Line{
			{ProductID: "p1", Title: "Desk Lamp", Quantity: 2, UnitPrice: usd(3499), LineTotal: usd(6998)},
		},
		Subtotal: usd(6998), Discount: usd(0), Tax: usd(560), Total: usd(7558),
		ShipTo:    domain.Address{FullName: "Buyer", Line1: "1 Main St", City: "Shelbyville", Region: "IL", PostalCode: "62701", CountryCode: "US"},
		PaymentID: "pay-1", ReservationID: "res-1",
	}
	if err := st.Insert(context.Background(), o); err != nil {
		t.Fatalf("insert: %v", err)
	}
	return o
}

// List: "" ownerID lists every customer's orders (operator mode), and status
// filters; a real ownerID stays scoped to that customer.
func TestList_OperatorScopeAndStatusFilter(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))

	a := seedPendingFor(t, st, "cust-a")
	_ = seedPendingFor(t, st, "cust-b")
	if _, err := st.Apply(ctx, a.ID, "c", func(o *domain.Order) error { return o.Confirm() }); err != nil {
		t.Fatalf("confirm a: %v", err)
	}

	all, _, err := st.List(ctx, "", "", 50, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("operator list: %v n=%d", err, len(all))
	}
	confirmed, _, _ := st.List(ctx, "", "CONFIRMED", 50, "")
	if len(confirmed) != 1 || confirmed[0].ID != a.ID {
		t.Fatalf("status filter: %+v", confirmed)
	}
	mine, _, _ := st.List(ctx, "cust-b", "", 50, "")
	if len(mine) != 1 || mine[0].OwnerID != "cust-b" {
		t.Fatalf("owner-scoped list leaked: %+v", mine)
	}
}

// A syntactically invalid id must be a clean NotFound, not a Postgres
// uuid-cast error surfacing as 500 (ZAP "Application Error Disclosure").
func TestGet_MalformedID_IsNotFoundNot500(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))

	_, err := st.Get(ctx, "id", "")
	if !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("Get(malformed): got %v, want NotFound", err)
	}
	_, err = st.GetReturn(ctx, "not-a-uuid", "")
	if !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("GetReturn(malformed): got %v, want NotFound", err)
	}
}

// The saga's Apply path drives CONFIRMED -> FULFILLED and writes the matching
// order.* outbox rows; the enriched order.confirmed event carries ship_to+lines.
func TestApply_ConfirmThenFulfill_OutboxEnrichment(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	o := seedPending(t, st)

	if _, err := st.Apply(ctx, o.ID, "evt-confirm", func(o *domain.Order) error { return o.Confirm() }); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	ful, err := st.Apply(ctx, o.ID, "evt-fulfill", func(o *domain.Order) error { return o.Fulfill() })
	if err != nil {
		t.Fatalf("fulfill: %v", err)
	}
	if ful == nil || ful.Status != domain.StatusFulfilled {
		t.Fatalf("status = %v, want FULFILLED", ful)
	}

	// Redelivered fulfill event -> short-circuits, no error, no extra outbox row.
	if again, err := st.Apply(ctx, o.ID, "evt-fulfill", func(o *domain.Order) error { return o.Fulfill() }); err != nil || again != nil {
		t.Fatalf("duplicate event id should be a no-op: %+v %v", again, err)
	}

	// order.confirmed carries the shipping address and line items.
	var confirmedPayload []byte
	if err := pool.QueryRow(ctx,
		`SELECT payload FROM outbox WHERE topic='commerce.order.confirmed'`).Scan(&confirmedPayload); err != nil {
		t.Fatalf("no order.confirmed outbox row: %v", err)
	}
	var oc orderv1.OrderConfirmed
	if err := proto.Unmarshal(confirmedPayload, &oc); err != nil {
		t.Fatalf("confirmed payload: %v", err)
	}
	if oc.GetShipTo().GetCity() != "Shelbyville" || len(oc.GetLines()) != 1 || oc.GetLines()[0].GetTitle() != "Desk Lamp" {
		t.Fatalf("order.confirmed not enriched: %+v", &oc)
	}
	if oc.GetLines()[0].GetLineTotal().GetUnits() != 69 {
		t.Fatalf("line money not carried: %+v", oc.GetLines()[0])
	}

	// order.fulfilled emitted exactly once.
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic='commerce.order.fulfilled'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("order.fulfilled count = %d, want 1", n)
	}
}

// A delivery event for a cancelled order must not resurrect it.
func TestApply_ShipmentDeliveredOnCancelledOrder_NoOp(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	o := seedPending(t, st)

	if _, err := st.Apply(ctx, o.ID, "c", func(o *domain.Order) error { return o.Cancel("PAYMENT_FAILED") }); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	got, err := st.Apply(ctx, o.ID, "d", func(o *domain.Order) error {
		if o.Status != domain.StatusConfirmed {
			return nil // mirrors saga.OnShipmentDelivered
		}
		return o.Fulfill()
	})
	if err != nil {
		t.Fatalf("apply on cancelled: %v", err)
	}
	if got.Status != domain.StatusCancelled {
		t.Fatalf("status = %s, want CANCELLED (unchanged)", got.Status)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic='commerce.order.fulfilled'`).Scan(&n)
	if n != 0 {
		t.Fatalf("order.fulfilled should not have been emitted, got %d", n)
	}
}

// saga.OnShipmentDelivered moves a CONFIRMED order to FULFILLED and is
// idempotent; nil downstream clients are fine — it only touches the store.
func TestSaga_OnShipmentDelivered(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	orch := saga.New(st, saga.Clients{})
	o := seedPending(t, st)
	if _, err := st.Apply(ctx, o.ID, "c", func(o *domain.Order) error { return o.Confirm() }); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	if err := orch.OnShipmentDelivered(ctx, "deliver-1", o.ID); err != nil {
		t.Fatalf("OnShipmentDelivered: %v", err)
	}
	got, _ := st.Get(ctx, o.ID, "owner-1")
	if got.Status != domain.StatusFulfilled {
		t.Fatalf("status = %s, want FULFILLED", got.Status)
	}
	// Duplicate delivery event -> no-op, no error.
	if err := orch.OnShipmentDelivered(ctx, "deliver-1", o.ID); err != nil {
		t.Fatalf("duplicate delivery: %v", err)
	}

	// A delivery for an order that never confirmed is ignored (stays put).
	pend := seedPending(t, st)
	if err := orch.OnShipmentDelivered(ctx, "deliver-pend", pend.ID); err != nil {
		t.Fatalf("delivery on pending order: %v", err)
	}
	got2, _ := st.Get(ctx, pend.ID, "owner-1")
	if got2.Status != domain.StatusPendingPayment {
		t.Fatalf("pending order changed on delivery: %s", got2.Status)
	}
}

// The order consumer routes a fulfillment.delivered record into the saga.
func TestConsumer_DispatchesShipmentDelivered(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	h := consumer.Handler(saga.New(st, saga.Clients{}))
	o := seedPending(t, st)
	if _, err := st.Apply(ctx, o.ID, "c", func(o *domain.Order) error { return o.Confirm() }); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	payload, _ := proto.Marshal(&fulfillmentv1.ShipmentDelivered{ShipmentId: "sh-1", OrderId: o.ID, OwnerId: "owner-1"})
	rec := &kgo.Record{Topic: kafka.Topic("fulfillment", "delivered"), Partition: 0, Offset: 3, Value: payload}
	if err := h(ctx, rec); err != nil {
		t.Fatalf("handle delivered: %v", err)
	}

	got, _ := st.Get(ctx, o.ID, "owner-1")
	if got.Status != domain.StatusFulfilled {
		t.Fatalf("consumer did not fulfil the order: %s", got.Status)
	}

	// Topic list and undecodable-payload handling.
	if len(consumer.Topics()) == 0 {
		t.Fatal("Topics() is empty")
	}
	bad := &kgo.Record{Topic: kafka.Topic("fulfillment", "delivered"), Partition: 0, Offset: 4, Value: []byte("not-proto")}
	if err := h(ctx, bad); err != nil {
		t.Fatalf("undecodable record should be skipped, got %v", err)
	}
}
