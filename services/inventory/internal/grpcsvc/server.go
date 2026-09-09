// Package grpcsvc implements InventoryService on top of the store.
package grpcsvc

import (
	"context"
	"time"

	inventoryv1 "github.com/deeprath/commerce-platform/gen/go/commerce/inventory/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/grpcx"
	"github.com/deeprath/commerce-platform/services/inventory/internal/store"
)

const (
	roleInventoryManager = "inventory_manager"
	minTTL               = 60 * time.Second
	maxTTL               = 60 * time.Minute
	defaultTTL           = 15 * time.Minute
)

type Server struct {
	inventoryv1.UnimplementedInventoryServiceServer
	store *store.Store
}

func New(s *store.Store) *Server { return &Server{store: s} }

func (s *Server) CheckAvailability(ctx context.Context, req *inventoryv1.CheckAvailabilityRequest) (*inventoryv1.Availability, error) {
	levels, err := s.store.Levels(ctx, req.GetProductIds())
	if err != nil {
		return nil, err
	}
	out := &inventoryv1.Availability{}
	for _, id := range req.GetProductIds() {
		l := levels[id]
		out.Levels = append(out.Levels, &inventoryv1.StockLevel{
			ProductId: id, OnHand: int32(l.OnHand), Reserved: int32(l.Reserved), Available: int32(l.Available()),
		})
	}
	return out, nil
}

func (s *Server) Reserve(ctx context.Context, req *inventoryv1.ReserveRequest) (*inventoryv1.Reservation, error) {
	if req.GetOrderRef() == "" {
		return nil, errs.New(errs.KindInvalidArgument, "ORDER_REF_REQUIRED", "order_ref is required")
	}
	if len(req.GetItems()) == 0 {
		return nil, errs.New(errs.KindInvalidArgument, "NO_ITEMS", "at least one item is required")
	}
	lines := make([]store.Line, 0, len(req.GetItems()))
	for _, it := range req.GetItems() {
		if it.GetQuantity() <= 0 {
			return nil, errs.New(errs.KindInvalidArgument, "BAD_QUANTITY", "quantity must be > 0")
		}
		lines = append(lines, store.Line{ProductID: it.GetProductId(), Quantity: int(it.GetQuantity())})
	}

	ttl := time.Duration(req.GetTtlSeconds()) * time.Second
	switch {
	case ttl == 0:
		ttl = defaultTTL
	case ttl < minTTL:
		ttl = minTTL
	case ttl > maxTTL:
		ttl = maxTTL
	}

	id, expires, err := s.store.Reserve(ctx, req.GetOrderRef(), lines, ttl)
	if err != nil {
		return nil, err
	}
	return &inventoryv1.Reservation{ReservationId: id, ExpiresAt: expires.Format(time.RFC3339)}, nil
}

func (s *Server) Commit(ctx context.Context, req *inventoryv1.CommitRequest) (*inventoryv1.CommitResponse, error) {
	if err := s.store.Commit(ctx, req.GetReservationId()); err != nil {
		return nil, err
	}
	return &inventoryv1.CommitResponse{}, nil
}

func (s *Server) Release(ctx context.Context, req *inventoryv1.ReleaseRequest) (*inventoryv1.ReleaseResponse, error) {
	if err := s.store.Release(ctx, req.GetReservationId()); err != nil {
		return nil, err
	}
	return &inventoryv1.ReleaseResponse{}, nil
}

func (s *Server) AdjustStock(ctx context.Context, req *inventoryv1.AdjustStockRequest) (*inventoryv1.StockLevel, error) {
	if err := grpcx.RequireRole(ctx, roleInventoryManager); err != nil {
		return nil, err
	}
	if req.GetProductId() == "" {
		return nil, errs.New(errs.KindInvalidArgument, "PRODUCT_ID_REQUIRED", "product_id is required")
	}
	l, err := s.store.AdjustStock(ctx, req.GetProductId(), int(req.GetDelta()))
	if err != nil {
		return nil, err
	}
	return &inventoryv1.StockLevel{
		ProductId: req.GetProductId(), OnHand: int32(l.OnHand), Reserved: int32(l.Reserved), Available: int32(l.Available()),
	}, nil
}
