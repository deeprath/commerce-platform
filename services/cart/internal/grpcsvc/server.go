// Package grpcsvc implements CartService over the Redis-backed store.
package grpcsvc

import (
	"context"
	"time"

	cartv1 "github.com/deeprath/commerce-platform/gen/go/commerce/cart/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/cart/internal/domain"
	"github.com/deeprath/commerce-platform/services/cart/internal/store"
)

type Server struct {
	cartv1.UnimplementedCartServiceServer
	store *store.Store
}

func New(s *store.Store) *Server { return &Server{store: s} }

func (s *Server) GetCart(ctx context.Context, req *cartv1.GetCartRequest) (*cartv1.Cart, error) {
	c, err := s.load(ctx, req.GetCartId())
	if err != nil {
		return nil, err
	}
	return toProto(c), nil
}

func (s *Server) AddItem(ctx context.Context, req *cartv1.AddItemRequest) (*cartv1.Cart, error) {
	c, err := s.load(ctx, req.GetCartId())
	if err != nil {
		return nil, err
	}
	if err := c.Add(req.GetProductId(), req.GetQuantity()); err != nil {
		return nil, err
	}
	return s.save(ctx, c)
}

func (s *Server) SetItemQuantity(ctx context.Context, req *cartv1.SetItemQuantityRequest) (*cartv1.Cart, error) {
	c, err := s.load(ctx, req.GetCartId())
	if err != nil {
		return nil, err
	}
	if err := c.SetQuantity(req.GetProductId(), req.GetQuantity()); err != nil {
		return nil, err
	}
	return s.save(ctx, c)
}

func (s *Server) RemoveItem(ctx context.Context, req *cartv1.RemoveItemRequest) (*cartv1.Cart, error) {
	c, err := s.load(ctx, req.GetCartId())
	if err != nil {
		return nil, err
	}
	c.Remove(req.GetProductId())
	return s.save(ctx, c)
}

func (s *Server) Clear(ctx context.Context, req *cartv1.ClearRequest) (*cartv1.Cart, error) {
	c, err := s.load(ctx, req.GetCartId())
	if err != nil {
		return nil, err
	}
	c.Clear()
	return s.save(ctx, c)
}

func (s *Server) Merge(ctx context.Context, req *cartv1.MergeRequest) (*cartv1.Cart, error) {
	if req.GetFromCartId() == "" || req.GetToCartId() == "" {
		return nil, errs.New(errs.KindInvalidArgument, "CART_IDS_REQUIRED", "from and to cart ids are required")
	}
	from, err := s.load(ctx, req.GetFromCartId())
	if err != nil {
		return nil, err
	}
	to, err := s.load(ctx, req.GetToCartId())
	if err != nil {
		return nil, err
	}
	to.MergeFrom(from)
	if _, err := s.save(ctx, to); err != nil {
		return nil, err
	}
	_ = s.store.Delete(ctx, req.GetFromCartId())
	return toProto(to), nil
}

func (s *Server) load(ctx context.Context, cartID string) (*domain.Cart, error) {
	if cartID == "" {
		return nil, errs.New(errs.KindInvalidArgument, "CART_ID_REQUIRED", "cart_id is required")
	}
	return s.store.Get(ctx, cartID)
}

func (s *Server) save(ctx context.Context, c *domain.Cart) (*cartv1.Cart, error) {
	if err := s.store.Put(ctx, c); err != nil {
		return nil, err
	}
	return toProto(c), nil
}

func toProto(c *domain.Cart) *cartv1.Cart {
	out := &cartv1.Cart{
		Id:            c.ID,
		TotalQuantity: c.TotalQuantity(),
		UpdatedAt:     c.UpdatedAt.Format(time.RFC3339),
	}
	for _, it := range c.Items {
		out.Items = append(out.Items, &cartv1.CartItem{
			ProductId: it.ProductID, Quantity: it.Quantity, AddedAt: it.AddedAt.Format(time.RFC3339),
		})
	}
	return out
}
