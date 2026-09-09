package grpcsvc

import (
	"context"
	"time"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/grpcx"
	"github.com/deeprath/commerce-platform/services/order/internal/domain"
	"github.com/deeprath/commerce-platform/services/order/internal/saga"
)

const roleOrderManager = "order_manager"

// principal returns the authenticated caller or an Unauthenticated error.
func principal(ctx context.Context) (*auth.Principal, error) {
	p := auth.FromContext(ctx)
	if p == nil {
		return nil, errs.New(errs.KindUnauthenticated, "NOT_AUTHENTICATED", "sign-in required")
	}
	return p, nil
}

func (s *Server) RequestReturn(ctx context.Context, req *orderv1.RequestReturnRequest) (*orderv1.Return, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	lines := make([]saga.ReturnLineReq, 0, len(req.GetLines()))
	for _, l := range req.GetLines() {
		lines = append(lines, saga.ReturnLineReq{ProductID: l.GetProductId(), Quantity: l.GetQuantity()})
	}
	r, err := s.saga.RequestReturn(ctx, p.Subject, req.GetOrderId(), req.GetReason(), lines)
	if err != nil {
		return nil, err
	}
	return returnToProto(r), nil
}

func (s *Server) GetReturn(ctx context.Context, req *orderv1.GetReturnRequest) (*orderv1.Return, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	owner := p.Subject
	if p.HasRole(roleOrderManager) {
		owner = "" // operators may read any return
	}
	rr, err := s.store.GetReturn(ctx, req.GetId(), owner)
	if err != nil {
		return nil, err
	}
	return returnToProto(rr), nil
}

func (s *Server) ListReturns(ctx context.Context, req *orderv1.ListReturnsRequest) (*orderv1.ListReturnsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	owner, status := p.Subject, ""
	if p.HasRole(roleOrderManager) {
		owner, status = "", req.GetStatus() // every customer's returns; optional status queue
	}
	items, next, err := s.store.ListReturns(ctx, owner, status,
		int(req.GetPage().GetPageSize()), req.GetPage().GetPageToken())
	if err != nil {
		return nil, err
	}
	out := &orderv1.ListReturnsResponse{Page: &commonv1.PageResponse{NextPageToken: next, TotalSize: -1}}
	for _, r := range items {
		out.Returns = append(out.Returns, returnToProto(r))
	}
	return out, nil
}

func (s *Server) DecideReturn(ctx context.Context, req *orderv1.DecideReturnRequest) (*orderv1.Return, error) {
	if err := grpcx.RequireRole(ctx, roleOrderManager); err != nil {
		return nil, err
	}
	p := auth.FromContext(ctx)
	r, err := s.saga.DecideReturn(ctx, p.Subject, req.GetId(), req.GetApprove(), req.GetNote())
	if err != nil {
		return nil, err
	}
	return returnToProto(r), nil
}

func returnToProto(r *domain.Return) *orderv1.Return {
	m := func(x domain.Money) *commonv1.Money {
		u, n := x.UnitsNanos()
		return &commonv1.Money{CurrencyCode: x.Currency, Units: u, Nanos: n}
	}
	out := &orderv1.Return{
		Id: r.ID, OrderId: r.OrderID, OwnerId: r.OwnerID,
		Status: returnStatusToProto(r.Status), Reason: r.Reason,
		RefundTotal: m(r.RefundTotal), DecidedBy: r.DecidedBy, DecisionNote: r.DecisionNote,
		CreatedAt: r.CreatedAt.Format(time.RFC3339), UpdatedAt: r.UpdatedAt.Format(time.RFC3339),
	}
	for _, l := range r.Lines {
		out.Lines = append(out.Lines, &orderv1.ReturnLine{
			ProductId: l.ProductID, Quantity: l.Quantity, RefundAmount: m(l.RefundAmount),
		})
	}
	return out
}

func returnStatusToProto(s domain.ReturnStatus) orderv1.ReturnStatus {
	switch s {
	case domain.ReturnRequested:
		return orderv1.ReturnStatus_RETURN_STATUS_REQUESTED
	case domain.ReturnApproved:
		return orderv1.ReturnStatus_RETURN_STATUS_APPROVED
	case domain.ReturnRejected:
		return orderv1.ReturnStatus_RETURN_STATUS_REJECTED
	default:
		return orderv1.ReturnStatus_RETURN_STATUS_UNSPECIFIED
	}
}
