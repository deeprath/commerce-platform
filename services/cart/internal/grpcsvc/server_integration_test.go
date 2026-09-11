package grpcsvc_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	cartv1 "github.com/deeprath/commerce-platform/gen/go/commerce/cart/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/cart/internal/grpcsvc"
	"github.com/deeprath/commerce-platform/services/cart/internal/store"
)

// spinUp starts a throwaway Redis and returns a connected client.
func spinUp(t *testing.T) *redis.Client {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; skipped with -short")
	}
	ctx := context.Background()
	c, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		t.Fatalf("start redis: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	uri, err := c.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	opts, err := redis.ParseURL(uri)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func newSrv(t *testing.T) *grpcsvc.Server {
	return grpcsvc.New(store.New(spinUp(t), time.Hour))
}

func productID(t *testing.T) string {
	t.Helper()
	return uuid.NewString()
}

func TestGetCart_EmptyCartIsNotFoundInRedisButNotAnError(t *testing.T) {
	ctx := context.Background()
	s := newSrv(t)
	c, err := s.GetCart(ctx, &cartv1.GetCartRequest{CartId: "c1"})
	if err != nil {
		t.Fatalf("GetCart: %v", err)
	}
	if c.GetId() != "c1" || len(c.GetItems()) != 0 || c.GetTotalQuantity() != 0 {
		t.Fatalf("cart = %+v", c)
	}
}

func TestGetCart_RequiresACartID(t *testing.T) {
	s := newSrv(t)
	_, err := s.GetCart(context.Background(), &cartv1.GetCartRequest{})
	if !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("err = %v, want KindInvalidArgument", err)
	}
}

func TestAddItem_PersistsAndAccumulates(t *testing.T) {
	ctx := context.Background()
	s := newSrv(t)
	pid := productID(t)

	c, err := s.AddItem(ctx, &cartv1.AddItemRequest{CartId: "c1", ProductId: pid, Quantity: 2})
	if err != nil {
		t.Fatalf("AddItem: %v", err)
	}
	if c.GetTotalQuantity() != 2 || len(c.GetItems()) != 1 {
		t.Fatalf("cart after first add = %+v", c)
	}

	// A second Add for the same product accumulates onto the existing line,
	// and this is read back from Redis, not from in-memory state.
	c, err = s.AddItem(ctx, &cartv1.AddItemRequest{CartId: "c1", ProductId: pid, Quantity: 3})
	if err != nil {
		t.Fatalf("AddItem again: %v", err)
	}
	if c.GetTotalQuantity() != 5 || len(c.GetItems()) != 1 {
		t.Fatalf("cart after second add = %+v", c)
	}

	got, err := s.GetCart(ctx, &cartv1.GetCartRequest{CartId: "c1"})
	if err != nil {
		t.Fatalf("GetCart: %v", err)
	}
	if got.GetTotalQuantity() != 5 {
		t.Fatalf("re-fetched cart total = %d, want 5", got.GetTotalQuantity())
	}
}

func TestAddItem_RejectsANonUUIDProductID(t *testing.T) {
	// This is the exact input class the ZAP authenticated scan's SQLi
	// heuristic tripped on before checkProductID existed (docs/SECURITY.md
	// §8, ZAP-40018) — a scanner probe string like "1 OR 1=1 --" must be
	// rejected as InvalidArgument, not silently accepted as a new line.
	s := newSrv(t)
	_, err := s.AddItem(context.Background(), &cartv1.AddItemRequest{
		CartId: "c1", ProductId: "1 OR 1=1 --", Quantity: 1,
	})
	if !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("err = %v, want KindInvalidArgument", err)
	}
}

func TestAddItem_RequiresACartID(t *testing.T) {
	s := newSrv(t)
	_, err := s.AddItem(context.Background(), &cartv1.AddItemRequest{ProductId: productID(t), Quantity: 1})
	if !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("err = %v, want KindInvalidArgument", err)
	}
}

func TestSetItemQuantity_ZeroRemovesTheLine(t *testing.T) {
	ctx := context.Background()
	s := newSrv(t)
	pid := productID(t)
	if _, err := s.AddItem(ctx, &cartv1.AddItemRequest{CartId: "c1", ProductId: pid, Quantity: 4}); err != nil {
		t.Fatal(err)
	}

	c, err := s.SetItemQuantity(ctx, &cartv1.SetItemQuantityRequest{CartId: "c1", ProductId: pid, Quantity: 0})
	if err != nil {
		t.Fatalf("SetItemQuantity: %v", err)
	}
	if len(c.GetItems()) != 0 {
		t.Fatalf("items = %+v, want none after setting quantity 0", c.GetItems())
	}
}

func TestRemoveItem(t *testing.T) {
	ctx := context.Background()
	s := newSrv(t)
	pid := productID(t)
	if _, err := s.AddItem(ctx, &cartv1.AddItemRequest{CartId: "c1", ProductId: pid, Quantity: 1}); err != nil {
		t.Fatal(err)
	}

	c, err := s.RemoveItem(ctx, &cartv1.RemoveItemRequest{CartId: "c1", ProductId: pid})
	if err != nil {
		t.Fatalf("RemoveItem: %v", err)
	}
	if len(c.GetItems()) != 0 {
		t.Fatalf("items = %+v, want none", c.GetItems())
	}
}

func TestClear(t *testing.T) {
	ctx := context.Background()
	s := newSrv(t)
	if _, err := s.AddItem(ctx, &cartv1.AddItemRequest{CartId: "c1", ProductId: productID(t), Quantity: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddItem(ctx, &cartv1.AddItemRequest{CartId: "c1", ProductId: productID(t), Quantity: 2}); err != nil {
		t.Fatal(err)
	}

	c, err := s.Clear(ctx, &cartv1.ClearRequest{CartId: "c1"})
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if len(c.GetItems()) != 0 || c.GetTotalQuantity() != 0 {
		t.Fatalf("cart after Clear = %+v", c)
	}
}

func TestMerge_CombinesLinesAndDeletesTheSourceCart(t *testing.T) {
	ctx := context.Background()
	s := newSrv(t)
	shared := productID(t)
	guestOnly := productID(t)

	if _, err := s.AddItem(ctx, &cartv1.AddItemRequest{CartId: "guest", ProductId: shared, Quantity: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddItem(ctx, &cartv1.AddItemRequest{CartId: "guest", ProductId: guestOnly, Quantity: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddItem(ctx, &cartv1.AddItemRequest{CartId: "user", ProductId: shared, Quantity: 3}); err != nil {
		t.Fatal(err)
	}

	merged, err := s.Merge(ctx, &cartv1.MergeRequest{FromCartId: "guest", ToCartId: "user"})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(merged.GetItems()) != 2 {
		t.Fatalf("merged items = %+v, want 2 distinct products", merged.GetItems())
	}
	if merged.GetTotalQuantity() != 6 { // shared: 2+3, guestOnly: 1
		t.Fatalf("merged total = %d, want 6", merged.GetTotalQuantity())
	}

	// The source (guest) cart is gone.
	guestAfter, err := s.GetCart(ctx, &cartv1.GetCartRequest{CartId: "guest"})
	if err != nil {
		t.Fatalf("GetCart(guest): %v", err)
	}
	if len(guestAfter.GetItems()) != 0 {
		t.Fatalf("guest cart should be deleted after merge, got %+v", guestAfter.GetItems())
	}
}

func TestSetItemQuantity_RejectsANonUUIDProductID(t *testing.T) {
	s := newSrv(t)
	_, err := s.SetItemQuantity(context.Background(), &cartv1.SetItemQuantityRequest{
		CartId: "c1", ProductId: "not-a-uuid", Quantity: 1,
	})
	if !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("err = %v, want KindInvalidArgument", err)
	}
}

func TestSetItemQuantity_RequiresACartID(t *testing.T) {
	s := newSrv(t)
	_, err := s.SetItemQuantity(context.Background(), &cartv1.SetItemQuantityRequest{ProductId: productID(t), Quantity: 1})
	if !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("err = %v, want KindInvalidArgument", err)
	}
}

func TestRemoveItem_RequiresACartID(t *testing.T) {
	s := newSrv(t)
	_, err := s.RemoveItem(context.Background(), &cartv1.RemoveItemRequest{ProductId: productID(t)})
	if !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("err = %v, want KindInvalidArgument", err)
	}
}

func TestClear_RequiresACartID(t *testing.T) {
	s := newSrv(t)
	_, err := s.Clear(context.Background(), &cartv1.ClearRequest{})
	if !errs.Is(err, errs.KindInvalidArgument) {
		t.Fatalf("err = %v, want KindInvalidArgument", err)
	}
}

func TestMerge_RequiresBothCartIDs(t *testing.T) {
	s := newSrv(t)
	for _, req := range []*cartv1.MergeRequest{
		{FromCartId: "", ToCartId: "user"},
		{FromCartId: "guest", ToCartId: ""},
	} {
		if _, err := s.Merge(context.Background(), req); !errs.Is(err, errs.KindInvalidArgument) {
			t.Fatalf("Merge(%+v): err = %v, want KindInvalidArgument", req, err)
		}
	}
}
