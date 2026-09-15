package store

import (
	"context"
	"time"
)

// PendingCompensation is a cancelled order whose compensation did not complete.
// Only the ids the reconciler needs to retry are loaded — a sweep should not pay
// for hydrating whole aggregates it will mostly not touch.
type PendingCompensation struct {
	OrderID string
	// ReservationID is set only when the reservation still needs releasing.
	ReservationID string
	// PaymentID is set only when the payment still needs voiding.
	PaymentID string
}

// MarkReservationReleased records that Inventory.Release succeeded for an order,
// so the reconciler stops retrying it.
func (s *Store) MarkReservationReleased(ctx context.Context, orderID string) error {
	if err := notFoundID(orderID, "ORDER_NOT_FOUND"); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE orders SET reservation_released_at = now() WHERE id = $1`, orderID)
	return wrap(err)
}

// MarkPaymentVoided records that Payment.Void succeeded for an order.
func (s *Store) MarkPaymentVoided(ctx context.Context, orderID string) error {
	if err := notFoundID(orderID, "ORDER_NOT_FOUND"); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE orders SET payment_voided_at = now() WHERE id = $1`, orderID)
	return wrap(err)
}

// PendingCompensations returns cancelled orders whose compensation is still
// outstanding and that have been settled for at least `settledFor`.
//
// The delay matters: compensation runs inline the moment an order is cancelled,
// so sweeping immediately would race that attempt and double up on every
// cancellation. Waiting means the reconciler only ever sees the ones that
// genuinely did not complete.
func (s *Store) PendingCompensations(ctx context.Context, settledFor time.Duration, limit int) ([]PendingCompensation, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id,
		       CASE WHEN reservation_released_at IS NULL THEN reservation_id ELSE '' END,
		       CASE WHEN payment_voided_at       IS NULL THEN payment_id     ELSE '' END
		  FROM orders
		 WHERE status = 'CANCELLED'
		   AND ((reservation_released_at IS NULL AND reservation_id <> '')
		     OR (payment_voided_at       IS NULL AND payment_id     <> ''))
		   AND updated_at < now() - $1::interval
		 ORDER BY updated_at
		 LIMIT $2`, settledFor.String(), limit)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()

	var out []PendingCompensation
	for rows.Next() {
		var p PendingCompensation
		if err := rows.Scan(&p.OrderID, &p.ReservationID, &p.PaymentID); err != nil {
			return nil, wrap(err)
		}
		// An order can be cancelled before it ever reserved or paid; there is
		// nothing to compensate for those, so don't hand them to the caller.
		if p.ReservationID == "" && p.PaymentID == "" {
			continue
		}
		out = append(out, p)
	}
	return out, wrap(rows.Err())
}
