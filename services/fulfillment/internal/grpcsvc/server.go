// Package grpcsvc adapts FulfillmentService to the shipment store. Read RPCs are
// owner-scoped; the Mark*/Cancel admin RPCs require the order_manager role.
package grpcsvc

import (
	"context"
	"time"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	fulfillmentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/fulfillment/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/grpcx"
	"github.com/deeprath/commerce-platform/services/fulfillment/internal/domain"
	"github.com/deeprath/commerce-platform/services/fulfillment/internal/store"
)

// Fulfillment is part of the order lifecycle, so its admin actions gate on the
// same role as the rest of that lifecycle.
const roleOrderManager = "order_manager"

type Server struct {
	fulfillmentv1.UnimplementedFulfillmentServiceServer
	store *store.Store
}

func New(s *store.Store) *Server { return &Server{store: s} }

func (s *Server) GetShipment(ctx context.Context, req *fulfillmentv1.GetShipmentRequest) (*fulfillmentv1.Shipment, error) {
	p := auth.FromContext(ctx)
	if p == nil {
		return nil, errs.New(errs.KindUnauthenticated, "NOT_AUTHENTICATED", "sign-in required")
	}
	owner := p.Subject
	if p.HasRole(roleOrderManager) {
		owner = "" // managers may read any shipment
	}
	sh, err := s.store.Get(ctx, req.GetId(), owner)
	if err != nil {
		return nil, err
	}
	return toProto(sh), nil
}

func (s *Server) ListShipments(ctx context.Context, req *fulfillmentv1.ListShipmentsRequest) (*fulfillmentv1.ListShipmentsResponse, error) {
	p := auth.FromContext(ctx)
	if p == nil {
		return nil, errs.New(errs.KindUnauthenticated, "NOT_AUTHENTICATED", "sign-in required")
	}
	shipments, next, err := s.store.List(ctx, p.Subject, req.GetOrderId(),
		int(req.GetPage().GetPageSize()), req.GetPage().GetPageToken())
	if err != nil {
		return nil, err
	}
	out := &fulfillmentv1.ListShipmentsResponse{Page: &commonv1.PageResponse{NextPageToken: next, TotalSize: -1}}
	for _, sh := range shipments {
		out.Shipments = append(out.Shipments, toProto(sh))
	}
	return out, nil
}

func (s *Server) MarkShipped(ctx context.Context, req *fulfillmentv1.MarkShippedRequest) (*fulfillmentv1.Shipment, error) {
	if err := grpcx.RequireRole(ctx, roleOrderManager); err != nil {
		return nil, err
	}
	carrier, tracking := req.GetCarrier(), req.GetTrackingNumber()
	if carrier == "" {
		carrier = "SANDBOX"
	}
	if tracking == "" {
		tracking = "TRK-" + req.GetId()
	}
	sh, err := s.store.Transition(ctx, req.GetId(), domain.StatusShipped, carrier, tracking, "")
	if err != nil {
		return nil, err
	}
	return toProto(sh), nil
}

func (s *Server) MarkDelivered(ctx context.Context, req *fulfillmentv1.MarkDeliveredRequest) (*fulfillmentv1.Shipment, error) {
	if err := grpcx.RequireRole(ctx, roleOrderManager); err != nil {
		return nil, err
	}
	sh, err := s.store.Transition(ctx, req.GetId(), domain.StatusDelivered, "", "", "")
	if err != nil {
		return nil, err
	}
	return toProto(sh), nil
}

func (s *Server) CancelShipment(ctx context.Context, req *fulfillmentv1.CancelShipmentRequest) (*fulfillmentv1.Shipment, error) {
	if err := grpcx.RequireRole(ctx, roleOrderManager); err != nil {
		return nil, err
	}
	reason := req.GetReason()
	if reason == "" {
		reason = "CANCELLED_BY_OPERATOR"
	}
	sh, err := s.store.Transition(ctx, req.GetId(), domain.StatusCancelled, "", "", reason)
	if err != nil {
		return nil, err
	}
	return toProto(sh), nil
}

func toProto(sh *domain.Shipment) *fulfillmentv1.Shipment {
	out := &fulfillmentv1.Shipment{
		Id: sh.ID, OrderId: sh.OrderID, OwnerId: sh.OwnerID,
		Status:         statusToProto(sh.Status),
		Carrier:        sh.Carrier,
		TrackingNumber: sh.TrackingNumber,
		ShipTo: &commonv1.Address{
			FullName: sh.ShipTo.FullName, Line1: sh.ShipTo.Line1, Line2: sh.ShipTo.Line2, City: sh.ShipTo.City,
			Region: sh.ShipTo.Region, PostalCode: sh.ShipTo.PostalCode, CountryCode: sh.ShipTo.CountryCode, Phone: sh.ShipTo.Phone,
		},
		CancelReason: sh.CancelReason,
		CreatedAt:    sh.CreatedAt.Format(time.RFC3339),
	}
	if sh.ShippedAt != nil {
		out.ShippedAt = sh.ShippedAt.Format(time.RFC3339)
	}
	if sh.DeliveredAt != nil {
		out.DeliveredAt = sh.DeliveredAt.Format(time.RFC3339)
	}
	for _, it := range sh.Items {
		out.Items = append(out.Items, &fulfillmentv1.ShipmentItem{
			ProductId: it.ProductID, Title: it.Title, Quantity: it.Quantity,
		})
	}
	return out
}

func statusToProto(s domain.Status) fulfillmentv1.ShipmentStatus {
	switch s {
	case domain.StatusPending:
		return fulfillmentv1.ShipmentStatus_SHIPMENT_STATUS_PENDING
	case domain.StatusShipped:
		return fulfillmentv1.ShipmentStatus_SHIPMENT_STATUS_SHIPPED
	case domain.StatusDelivered:
		return fulfillmentv1.ShipmentStatus_SHIPMENT_STATUS_DELIVERED
	case domain.StatusCancelled:
		return fulfillmentv1.ShipmentStatus_SHIPMENT_STATUS_CANCELLED
	default:
		return fulfillmentv1.ShipmentStatus_SHIPMENT_STATUS_UNSPECIFIED
	}
}
