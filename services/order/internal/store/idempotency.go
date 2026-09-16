package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	pkgerrs "github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/order/internal/domain"
)

// StaleInFlightAfter is how long a claimed-but-uncommitted key is honoured
// before another attempt may take it over.
//
// It has to be comfortably longer than a checkout, which takes seconds: a key
// still in flight after this long means the process holding it died between
// claiming and committing, not that it is slow. Sized against the 15-minute
// reservation TTL, which already bounds how long a half-finished checkout can
// hold anything.
const StaleInFlightAfter = 15 * time.Minute

// Claim is the outcome of trying to claim an idempotency key.
//
// Exactly one of the three states is set. Claimed means this caller owns the
// checkout and should proceed; OrderID means a previous attempt with this key
// already produced an order and it should be returned as-is; InFlight means
// another attempt is running right now.
type Claim struct {
	Claimed  bool
	OrderID  string
	InFlight bool
}

// Fingerprint identifies the request a key was first used for, so a key reused
// for a *different* checkout is caught rather than silently answered with
// someone else's order.
func Fingerprint(ownerID, cartID, currency, coupon string, totalCents int64) string {
	h := sha256.New()
	for _, part := range []string{ownerID, cartID, currency, coupon} {
		h.Write([]byte(part))
		h.Write([]byte{0}) // separator, so "a"+"bc" and "ab"+"c" differ
	}
	h.Write([]byte(strconv.FormatInt(totalCents, 10)))
	return hex.EncodeToString(h.Sum(nil))
}

// ErrIdempotencyKeyReused is returned when a key is presented with a different
// request than the one it was claimed for. That is a client bug — the same key
// being reused across genuinely different checkouts — and answering it with the
// first order would be worse than failing.
var ErrIdempotencyKeyReused = errors.New("idempotency key reused for a different request")

// ClaimIdempotencyKey attempts to take ownership of key for this checkout.
//
// The claim is a single INSERT ... ON CONFLICT DO NOTHING, which is what makes
// concurrent attempts safe: the database decides the winner, so there is no
// read-then-write window for a second request to slip through. A check-then-act
// would let two simultaneous submits both conclude the key was free.
func (s *Store) ClaimIdempotencyKey(ctx context.Context, key, ownerID, fingerprint string) (Claim, error) {
	var claimed string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO idempotency_keys (key, owner_id, fingerprint, status)
		VALUES ($1, $2, $3, 'IN_FLIGHT')
		ON CONFLICT (key) DO NOTHING
		RETURNING key`, key, ownerID, fingerprint).Scan(&claimed)
	if err == nil {
		return Claim{Claimed: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Claim{}, wrap(err)
	}

	// The key already exists. Who owns it, and what happened to it?
	var (
		existingOwner, existingPrint, status string
		orderID                              *string
		createdAt                            time.Time
	)
	if err := s.pool.QueryRow(ctx, `
		SELECT owner_id, fingerprint, status, order_id, created_at
		  FROM idempotency_keys WHERE key = $1`, key,
	).Scan(&existingOwner, &existingPrint, &status, &orderID, &createdAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Reaped between the two statements. Treat as contention rather than
			// retrying here, so this stays a single round of decisions.
			return Claim{InFlight: true}, nil
		}
		return Claim{}, wrap(err)
	}

	// A key is scoped to the shopper who created it. Another owner presenting it
	// must never be handed the first owner's order.
	if existingOwner != ownerID || existingPrint != fingerprint {
		return Claim{}, ErrIdempotencyKeyReused
	}

	if status == "COMPLETED" && orderID != nil {
		return Claim{OrderID: *orderID}, nil
	}

	// In flight. If it has been that way long enough that the holder must be
	// gone, take it over; otherwise tell the caller to retry.
	if time.Since(createdAt) < StaleInFlightAfter {
		return Claim{InFlight: true}, nil
	}
	var takenOver string
	err = s.pool.QueryRow(ctx, `
		UPDATE idempotency_keys
		   SET created_at = now(), updated_at = now()
		 WHERE key = $1 AND status = 'IN_FLIGHT' AND created_at < now() - $2::interval
		 RETURNING key`, key, StaleInFlightAfter.String()).Scan(&takenOver)
	if errors.Is(err, pgx.ErrNoRows) {
		// Someone else took it over first.
		return Claim{InFlight: true}, nil
	}
	if err != nil {
		return Claim{}, wrap(err)
	}
	return Claim{Claimed: true}, nil
}

// ReleaseIdempotencyKey drops a claim whose checkout failed, so the shopper can
// genuinely retry rather than being told their order is still in flight.
func (s *Store) ReleaseIdempotencyKey(ctx context.Context, key string) error {
	if key == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`DELETE FROM idempotency_keys WHERE key = $1 AND status = 'IN_FLIGHT'`, key)
	return wrap(err)
}

// OrderForKey returns the order a completed key produced.
func (s *Store) OrderForKey(ctx context.Context, key string) (*domain.Order, error) {
	var orderID string
	err := s.pool.QueryRow(ctx,
		`SELECT order_id FROM idempotency_keys WHERE key = $1 AND status = 'COMPLETED'`, key).Scan(&orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, pkgerrs.New(pkgerrs.KindNotFound, "ORDER_NOT_FOUND", "no order for that key")
	}
	if err != nil {
		return nil, wrap(err)
	}
	return s.Get(ctx, orderID, "")
}

// ReapIdempotencyKeys deletes keys that are past their useful life: completed
// ones older than retain, and in-flight ones abandoned long enough that the
// attempt holding them is certainly gone.
func (s *Store) ReapIdempotencyKeys(ctx context.Context, retain time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM idempotency_keys
		 WHERE (status = 'COMPLETED' AND created_at < now() - $1::interval)
		    OR (status = 'IN_FLIGHT' AND created_at < now() - $2::interval)`,
		retain.String(), StaleInFlightAfter.String())
	if err != nil {
		return 0, wrap(err)
	}
	return tag.RowsAffected(), nil
}

// InsertWithKey persists the order and completes its idempotency key in one
// transaction.
//
// Both or neither: committing the order without closing the key would let a
// retry place a second one, and closing the key without the order would leave a
// key pointing at nothing. Falls back to a plain Insert when no key was given,
// so callers that do not send one behave exactly as before.
func (s *Store) InsertWithKey(ctx context.Context, o *domain.Order, key string) error {
	if key == "" {
		return s.Insert(ctx, o)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := insertOrderTx(ctx, tx, o); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE idempotency_keys
		   SET status = 'COMPLETED', order_id = $2, updated_at = now()
		 WHERE key = $1 AND status = 'IN_FLIGHT'`, key, o.ID)
	if err != nil {
		return wrap(err)
	}
	if tag.RowsAffected() == 0 {
		// The claim is gone — reaped, or taken over by another attempt that has
		// since committed. Rolling back is the safe answer: the caller's order
		// is abandoned rather than racing a second one to the same key.
		return pkgerrs.New(pkgerrs.KindConflict, "IDEMPOTENCY_CLAIM_LOST",
			"the checkout claim expired; retry")
	}
	return wrap(tx.Commit(ctx))
}
