// Package store persists carts as JSON blobs in Redis with a sliding TTL.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	pkgerrs "github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/cart/internal/domain"
)

type Store struct {
	rdb *redis.Client
	ttl time.Duration
}

func New(rdb *redis.Client, ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	return &Store{rdb: rdb, ttl: ttl}
}

func key(cartID string) string { return "cart:" + cartID }

// Get returns the cart, or an empty one if it doesn't exist yet.
func (s *Store) Get(ctx context.Context, cartID string) (*domain.Cart, error) {
	b, err := s.rdb.Get(ctx, key(cartID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return domain.NewCart(cartID), nil
	}
	if err != nil {
		return nil, wrap(err)
	}
	var c domain.Cart
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, pkgerrs.Wrap(err, pkgerrs.KindInternal, "CART_DECODE", "stored cart is corrupt")
	}
	c.ID = cartID
	// Refresh the sliding TTL on read.
	_ = s.rdb.Expire(ctx, key(cartID), s.ttl).Err()
	return &c, nil
}

// Put writes the cart and (re)sets its TTL.
func (s *Store) Put(ctx context.Context, c *domain.Cart) error {
	b, err := json.Marshal(c)
	if err != nil {
		return pkgerrs.Wrap(err, pkgerrs.KindInternal, "CART_ENCODE", "cannot encode cart")
	}
	return wrap(s.rdb.Set(ctx, key(c.ID), b, s.ttl).Err())
}

// Delete removes a cart.
func (s *Store) Delete(ctx context.Context, cartID string) error {
	return wrap(s.rdb.Del(ctx, key(cartID)).Err())
}

func wrap(err error) error {
	if err == nil {
		return nil
	}
	return pkgerrs.Wrap(err, pkgerrs.KindUnavailable, "REDIS_ERROR", "cart store error: "+err.Error())
}
