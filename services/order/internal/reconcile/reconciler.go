// Package reconcile retries the saga compensations that did not complete.
//
// Compensation is best-effort at the call site by design: a cancellation must
// not be undone because the cleanup failed. But best-effort was previously also
// best-forgotten — a failed Release or Void was logged and dropped, and nothing
// looked at it again.
//
// A failed Release self-heals: inventory expires the reservation on its own
// sweep. A failed Void does not. It leaves the shopper's authorisation live on
// an order that no longer exists, with no retry and no way to find it later.
// That is the hole this closes.
//
// Not covered: the two CreateOrder paths that compensate before the order row
// exists. If the insert itself fails there is nothing to reconcile against, and
// the reservation is reclaimed by inventory's TTL while the payment
// authorisation is not. Closing that properly means persisting the order before
// opening the payment, which is a change to the saga's shape rather than to its
// cleanup, and is left alone here.
package reconcile

import (
	"context"
	"log/slog"
	"time"

	"github.com/deeprath/commerce-platform/services/order/internal/store"
)

// compensator is the slice of *saga.Orchestrator this needs. Narrowed to an
// interface both to keep the dependency one-way — the saga does not import the
// reconciler — and so the sweep is testable without standing up every
// downstream client the orchestrator holds.
type compensator interface {
	ReleaseReservation(ctx context.Context, orderID, reservationID string) error
	VoidPayment(ctx context.Context, orderID, paymentID string) error
}

// compensationLister is the slice of *store.Store the sweep uses, narrowed for
// the same reason: the sweep's decisions are worth testing on their own, and
// they do not need a database to be worth testing.
type compensationLister interface {
	PendingCompensations(ctx context.Context, settledFor time.Duration, limit int) ([]store.PendingCompensation, error)
}

// Sweeper periodically retries outstanding compensations.
type Sweeper struct {
	store      compensationLister
	saga       compensator
	sweepEvery time.Duration
	// settledFor is how long an order must have been cancelled before it is
	// considered abandoned rather than merely in progress. Compensation runs
	// inline on cancellation, so sweeping sooner would race it.
	settledFor time.Duration
	batch      int
}

func New(st compensationLister, c compensator, sweepEvery, settledFor time.Duration, batch int) *Sweeper {
	if sweepEvery <= 0 {
		sweepEvery = time.Minute
	}
	if settledFor <= 0 {
		settledFor = 5 * time.Minute
	}
	if batch <= 0 {
		batch = 100
	}
	return &Sweeper{store: st, saga: c, sweepEvery: sweepEvery, settledFor: settledFor, batch: batch}
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
			sw.Sweep(ctx)
		}
	}
}

// Sweep retries every outstanding compensation once. Exported so it can be
// driven directly by a test or an operator-triggered run rather than only by
// the ticker.
func (sw *Sweeper) Sweep(ctx context.Context) {
	pending, err := sw.store.PendingCompensations(ctx, sw.settledFor, sw.batch)
	if err != nil {
		slog.ErrorContext(ctx, "compensation sweep query failed", slog.Any("err", err))
		return
	}
	if len(pending) == 0 {
		return
	}
	slog.WarnContext(ctx, "retrying outstanding saga compensations",
		slog.Int("orders", len(pending)))

	for _, p := range pending {
		// Each compensation is independent: a reservation that will not release
		// must not stop the payment on the same order being voided, because the
		// payment is the one holding the shopper's money.
		if p.ReservationID != "" {
			if err := sw.saga.ReleaseReservation(ctx, p.OrderID, p.ReservationID); err != nil {
				slog.ErrorContext(ctx, "compensation retry: Release still failing",
					slog.String("order_id", p.OrderID),
					slog.String("reservation_id", p.ReservationID), slog.Any("err", err))
			} else {
				slog.InfoContext(ctx, "compensation retry: reservation released",
					slog.String("order_id", p.OrderID))
			}
		}
		if p.PaymentID != "" {
			if err := sw.saga.VoidPayment(ctx, p.OrderID, p.PaymentID); err != nil {
				slog.ErrorContext(ctx, "compensation retry: Void still failing — the authorisation is still live",
					slog.String("order_id", p.OrderID),
					slog.String("payment_id", p.PaymentID), slog.Any("err", err))
			} else {
				slog.InfoContext(ctx, "compensation retry: payment voided",
					slog.String("order_id", p.OrderID))
			}
		}
	}
}
