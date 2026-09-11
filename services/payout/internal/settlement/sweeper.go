// Package settlement is the SANDBOX settlement sweep: a background loop that
// marks a PENDING payout PAID on a delay, standing in for a real payout-
// provider integration (e.g. Stripe Connect transfers). In production this
// loop is removed and the same transition is driven by the provider's payout
// webhook/reconciliation job.
package settlement

import (
	"context"
	"log/slog"
	"time"

	"github.com/deeprath/commerce-platform/services/payout/internal/store"
)

// Sweeper periodically settles due payouts.
type Sweeper struct {
	store        *store.Store
	sweepEvery   time.Duration
	pendingAfter time.Duration // PENDING -> PAID once this old
}

func New(st *store.Store, sweepEvery, pendingAfter time.Duration) *Sweeper {
	return &Sweeper{store: st, sweepEvery: sweepEvery, pendingAfter: pendingAfter}
}

// Run blocks until ctx is cancelled.
func (sw *Sweeper) Run(ctx context.Context) error {
	t := time.NewTicker(sw.sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			sw.sweep(ctx)
		}
	}
}

func (sw *Sweeper) sweep(ctx context.Context) {
	due, err := sw.store.DuePayoutIDs(ctx, sw.pendingAfter, 100)
	if err != nil {
		slog.ErrorContext(ctx, "settlement sweep query failed", slog.Any("err", err))
		return
	}
	for _, id := range due {
		if _, err := sw.store.MarkPaid(ctx, id); err != nil {
			slog.ErrorContext(ctx, "settlement mark-paid failed", slog.String("payout_id", id), slog.Any("err", err))
			continue
		}
		slog.InfoContext(ctx, "settlement paid payout", slog.String("payout_id", id))
	}
}
