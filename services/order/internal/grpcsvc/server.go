// Package grpcsvc adapts OrderService to the saga + store.
package grpcsvc

import (
	"context"
	"time"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/order/internal/domain"
	"github.com/deeprath/commerce-platform/services/order/internal/saga"
	"github.com/deeprath/commerce-platform/services/order/internal/store"
)

type Server struct {
	orderv1.UnimplementedOrderServiceServer
	saga  *saga.Orchestrator
	store *store.Store
}

func New(sg *saga.Orchestrator, st *store.Store) *Server { return &Server{saga: sg, store: st} }

func (s *Server) CreateOrder(ctx context.Context, req *orderv1.CreateOrderRequest) (*orderv1.CreateOrderResponse, error) {
	p := auth.FromContext(ctx)
	if p == nil {
		return nil, errs.New(errs.KindUnauthenticated, "NOT_AUTHENTICATED", "sign-in required to check out")
	}
	res, err := s.saga.CreateOrder(ctx, saga.CreateInput{
		OwnerID:     p.Subject,
		CartID:      req.GetCartId(),
		Currency:    req.GetCurrencyCode(),
		CouponCode:  req.GetCouponCode(),
		MethodToken: req.GetPaymentMethodToken(),
		ShipTo:      addrFrom(req.GetShipTo()),
	})
	if err != nil {
		return nil, err
	}
	u, n := res.Order.Total.UnitsNanos()
	return &orderv1.CreateOrderResponse{
		OrderId:             res.Order.ID,
		Status:              statusToProto(res.Order.Status),
		PaymentId:           res.Order.PaymentID,
		PaymentClientSecret: res.PaymentSecret,
		Total:               &commonv1.Money{CurrencyCode: res.Order.Total.Currency, Units: u, Nanos: n},
	}, nil
}

func (s *Server) GetOrder(ctx context.Context, req *orderv1.GetOrderRequest) (*orderv1.Order, error) {
	p := auth.FromContext(ctx)
	if p == nil {
		return nil, errs.New(errs.KindUnauthenticated, "NOT_AUTHENTICATED", "sign-in required")
	}
	owner := p.Subject
	if p.HasRole(roleOrderManager) {
		owner = "" // operators may read any order
	}
	o, err := s.store.Get(ctx, req.GetId(), owner)
	if err != nil {
		return nil, err
	}
	return toProto(o), nil
}

func (s *Server) ListOrders(ctx context.Context, req *orderv1.ListOrdersRequest) (*orderv1.ListOrdersResponse, error) {
	p := auth.FromContext(ctx)
	if p == nil {
		return nil, errs.New(errs.KindUnauthenticated, "NOT_AUTHENTICATED", "sign-in required")
	}
	// An operator may list any customer's orders and filter; everyone else is
	// scoped to their own.
	owner, status := p.Subject, ""
	if p.HasRole(roleOrderManager) {
		owner, status = req.GetOwnerId(), req.GetStatus()
	}
	orders, next, err := s.store.List(ctx, owner, status,
		int(req.GetPage().GetPageSize()), req.GetPage().GetPageToken())
	if err != nil {
		return nil, err
	}
	out := &orderv1.ListOrdersResponse{Page: &commonv1.PageResponse{NextPageToken: next, TotalSize: -1}}
	for _, o := range orders {
		out.Orders = append(out.Orders, toProto(o))
	}
	return out, nil
}

func (s *Server) CancelOrder(ctx context.Context, req *orderv1.CancelOrderRequest) (*orderv1.Order, error) {
	p := auth.FromContext(ctx)
	if p == nil {
		return nil, errs.New(errs.KindUnauthenticated, "NOT_AUTHENTICATED", "sign-in required")
	}
	o, err := s.store.Get(ctx, req.GetId(), p.Subject)
	if err != nil {
		return nil, err
	}
	applied, err := s.store.Apply(ctx, o.ID, "", func(o *domain.Order) error { return o.Cancel("CANCELLED_BY_CUSTOMER") })
	if err != nil {
		return nil, err
	}
	// compensations run through the saga's public helpers via a fresh cancel path
	s.saga.CompensateCancelled(ctx, applied)
	return toProto(applied), nil
}

func addrFrom(a *commonv1.Address) domain.Address {
	return domain.Address{
		FullName: a.GetFullName(), Line1: a.GetLine1(), Line2: a.GetLine2(), City: a.GetCity(),
		Region: a.GetRegion(), PostalCode: a.GetPostalCode(), CountryCode: a.GetCountryCode(), Phone: a.GetPhone(),
	}
}

func toProto(o *domain.Order) *orderv1.Order {
	m := func(x domain.Money) *commonv1.Money {
		u, n := x.UnitsNanos()
		return &commonv1.Money{CurrencyCode: x.Currency, Units: u, Nanos: n}
	}
	out := &orderv1.Order{
		Id: o.ID, OwnerId: o.OwnerID, Status: statusToProto(o.Status),
		Subtotal: m(o.Subtotal), Discount: m(o.Discount), Tax: m(o.Tax), Total: m(o.Total),
		ShipTo: &commonv1.Address{
			FullName: o.ShipTo.FullName, Line1: o.ShipTo.Line1, Line2: o.ShipTo.Line2, City: o.ShipTo.City,
			Region: o.ShipTo.Region, PostalCode: o.ShipTo.PostalCode, CountryCode: o.ShipTo.CountryCode, Phone: o.ShipTo.Phone,
		},
		PaymentId: o.PaymentID, ReservationId: o.ReservationID, CancelReason: o.CancelReason,
		CreatedAt: o.CreatedAt.Format(time.RFC3339), UpdatedAt: o.UpdatedAt.Format(time.RFC3339),
	}
	for _, l := range o.Lines {
		out.Lines = append(out.Lines, &orderv1.OrderLine{
			ProductId: l.ProductID, Title: l.Title, Quantity: l.Quantity,
			UnitPrice: m(l.UnitPrice), LineTotal: m(l.LineTotal),
		})
	}
	return out
}

func statusToProto(s domain.Status) orderv1.OrderStatus {
	switch s {
	case domain.StatusPendingPayment:
		return orderv1.OrderStatus_ORDER_STATUS_PENDING_PAYMENT
	case domain.StatusConfirmed:
		return orderv1.OrderStatus_ORDER_STATUS_CONFIRMED
	case domain.StatusCancelled:
		return orderv1.OrderStatus_ORDER_STATUS_CANCELLED
	case domain.StatusFulfilled:
		return orderv1.OrderStatus_ORDER_STATUS_FULFILLED
	default:
		return orderv1.OrderStatus_ORDER_STATUS_UNSPECIFIED
	}
}
