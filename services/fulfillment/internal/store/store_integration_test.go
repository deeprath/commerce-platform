package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/protobuf/proto"

	fulfillmentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/fulfillment/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/pgx"
	"github.com/deeprath/commerce-platform/services/fulfillment/internal/domain"
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

func addr() domain.Address {
	return domain.Address{FullName: "Test User", Line1: "1 Main St", City: "Springfield", Region: "IL", PostalCode: "62701", CountryCode: "US"}
}

func items() []domain.Item {
	return []domain.Item{{ProductID: "p1", Title: "Desk Lamp", Quantity: 2}}
}

// groups is the single first-party shop group most tests seed with — one
// shipment, shop_id "".
func groups() []store.ShopItems {
	return []store.ShopItems{{Items: items()}}
}

func TestCreateFromOrder_EnrichmentIdempotencyAndOutbox(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)

	created, err := st.CreateFromOrder(ctx, "order-1", "owner-1", addr(), groups(), "commerce.order.confirmed:0:1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("want exactly 1 shipment, got %+v", created)
	}
	sh := created[0]
	if sh.Status != domain.StatusPending || sh.OrderID != "order-1" || sh.OwnerID != "owner-1" {
		t.Fatalf("bad shipment: %+v", sh)
	}
	if len(sh.Items) != 1 || sh.Items[0].Title != "Desk Lamp" || sh.ShipTo.City != "Springfield" {
		t.Fatalf("ship_to / items not persisted: %+v", sh)
	}

	// Same event id again -> short-circuit, no new shipment, no extra outbox row.
	dup, err := st.CreateFromOrder(ctx, "order-1", "owner-1", addr(), groups(), "commerce.order.confirmed:0:1")
	if err != nil || dup != nil {
		t.Fatalf("duplicate event id should short-circuit: %+v %v", dup, err)
	}

	// New event id, same order+shop -> that group is skipped (already exists),
	// so nothing new is returned and still no 2nd created event.
	again, err := st.CreateFromOrder(ctx, "order-1", "owner-1", addr(), groups(), "commerce.order.confirmed:0:9")
	if err != nil {
		t.Fatalf("redelivered event: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("redelivery of an existing (order,shop) should create nothing, got %+v", again)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic='commerce.fulfillment.shipment_created'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 shipment_created event, got %d", n)
	}
}

// A marketplace order spanning two shops (plus a first-party group) produces
// one shipment per group, each with only that group's items, in one atomic
// CreateFromOrder call.
func TestCreateFromOrder_SplitsByShopGroup(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)

	created, err := st.CreateFromOrder(ctx, "order-multi", "owner-1", addr(), []store.ShopItems{
		{ShopID: "", Items: []domain.Item{{ProductID: "fp", Title: "First Party", Quantity: 1}}},
		{ShopID: "shop-a", Items: []domain.Item{{ProductID: "a1", Title: "Shop A Item", Quantity: 1}}},
		{ShopID: "shop-b", Items: []domain.Item{{ProductID: "b1", Title: "Shop B Item", Quantity: 2}}},
	}, "e:multi:1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(created) != 3 {
		t.Fatalf("want 3 shipments (one per group), got %+v", created)
	}
	byShop := map[string]*domain.Shipment{}
	for _, sh := range created {
		if sh.OrderID != "order-multi" {
			t.Fatalf("shipment for the wrong order: %+v", sh)
		}
		byShop[sh.ShopID] = sh
	}
	if len(byShop["shop-a"].Items) != 1 || byShop["shop-a"].Items[0].ProductID != "a1" {
		t.Fatalf("shop-a shipment has the wrong items: %+v", byShop["shop-a"])
	}
	if len(byShop["shop-b"].Items) != 1 || byShop["shop-b"].Items[0].ProductID != "b1" {
		t.Fatalf("shop-b shipment has the wrong items: %+v", byShop["shop-b"])
	}
	if len(byShop[""].Items) != 1 || byShop[""].Items[0].ProductID != "fp" {
		t.Fatalf("first-party shipment has the wrong items: %+v", byShop[""])
	}

	// One shipment_created event per group.
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM outbox WHERE topic='commerce.fulfillment.shipment_created' AND key=$1`,
		[]byte("order-multi")).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("expected 3 shipment_created events, got %d", n)
	}

	// Listing by order returns all three.
	all, _, err := st.List(ctx, "owner-1", "order-multi", "", 10, "")
	if err != nil || len(all) != 3 {
		t.Fatalf("List by order = %v, n=%d", err, len(all))
	}
}

// A partially-processed redelivery (e.g. a crash after one group's insert
// committed under an earlier offset) only creates the groups still missing.
func TestCreateFromOrder_PartialRedeliverySkipsExistingGroups(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)

	first, err := st.CreateFromOrder(ctx, "order-partial", "owner-1", addr(), []store.ShopItems{
		{ShopID: "shop-a", Items: items()},
	}, "e:partial:1")
	if err != nil || len(first) != 1 {
		t.Fatalf("seed shop-a: %v %+v", err, first)
	}

	// A later delivery (different offset) carries both groups; shop-a already
	// exists and is skipped, shop-b is newly created.
	second, err := st.CreateFromOrder(ctx, "order-partial", "owner-1", addr(), []store.ShopItems{
		{ShopID: "shop-a", Items: items()},
		{ShopID: "shop-b", Items: items()},
	}, "e:partial:2")
	if err != nil {
		t.Fatalf("redeliver: %v", err)
	}
	if len(second) != 1 || second[0].ShopID != "shop-b" {
		t.Fatalf("want only the new shop-b shipment, got %+v", second)
	}

	all, _, err := st.List(ctx, "owner-1", "order-partial", "", 10, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("List by order = %v, n=%d, want 2 total shipments", err, len(all))
	}
}

func TestTransition_StateMachineAndEvents(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)

	created, err := st.CreateFromOrder(ctx, "order-2", "owner-2", addr(), groups(), "e:0:1")
	if err != nil || len(created) != 1 {
		t.Fatalf("seed: %v %+v", err, created)
	}
	sh := created[0]

	shipped, err := st.Transition(ctx, sh.ID, domain.StatusShipped, "UPS", "1Z1", "")
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	if shipped.Status != domain.StatusShipped || shipped.Carrier != "UPS" || shipped.ShippedAt == nil {
		t.Fatalf("bad shipped shipment: %+v", shipped)
	}

	// Idempotent.
	if _, err := st.Transition(ctx, sh.ID, domain.StatusShipped, "", "", ""); err != nil {
		t.Fatalf("idempotent ship: %v", err)
	}

	delivered, err := st.Transition(ctx, sh.ID, domain.StatusDelivered, "", "", "")
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if delivered.Status != domain.StatusDelivered || delivered.DeliveredAt == nil {
		t.Fatalf("bad delivered shipment: %+v", delivered)
	}

	// A delivered shipment cannot be cancelled.
	if _, err := st.Transition(ctx, sh.ID, domain.StatusCancelled, "", "", "x"); err == nil {
		t.Fatalf("cancel after delivery should fail")
	}

	// The outbox carries the whole lifecycle, in order, keyed by the order id,
	// and the delivered payload decodes.
	topics := outboxTopics(t, pool, "order-2")
	want := []string{"commerce.fulfillment.shipment_created", "commerce.fulfillment.shipped", "commerce.fulfillment.delivered"}
	if len(topics) != len(want) {
		t.Fatalf("outbox topics = %v, want %v", topics, want)
	}
	for i := range want {
		if topics[i] != want[i] {
			t.Fatalf("outbox topics = %v, want %v", topics, want)
		}
	}
}

// outboxTopics returns the fulfillment.* outbox topics in id order, asserting
// each row is keyed by orderKey and that a delivered event decodes.
func outboxTopics(t *testing.T, pool *pgxpool.Pool, orderKey string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT topic, key, payload FROM outbox WHERE topic LIKE 'commerce.fulfillment.%' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var topics []string
	for rows.Next() {
		var topic string
		var key, payload []byte
		if err := rows.Scan(&topic, &key, &payload); err != nil {
			t.Fatal(err)
		}
		if string(key) != orderKey {
			t.Fatalf("event %s keyed by %q, want %q", topic, key, orderKey)
		}
		if topic == "commerce.fulfillment.delivered" {
			var e fulfillmentv1.ShipmentDelivered
			if err := proto.Unmarshal(payload, &e); err != nil || e.GetOrderId() != orderKey {
				t.Fatalf("delivered payload bad: %v %+v", err, &e)
			}
		}
		topics = append(topics, topic)
	}
	return topics
}

func TestDueForAdvance(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)

	if _, err := st.CreateFromOrder(ctx, "order-3", "owner-3", addr(), groups(), "e:1:1"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Nothing is due with a long threshold.
	due, err := st.DueForAdvance(ctx, time.Hour, time.Hour, 10)
	if err != nil {
		t.Fatalf("DueForAdvance: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("nothing should be due yet, got %v", due)
	}

	// With a zero threshold the PENDING shipment is due -> SHIPPED.
	due, err = st.DueForAdvance(ctx, 0, 0, 10)
	if err != nil {
		t.Fatalf("DueForAdvance: %v", err)
	}
	if len(due) != 1 || due[0].To != domain.StatusShipped {
		t.Fatalf("want one PENDING->SHIPPED target, got %+v", due)
	}
}

func TestStoreEdgePaths(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	const missing = "00000000-0000-0000-0000-000000000000"

	if _, err := st.Get(ctx, missing, ""); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("Get(missing): want NotFound, got %v", err)
	}
	if _, err := st.Transition(ctx, missing, domain.StatusShipped, "", "", ""); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("Transition(missing): want NotFound, got %v", err)
	}

	created, err := st.CreateFromOrder(ctx, "edge-1", "owner-e", addr(), groups(), "") // empty eventID branch
	if err != nil || len(created) != 1 {
		t.Fatalf("create (no eventID): %v %+v", err, created)
	}
	sh := created[0]
	// Transition to the status it is already in -> idempotent no-op.
	same, err := st.Transition(ctx, sh.ID, domain.StatusPending, "", "", "")
	if err != nil || same.Status != domain.StatusPending {
		t.Fatalf("idempotent same-status transition: %v %+v", err, same)
	}
	// Unknown target status -> InvalidArgument.
	if _, err := st.Transition(ctx, sh.ID, domain.Status("BOGUS"), "", "", ""); !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("Transition(bogus): want InvalidArgument, got %v", err)
	}
	// Cross-owner Get is a NotFound, not a leak.
	if _, err := st.Get(ctx, sh.ID, "someone-else"); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("Get cross-owner: want NotFound, got %v", err)
	}
}

func TestListPagination(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	for i := 0; i < 3; i++ {
		if _, err := st.CreateFromOrder(ctx, "pg-"+string(rune('a'+i)), "pager", addr(), groups(), ""); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
		time.Sleep(2 * time.Millisecond) // distinct created_at for a stable keyset
	}

	page1, next, err := st.List(ctx, "pager", "", "", 2, "")
	if err != nil || len(page1) != 2 || next == "" {
		t.Fatalf("page 1: n=%d next=%q err=%v", len(page1), next, err)
	}
	page2, next2, err := st.List(ctx, "pager", "", "", 2, next)
	if err != nil || len(page2) != 1 || next2 != "" {
		t.Fatalf("page 2: n=%d next=%q err=%v", len(page2), next2, err)
	}
	// Filtering by order id narrows to one.
	one, _, err := st.List(ctx, "pager", "pg-b", "", 10, "")
	if err != nil || len(one) != 1 || one[0].OrderID != "pg-b" {
		t.Fatalf("filtered list: %v %+v", err, one)
	}

	// A shipment for another owner + one advanced to SHIPPED, for the operator view.
	if _, err := st.CreateFromOrder(ctx, "pg-x", "other", addr(), groups(), ""); err != nil {
		t.Fatalf("seed other: %v", err)
	}
	shx, err := st.CreateFromOrder(ctx, "pg-y", "pager", addr(), groups(), "")
	if err != nil || len(shx) != 1 {
		t.Fatalf("seed pg-y: %v %+v", err, shx)
	}
	if _, err := st.Transition(ctx, shx[0].ID, domain.StatusShipped, "UPS", "1Z", ""); err != nil {
		t.Fatalf("ship: %v", err)
	}

	// Operator mode ("" owner): sees every owner's shipments; status filters.
	all, _, err := st.List(ctx, "", "", "", 50, "")
	if err != nil || len(all) != 5 {
		t.Fatalf("operator list: %v n=%d", err, len(all))
	}
	shipped, _, _ := st.List(ctx, "", "", "SHIPPED", 50, "")
	if len(shipped) != 1 || shipped[0].OrderID != "pg-y" {
		t.Fatalf("status filter: %+v", shipped)
	}
}
