// Package grpcsvc adapts SellerService to the store. Opening / editing a shop is
// a self-service action for any signed-in user; ListShops / ActivateShop /
// SuspendShop are operator actions gated by the shop_admin role. GetShop is
// public but only ever exposes an ACTIVE shop to a non-owner / non-operator.
package grpcsvc

import (
	"context"
	"log/slog"
	"time"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	sellerv1 "github.com/deeprath/commerce-platform/gen/go/commerce/seller/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/fga"
	"github.com/deeprath/commerce-platform/pkg/grpcx"
	"github.com/deeprath/commerce-platform/services/seller/internal/domain"
	"github.com/deeprath/commerce-platform/services/seller/internal/store"
)

const roleShopAdmin = "shop_admin"

type Server struct {
	sellerv1.UnimplementedSellerServiceServer
	store *store.Store
	// fga records shop staff relationships. nil when no OpenFGA endpoint is
	// configured — the staff RPCs then report Unavailable.
	fga fga.API
}

func New(s *store.Store, fgaClient fga.API) *Server { return &Server{store: s, fga: fgaClient} }

func (s *Server) CreateShop(ctx context.Context, req *sellerv1.CreateShopRequest) (*sellerv1.Shop, error) {
	p := auth.FromContext(ctx)
	if p == nil {
		return nil, errs.New(errs.KindUnauthenticated, "NOT_AUTHENTICATED", "sign-in required to open a shop")
	}
	sh, err := domain.NewShop(p.Subject, req.GetName(), req.GetDescription(), req.GetContactEmail())
	if err != nil {
		return nil, err
	}
	saved, err := s.store.Create(ctx, sh)
	if err != nil {
		return nil, err
	}
	// Record the owner in OpenFGA so `shop#staff` resolves for them and other
	// services can key off it. Best-effort: a missing owner tuple is re-written
	// on the owner's next staff operation.
	if s.fga != nil {
		if werr := s.fga.Write(ctx, fga.UserObject(p.Subject), fga.RelationOwner, fga.ShopObject(saved.ID)); werr != nil && !fga.IsAlreadyExists(werr) {
			slog.ErrorContext(ctx, "shop owner tuple write failed",
				slog.String("shop", saved.ID), slog.Any("err", werr))
		}
	}
	return toProto(saved), nil
}

func (s *Server) GetMyShop(ctx context.Context, _ *sellerv1.GetMyShopRequest) (*sellerv1.Shop, error) {
	p := auth.FromContext(ctx)
	if p == nil {
		return nil, errs.New(errs.KindUnauthenticated, "NOT_AUTHENTICATED", "sign-in required")
	}
	sh, err := s.store.GetByOwner(ctx, p.Subject)
	if err != nil {
		return nil, err
	}
	return toProto(sh), nil
}

func (s *Server) GetShop(ctx context.Context, req *sellerv1.GetShopRequest) (*sellerv1.Shop, error) {
	var (
		sh  *domain.Shop
		err error
	)
	switch sel := req.GetSelector().(type) {
	case *sellerv1.GetShopRequest_Id:
		sh, err = s.store.GetByID(ctx, sel.Id)
	case *sellerv1.GetShopRequest_Slug:
		sh, err = s.store.GetBySlug(ctx, sel.Slug)
	default:
		return nil, errs.New(errs.KindInvalidArgument, "SELECTOR_REQUIRED", "id or slug is required")
	}
	if err != nil {
		return nil, err
	}
	// A non-ACTIVE shop is visible only to its owner or an operator; to anyone
	// else it does not exist.
	if sh.Status != domain.StatusActive && !s.maySeePrivate(ctx, sh) {
		return nil, errs.New(errs.KindNotFound, "SHOP_NOT_FOUND", "no such shop")
	}
	return toProto(sh), nil
}

func (s *Server) UpdateShop(ctx context.Context, req *sellerv1.UpdateShopRequest) (*sellerv1.Shop, error) {
	p := auth.FromContext(ctx)
	if p == nil {
		return nil, errs.New(errs.KindUnauthenticated, "NOT_AUTHENTICATED", "sign-in required")
	}
	sh, err := s.store.Update(ctx, p.Subject, req.GetName(), req.GetDescription(), req.GetContactEmail())
	if err != nil {
		return nil, err
	}
	return toProto(sh), nil
}

func (s *Server) ListShops(ctx context.Context, req *sellerv1.ListShopsRequest) (*sellerv1.ListShopsResponse, error) {
	if err := grpcx.RequireRole(ctx, roleShopAdmin); err != nil {
		return nil, err
	}
	items, next, err := s.store.List(ctx, statusFromProto(req.GetStatus()),
		int(req.GetPage().GetPageSize()), req.GetPage().GetPageToken())
	if err != nil {
		return nil, err
	}
	out := &sellerv1.ListShopsResponse{Page: &commonv1.PageResponse{NextPageToken: next, TotalSize: -1}}
	for _, sh := range items {
		out.Shops = append(out.Shops, toProto(sh))
	}
	return out, nil
}

func (s *Server) ActivateShop(ctx context.Context, req *sellerv1.ActivateShopRequest) (*sellerv1.Shop, error) {
	if err := grpcx.RequireRole(ctx, roleShopAdmin); err != nil {
		return nil, err
	}
	sh, err := s.store.Transition(ctx, req.GetId(), func(sh *domain.Shop) (bool, error) { return sh.Activate() })
	if err != nil {
		return nil, err
	}
	return toProto(sh), nil
}

func (s *Server) SuspendShop(ctx context.Context, req *sellerv1.SuspendShopRequest) (*sellerv1.Shop, error) {
	if err := grpcx.RequireRole(ctx, roleShopAdmin); err != nil {
		return nil, err
	}
	sh, err := s.store.Transition(ctx, req.GetId(), func(sh *domain.Shop) (bool, error) { return sh.Suspend(req.GetReason()) })
	if err != nil {
		return nil, err
	}
	return toProto(sh), nil
}

// maySeePrivate reports whether the caller is the shop's owner or an operator.
func (s *Server) maySeePrivate(ctx context.Context, sh *domain.Shop) bool {
	p := auth.FromContext(ctx)
	if p == nil {
		return false
	}
	return p.Subject == sh.OwnerID || p.HasRole(roleShopAdmin)
}

func toProto(sh *domain.Shop) *sellerv1.Shop {
	return &sellerv1.Shop{
		Id: sh.ID, OwnerId: sh.OwnerID, Name: sh.Name, Slug: sh.Slug,
		Description: sh.Description, ContactEmail: sh.ContactEmail,
		Status:           statusToProto(sh.Status),
		SuspensionReason: sh.SuspensionReason,
		CreatedAt:        sh.CreatedAt.Format(time.RFC3339),
		UpdatedAt:        sh.UpdatedAt.Format(time.RFC3339),
	}
}

func statusToProto(s domain.Status) sellerv1.ShopStatus {
	switch s {
	case domain.StatusPendingReview:
		return sellerv1.ShopStatus_SHOP_STATUS_PENDING_REVIEW
	case domain.StatusActive:
		return sellerv1.ShopStatus_SHOP_STATUS_ACTIVE
	case domain.StatusSuspended:
		return sellerv1.ShopStatus_SHOP_STATUS_SUSPENDED
	default:
		return sellerv1.ShopStatus_SHOP_STATUS_UNSPECIFIED
	}
}

func statusFromProto(s sellerv1.ShopStatus) domain.Status {
	switch s {
	case sellerv1.ShopStatus_SHOP_STATUS_PENDING_REVIEW:
		return domain.StatusPendingReview
	case sellerv1.ShopStatus_SHOP_STATUS_ACTIVE:
		return domain.StatusActive
	case sellerv1.ShopStatus_SHOP_STATUS_SUSPENDED:
		return domain.StatusSuspended
	default:
		return domain.Status("")
	}
}
