package saga

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	inventoryv1 "github.com/deeprath/commerce-platform/gen/go/commerce/inventory/v1"
	paymentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payment/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/order/internal/domain"
)

// ReturnLineReq is one requested return line. Quantity 0 => all remaining
// returnable units of that product.
type ReturnLineReq struct {
	ProductID string
	Quantity  int32
}

// RequestReturn opens an RMA for a fulfilled order the caller owns. With no
// lines it returns every line's full remaining returnable quantity.
func (o *Orchestrator) RequestReturn(ctx context.Context, ownerID, orderID, reason string, reqLines []ReturnLineReq) (*domain.Return, error) {
	ord, err := o.store.Get(ctx, orderID, ownerID)
	if err != nil {
		return nil, err
	}
	if ord.Status != domain.StatusFulfilled {
		return nil, errs.New(errs.KindFailedPrecondition, "NOT_RETURNABLE",
			"only a delivered (FULFILLED) order can be returned")
	}
	if ord.Subtotal.Cents <= 0 {
		return nil, errs.New(errs.KindFailedPrecondition, "NOT_RETURNABLE", "order has no returnable value")
	}

	byProduct := map[string]domain.Line{}
	for _, l := range ord.Lines {
		byProduct[l.ProductID] = l
	}
	if len(reqLines) == 0 {
		for _, l := range ord.Lines {
			reqLines = append(reqLines, ReturnLineReq{ProductID: l.ProductID})
		}
	}

	// First resolve the quantity for each line and its pre-tax value.
	type resolved struct {
		productID string
		qty       int32
		value     int64 // unit_price * qty
	}
	var rl []resolved
	for _, req := range reqLines {
		ol, ok := byProduct[req.ProductID]
		if !ok {
			return nil, errs.New(errs.KindInvalidArgument, "LINE_NOT_IN_ORDER",
				"product "+req.ProductID+" is not on this order")
		}
		already, err := o.store.ReturnedQty(ctx, orderID, req.ProductID)
		if err != nil {
			return nil, err
		}
		returnable := ol.Quantity - already
		qty := req.Quantity
		if qty == 0 {
			qty = returnable
		}
		if qty <= 0 {
			continue // nothing left to return for this line
		}
		if qty > returnable {
			return nil, errs.New(errs.KindFailedPrecondition, "QTY_EXCEEDS_RETURNABLE",
				"requested quantity exceeds what is still returnable for "+req.ProductID)
		}
		rl = append(rl, resolved{req.ProductID, qty, ol.UnitPrice.Cents * int64(qty)})
	}
	if len(rl) == 0 {
		return nil, errs.New(errs.KindFailedPrecondition, "NOTHING_TO_RETURN",
			"every requested line has already been fully returned")
	}

	// Refund the customer's share of what they actually paid (order total, net
	// of any discount, incl. tax), proportioned by each line's pre-tax value.
	// Allocate by running cumulative rounding so the parts sum exactly to the
	// intended total (and to ord.Total for a whole-order return).
	var lines []domain.ReturnLine
	var totalCents, cumValue, cumRefund int64
	for _, r := range rl {
		cumValue += r.value
		newCum := ord.Total.Cents * cumValue / ord.Subtotal.Cents
		refund := newCum - cumRefund
		cumRefund = newCum
		lines = append(lines, domain.ReturnLine{
			ProductID:    r.productID,
			Quantity:     r.qty,
			RefundAmount: domain.Money{Currency: ord.Total.Currency, Cents: refund},
		})
		totalCents += refund
	}

	r := &domain.Return{
		ID: uuid.NewString(), OrderID: orderID, OwnerID: ownerID,
		Status: domain.ReturnRequested, Reason: reason, Lines: lines,
		RefundTotal: domain.Money{Currency: ord.Total.Currency, Cents: totalCents},
	}
	if err := o.store.InsertReturn(ctx, r); err != nil {
		return nil, err
	}
	return r, nil
}

// DecideReturn applies an operator's decision. On approval it refunds the
// returned amount and restocks the returned units; both are idempotent so a
// retry after a partial failure is safe.
func (o *Orchestrator) DecideReturn(ctx context.Context, decidedBy, returnID string, approve bool, note string) (*domain.Return, error) {
	r, err := o.store.DecideReturn(ctx, returnID, decidedBy, approve, note)
	if err != nil {
		return nil, err
	}
	if r.Status != domain.ReturnApproved {
		return r, nil
	}

	ord, err := o.store.Get(ctx, r.OrderID, "")
	if err != nil {
		return nil, err
	}

	if ord.PaymentID != "" && r.RefundTotal.Cents > 0 {
		u, n := r.RefundTotal.UnitsNanos()
		if _, err := o.cl.Payment.Refund(ctx, &paymentv1.RefundRequest{
			PaymentId:      ord.PaymentID,
			Amount:         &commonv1.Money{CurrencyCode: r.RefundTotal.Currency, Units: u, Nanos: n},
			IdempotencyKey: r.ID,
		}); err != nil {
			return nil, errs.Wrap(err, errs.KindUnavailable, "REFUND_FAILED",
				"return approved; refund failed and will be retried")
		}
	}

	pending, err := o.store.UnrestockedLines(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	for _, l := range pending {
		if _, err := o.cl.Inventory.AdjustStock(ctx, &inventoryv1.AdjustStockRequest{
			ProductId: l.ProductID, Delta: l.Quantity, Reason: "RETURN:" + r.ID,
		}); err != nil {
			return nil, errs.Wrap(err, errs.KindUnavailable, "RESTOCK_FAILED",
				"return approved; restock failed and will be retried")
		}
		if err := o.store.MarkRestocked(ctx, r.ID, l.ProductID); err != nil {
			return nil, err
		}
		slog.InfoContext(ctx, "restocked returned units",
			slog.String("return_id", r.ID), slog.String("product_id", l.ProductID), slog.Int("qty", int(l.Quantity)))
	}
	return r, nil
}
