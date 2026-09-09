// Package store persists coupons for the pricing service.
package store

import (
	"context"
	"embed"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	pkgerrs "github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/pricing/internal/domain"
)

//go:embed migrations/*.sql
var Migrations embed.FS

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// GetCoupon returns the coupon for code, or NOT_FOUND.
func (s *Store) GetCoupon(ctx context.Context, code string) (*domain.Coupon, error) {
	var (
		c        domain.Coupon
		kind     string
		amount   int64
		currency string
		exp      *time.Time
	)
	err := s.pool.QueryRow(ctx, `
		SELECT code, kind, percent_off, amount_cents, currency, expires_at, max_uses, used_count
		FROM coupons WHERE code = $1`, code).
		Scan(&c.Code, &kind, &c.PercentOff, &amount, &currency, &exp, &c.MaxUses, &c.UsedCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, pkgerrs.New(pkgerrs.KindNotFound, "COUPON_UNKNOWN", "no such coupon")
	}
	if err != nil {
		return nil, pkgerrs.Wrap(err, pkgerrs.KindInternal, "DB_ERROR", "database error: "+err.Error())
	}
	c.Kind = domain.CouponKind(kind)
	c.AmountOff = domain.Money{Currency: currency, Cents: amount}
	if exp != nil {
		c.ExpiresAt = *exp
	}
	return &c, nil
}
