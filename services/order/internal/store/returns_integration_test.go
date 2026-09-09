package store_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/order/internal/domain"
	"github.com/deeprath/commerce-platform/services/order/internal/store"
)

func usd(c int64) domain.Money { return domain.Money{Currency: "USD", Cents: c} }

func newReturn(orderID string) *domain.Return {
	return &domain.Return{
		ID: "", OrderID: orderID, OwnerID: "owner-1", Status: domain.ReturnRequested,
		Reason: "changed my mind",
		Lines: []domain.ReturnLine{
			{ProductID: "p1", Quantity: 2, RefundAmount: usd(6998)},
		},
		RefundTotal: usd(7558),
	}
}

func TestInsertGetListReturn(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	o := seedPending(t, st)

	r := newReturn(o.ID)
	r.ID = "11111111-1111-1111-1111-111111111111"
	if err := st.InsertReturn(ctx, r); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := st.GetReturn(ctx, r.ID, "owner-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != domain.ReturnRequested || got.RefundTotal.Cents != 7558 || len(got.Lines) != 1 || got.Lines[0].Quantity != 2 {
		t.Fatalf("bad return: %+v", got)
	}
	// Owner scoping.
	if _, err := st.GetReturn(ctx, r.ID, "someone-else"); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("cross-owner get: want NotFound, got %v", err)
	}

	list, next, err := st.ListReturns(ctx, "owner-1", 10, "")
	if err != nil || len(list) != 1 || next != "" {
		t.Fatalf("list: %v n=%d next=%q", err, len(list), next)
	}

	// return_requested event on the outbox.
	var b []byte
	_ = pool.QueryRow(ctx, `SELECT payload FROM outbox WHERE topic='commerce.order.return_requested'`).Scan(&b)
	var e orderv1.ReturnRequested
	if err := proto.Unmarshal(b, &e); err != nil || e.GetReturnId() != r.ID || e.GetOrderId() != o.ID {
		t.Fatalf("return_requested payload: %v %+v", err, &e)
	}
}

func TestListReturns_Pagination(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	o := seedPending(t, st)

	ids := []string{
		"a1111111-1111-1111-1111-111111111111",
		"a2222222-2222-2222-2222-222222222222",
		"a3333333-3333-3333-3333-333333333333",
	}
	for _, id := range ids {
		r := newReturn(o.ID)
		r.ID = id
		if err := st.InsertReturn(ctx, r); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	p1, next, err := st.ListReturns(ctx, "owner-1", 2, "")
	if err != nil || len(p1) != 2 || next == "" {
		t.Fatalf("page 1: n=%d next=%q err=%v", len(p1), next, err)
	}
	p2, next2, err := st.ListReturns(ctx, "owner-1", 2, next)
	if err != nil || len(p2) != 1 || next2 != "" {
		t.Fatalf("page 2: n=%d next=%q err=%v", len(p2), next2, err)
	}
	// Newest first, and every returned row carries its lines.
	if !p1[0].CreatedAt.After(p1[1].CreatedAt) || len(p1[0].Lines) == 0 {
		t.Fatalf("ordering / line hydration wrong: %+v", p1)
	}
	// Another owner sees nothing.
	other, _, _ := st.ListReturns(ctx, "nobody", 10, "")
	if len(other) != 0 {
		t.Fatalf("cross-owner list not empty")
	}
}

func TestGetReturn_MultiLineHydration(t *testing.T) {
	ctx := context.Background()
	st := store.New(spinUp(t))
	o := seedPending(t, st)

	r := newReturn(o.ID)
	r.ID = "b1111111-1111-1111-1111-111111111111"
	r.Lines = []domain.ReturnLine{
		{ProductID: "p1", Quantity: 2, RefundAmount: usd(6998)},
		{ProductID: "p2", Quantity: 1, RefundAmount: usd(1200)},
	}
	r.RefundTotal = usd(8198)
	if err := st.InsertReturn(ctx, r); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := st.GetReturn(ctx, r.ID, "")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Lines) != 2 || got.Lines[0].ProductID != "p1" || got.Lines[1].RefundAmount.Cents != 1200 {
		t.Fatalf("multi-line hydration: %+v", got.Lines)
	}
	if _, err := st.GetReturn(ctx, "00000000-0000-0000-0000-000000000000", ""); !errs.Is(err, errs.KindNotFound) {
		t.Fatalf("missing return: want NotFound, got %v", err)
	}
}

func TestReturnedQtyExcludesRejected(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	o := seedPending(t, st)

	r1 := newReturn(o.ID)
	r1.ID = "22222222-2222-2222-2222-222222222222"
	r1.Lines = []domain.ReturnLine{{ProductID: "p1", Quantity: 1, RefundAmount: usd(3499)}}
	if err := st.InsertReturn(ctx, r1); err != nil {
		t.Fatalf("insert r1: %v", err)
	}
	r2 := newReturn(o.ID)
	r2.ID = "33333333-3333-3333-3333-333333333333"
	r2.Lines = []domain.ReturnLine{{ProductID: "p1", Quantity: 1, RefundAmount: usd(3499)}}
	if err := st.InsertReturn(ctx, r2); err != nil {
		t.Fatalf("insert r2: %v", err)
	}

	if n, _ := st.ReturnedQty(ctx, o.ID, "p1"); n != 2 {
		t.Fatalf("ReturnedQty = %d, want 2 (both count while not rejected)", n)
	}
	if _, err := st.DecideReturn(ctx, r2.ID, "op", false, "denied"); err != nil {
		t.Fatalf("reject r2: %v", err)
	}
	if n, _ := st.ReturnedQty(ctx, o.ID, "p1"); n != 1 {
		t.Fatalf("ReturnedQty after reject = %d, want 1", n)
	}
}

func TestDecideReturn_IdempotentEventsAndRestockFlags(t *testing.T) {
	ctx := context.Background()
	pool := spinUp(t)
	st := store.New(pool)
	o := seedPending(t, st)

	r := newReturn(o.ID)
	r.ID = "44444444-4444-4444-4444-444444444444"
	if err := st.InsertReturn(ctx, r); err != nil {
		t.Fatalf("insert: %v", err)
	}

	appr, err := st.DecideReturn(ctx, r.ID, "op-1", true, "approved")
	if err != nil || appr.Status != domain.ReturnApproved || appr.DecidedBy != "op-1" {
		t.Fatalf("approve: %v %+v", err, appr)
	}
	// Deciding the same way again is a no-op (no second event).
	if _, err := st.DecideReturn(ctx, r.ID, "op-2", true, "again"); err != nil {
		t.Fatalf("re-approve: %v", err)
	}
	// Flipping an approved return is refused.
	if _, err := st.DecideReturn(ctx, r.ID, "op-1", false, ""); !errs.Is(err, errs.KindFailedPrecondition) {
		t.Fatalf("approve->reject: want FailedPrecondition, got %v", err)
	}

	var reqN, apprN int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic='commerce.order.return_requested'`).Scan(&reqN)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE topic='commerce.order.return_approved'`).Scan(&apprN)
	if reqN != 1 || apprN != 1 {
		t.Fatalf("events: requested=%d approved=%d", reqN, apprN)
	}

	// Restock flags: all lines pending, mark one, it drops out.
	pending, _ := st.UnrestockedLines(ctx, r.ID)
	if len(pending) != 1 {
		t.Fatalf("pending lines = %d, want 1", len(pending))
	}
	if err := st.MarkRestocked(ctx, r.ID, "p1"); err != nil {
		t.Fatalf("mark restocked: %v", err)
	}
	if again, _ := st.UnrestockedLines(ctx, r.ID); len(again) != 0 {
		t.Fatalf("still pending after mark: %d", len(again))
	}
}
