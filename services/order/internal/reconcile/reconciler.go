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

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/deeprath/commerce-platform/pkg/telemetry"
	"github.com/deeprath/commerce-platform/services/order/internal/store"
)

// A compensation that is still outstanding after a sweep is money or stock held
// against an order that no longer exists. These make that visible: `retries`
// says whether the reconciler is making progress, `outstanding` says whether
// anything is stuck regardless.
var (
	meter = telemetry.Meter("github.com/deeprath/commerce-platform/services/order/internal/reconcile")

	retries, _ = meter.Int64Counter("commerce.saga.compensation.retries",
		metric.WithDescription("Saga compensations retried by the reconciler, by kind and outcome."))

	outstanding, _ = meter.Int64Gauge("commerce.saga.compensation.outstanding",
		metric.WithDescription("Cancelled orders whose compensation has still not completed."))
)

func recordRetry(ctx context.Context, kind string, err error) {
	outcome := "ok"
	if err != nil {
		outcome = "failed"
	}
	retries.Add(ctx, 1, metric.WithAttributes(
		attribute.String("kind", kind),
		attribute.String("outcome", outcome),
	))
}

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

// keyReaper is the idempotency-key housekeeping the sweeper also drives.
type keyReaper interface {
	ReapIdempotencyKeys(ctx context.Context, retain time.Duration) (int64, error)
}

// Sweeper periodically retries outstanding compensations.
type Sweeper struct {
	store      compensationLister
	keys       keyReaper
	keyRetain  time.Duration
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

// WithKeyReaper adds idempotency-key housekeeping to the sweep. retain<=0 means
// 24h, long enough that a client retrying a checkout hours later still gets its
// original order rather than a second one.
func (sw *Sweeper) WithKeyReaper(k keyReaper, retain time.Duration) *Sweeper {
	if retain <= 0 {
		retain = 24 * time.Hour
	}
	sw.keys, sw.keyRetain = k, retain
	return sw
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
			if sw.keys != nil {
				sw.ReapKeys(ctx, sw.keyRetain)
			}
		}
	}
}

// ReapKeys deletes idempotency keys past their useful life. Runs on the same
// ticker as the compensation sweep rather than needing its own goroutine —
// both are periodic housekeeping over the same database.
func (sw *Sweeper) ReapKeys(ctx context.Context, retain time.Duration) {
	n, err := sw.keys.ReapIdempotencyKeys(ctx, retain)
	if err != nil {
		slog.ErrorContext(ctx, "idempotency key reap failed", slog.Any("err", err))
		return
	}
	if n > 0 {
		slog.InfoContext(ctx, "reaped expired idempotency keys", slog.Int64("count", n))
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
	// Recorded on every sweep, including the zero, so the gauge falls back to 0
	// once the backlog clears rather than staying at its last non-zero reading.
	outstanding.Record(ctx, int64(len(pending)))
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
			err := sw.saga.ReleaseReservation(ctx, p.OrderID, p.ReservationID)
			recordRetry(ctx, "release", err)
			if err != nil {
				slog.ErrorContext(ctx, "compensation retry: Release still failing",
					slog.String("order_id", p.OrderID),
					slog.String("reservation_id", p.ReservationID), slog.Any("err", err))
			} else {
				slog.InfoContext(ctx, "compensation retry: reservation released",
					slog.String("order_id", p.OrderID))
			}
		}
		if p.PaymentID != "" {
			err := sw.saga.VoidPayment(ctx, p.OrderID, p.PaymentID)
			recordRetry(ctx, "void", err)
			if err != nil {
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
