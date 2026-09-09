// Package carrier is the SANDBOX carrier: a background loop that advances
// shipments PENDING -> SHIPPED -> DELIVERED on a delay, standing in for real
// carrier pickup/scan/delivery webhooks. In production this loop is removed and
// the same transitions are driven by a signature-verified carrier webhook.
package carrier

import (
	"context"
	"log/slog"
	"time"

	"github.com/deeprath/commerce-platform/services/fulfillment/internal/domain"
	"github.com/deeprath/commerce-platform/services/fulfillment/internal/store"
)

// Advancer periodically moves due shipments to their next state.
type Advancer struct {
	store        *store.Store
	sweepEvery   time.Duration
	pendingAfter time.Duration // PENDING -> SHIPPED once this old
	shippedAfter time.Duration // SHIPPED -> DELIVERED once shipped_at this old
}

func New(st *store.Store, sweepEvery, pendingAfter, shippedAfter time.Duration) *Advancer {
	return &Advancer{store: st, sweepEvery: sweepEvery, pendingAfter: pendingAfter, shippedAfter: shippedAfter}
}

// Run blocks until ctx is cancelled.
func (a *Advancer) Run(ctx context.Context) error {
	t := time.NewTicker(a.sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			a.sweep(ctx)
		}
	}
}

func (a *Advancer) sweep(ctx context.Context) {
	due, err := a.store.DueForAdvance(ctx, a.pendingAfter, a.shippedAfter, 100)
	if err != nil {
		slog.ErrorContext(ctx, "carrier sweep query failed", slog.Any("err", err))
		return
	}
	for _, d := range due {
		carrier, tracking := "", ""
		if d.To == domain.StatusShipped {
			carrier, tracking = "SANDBOX", "TRK-"+d.ID
		}
		if _, err := a.store.Transition(ctx, d.ID, d.To, carrier, tracking, ""); err != nil {
			slog.ErrorContext(ctx, "carrier advance failed",
				slog.String("shipment_id", d.ID), slog.String("to", string(d.To)), slog.Any("err", err))
			continue
		}
		slog.InfoContext(ctx, "carrier advanced shipment",
			slog.String("shipment_id", d.ID), slog.String("to", string(d.To)))
	}
}
