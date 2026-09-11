// Package grpcsvc adapts PayoutService to the payout store. Reads require the
// caller to be staff of the payout's shop (OpenFGA) or hold the finance role;
// MarkPaid is finance-only.
package grpcsvc

import (
	"context"
	"time"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	payoutv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payout/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/fga"
	"github.com/deeprath/commerce-platform/pkg/grpcx"
	"github.com/deeprath/commerce-platform/services/payout/internal/domain"
	"github.com/deeprath/commerce-platform/services/payout/internal/store"
)

const roleFinance = "finance"

// Server implements payoutv1.PayoutServiceServer.
type Server struct {
	payoutv1.UnimplementedPayoutServiceServer
	store *store.Store
	// fga authorizes a seller's own read: staff of the payout's shop. nil when
	// no OpenFGA endpoint is configured — reads then require the finance role.
	fga fga.API
}

func New(s *store.Store, fgaClient fga.API) *Server { return &Server{store: s, fga: fgaClient} }

// mayReadShop authorizes a read scoped to shopID: the finance role always
// passes; otherwise the caller must be `shop#staff` of shopID.
func (s *Server) mayReadShop(ctx context.Context, shopID string) error {
	if hasRole(ctx, roleFinance) {
		return nil
	}
	p := auth.FromContext(ctx)
	if p == nil {
		return errs.New(errs.KindUnauthenticated, "NOT_AUTHENTICATED", "sign-in required")
	}
	if shopID == "" {
		return errs.New(errs.KindPermissionDenied, "MISSING_ROLE", "caller lacks role "+roleFinance)
	}
	if s.fga == nil {
		return errs.New(errs.KindPermissionDenied, "NOT_SHOP_STAFF", "not permitted to view this shop's payouts")
	}
	ok, err := s.fga.Check(ctx, fga.UserObject(p.Subject), fga.RelationStaff, fga.ShopObject(shopID))
	if err != nil {
		return errs.Wrap(err, errs.KindPermissionDenied, "AUTHZ_CHECK_FAILED", "could not verify shop staff access")
	}
	if !ok {
		return errs.New(errs.KindPermissionDenied, "NOT_SHOP_STAFF", "you are not staff of this shop")
	}
	return nil
}

func (s *Server) GetPayout(ctx context.Context, req *payoutv1.GetPayoutRequest) (*payoutv1.Payout, error) {
	p, err := s.store.Get(ctx, req.GetId(), "")
	if err != nil {
		return nil, err
	}
	if err := s.mayReadShop(ctx, p.ShopID); err != nil {
		return nil, err
	}
	return toProto(p), nil
}

func (s *Server) ListPayouts(ctx context.Context, req *payoutv1.ListPayoutsRequest) (*payoutv1.ListPayoutsResponse, error) {
	if req.GetShopId() == "" && !hasRole(ctx, roleFinance) {
		return nil, errs.New(errs.KindInvalidArgument, "SHOP_ID_REQUIRED", "shop_id is required")
	}
	if err := s.mayReadShop(ctx, req.GetShopId()); err != nil {
		return nil, err
	}
	payouts, next, err := s.store.List(ctx, req.GetShopId(), req.GetStatus(),
		int(req.GetPage().GetPageSize()), req.GetPage().GetPageToken())
	if err != nil {
		return nil, err
	}
	out := &payoutv1.ListPayoutsResponse{Page: &commonv1.PageResponse{NextPageToken: next, TotalSize: -1}}
	for _, p := range payouts {
		out.Payouts = append(out.Payouts, toProto(p))
	}
	return out, nil
}

func (s *Server) MarkPaid(ctx context.Context, req *payoutv1.MarkPaidRequest) (*payoutv1.Payout, error) {
	if err := grpcx.RequireRole(ctx, roleFinance); err != nil {
		return nil, err
	}
	p, err := s.store.MarkPaid(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	return toProto(p), nil
}

// --- mapping helpers ---

func hasRole(ctx context.Context, r string) bool {
	p := auth.FromContext(ctx)
	return p != nil && p.HasRole(r)
}

func toProto(p *domain.Payout) *payoutv1.Payout {
	u, n := p.Amount.UnitsNanos()
	out := &payoutv1.Payout{
		Id: p.ID, OrderId: p.OrderID, ShopId: p.ShopID,
		Amount:    &commonv1.Money{CurrencyCode: p.Amount.Currency, Units: u, Nanos: n},
		Status:    statusToProto(p.Status),
		CreatedAt: p.CreatedAt.Format(time.RFC3339),
	}
	if p.PaidAt != nil {
		out.PaidAt = p.PaidAt.Format(time.RFC3339)
	}
	return out
}

func statusToProto(s domain.Status) payoutv1.PayoutStatus {
	switch s {
	case domain.StatusPending:
		return payoutv1.PayoutStatus_PAYOUT_STATUS_PENDING
	case domain.StatusPaid:
		return payoutv1.PayoutStatus_PAYOUT_STATUS_PAID
	default:
		return payoutv1.PayoutStatus_PAYOUT_STATUS_UNSPECIFIED
	}
}
