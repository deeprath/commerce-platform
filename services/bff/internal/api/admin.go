package api

import (
	"github.com/labstack/echo/v4"

	fulfillmentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/fulfillment/v1"
	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
)

// requireAuth returns false (and writes 401) when the request carries no token.
// The downstream services enforce the actual role.
func requireAuth(c echo.Context) bool {
	if bearer(c) == "" {
		_ = c.JSON(401, errs.HTTPError{Status: 401, Code: "UNAUTHENTICATED", Reason: "SIGN_IN_REQUIRED"})
		return false
	}
	return true
}

// --- orders ---------------------------------------------------------------

func (s *Server) adminListOrders(c echo.Context) error {
	if !requireAuth(c) {
		return nil
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Order.ListOrders(ctx, &orderv1.ListOrdersRequest{
		Page: page(c), OwnerId: c.QueryParam("owner_id"), Status: c.QueryParam("status"),
	})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

func (s *Server) adminGetOrder(c echo.Context) error {
	if !requireAuth(c) {
		return nil
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Order.GetOrder(ctx, &orderv1.GetOrderRequest{Id: c.Param("id")})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

// --- returns queue -----------------------------------------------------------

func (s *Server) adminListReturns(c echo.Context) error {
	if !requireAuth(c) {
		return nil
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Order.ListReturns(ctx, &orderv1.ListReturnsRequest{
		Page: page(c), Status: c.QueryParam("status"),
	})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

type decideReturnBody struct {
	Approve bool   `json:"approve"`
	Note    string `json:"note"`
}

func (s *Server) adminDecideReturn(c echo.Context) error {
	if !requireAuth(c) {
		return nil
	}
	var in decideReturnBody
	if err := c.Bind(&in); err != nil {
		return c.JSON(400, errs.HTTPError{Status: 400, Code: "INVALID_ARGUMENT", Reason: "BAD_JSON"})
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Order.DecideReturn(ctx, &orderv1.DecideReturnRequest{
		Id: c.Param("id"), Approve: in.Approve, Note: in.Note,
	})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

// --- shipments -------------------------------------------------------------

func (s *Server) adminListShipments(c echo.Context) error {
	if !requireAuth(c) {
		return nil
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Fulfillment.ListShipments(ctx, &fulfillmentv1.ListShipmentsRequest{
		Page: page(c), OrderId: c.QueryParam("order_id"), Status: c.QueryParam("status"),
	})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

type shipBody struct {
	Carrier        string `json:"carrier"`
	TrackingNumber string `json:"tracking_number"`
}

func (s *Server) adminShipShipment(c echo.Context) error {
	if !requireAuth(c) {
		return nil
	}
	var in shipBody
	_ = c.Bind(&in)
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Fulfillment.MarkShipped(ctx, &fulfillmentv1.MarkShippedRequest{
		Id: c.Param("id"), Carrier: in.Carrier, TrackingNumber: in.TrackingNumber,
	})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

func (s *Server) adminDeliverShipment(c echo.Context) error {
	if !requireAuth(c) {
		return nil
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Fulfillment.MarkDelivered(ctx, &fulfillmentv1.MarkDeliveredRequest{Id: c.Param("id")})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

type cancelShipmentBody struct {
	Reason string `json:"reason"`
}

func (s *Server) adminCancelShipment(c echo.Context) error {
	if !requireAuth(c) {
		return nil
	}
	var in cancelShipmentBody
	_ = c.Bind(&in)
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Fulfillment.CancelShipment(ctx, &fulfillmentv1.CancelShipmentRequest{
		Id: c.Param("id"), Reason: in.Reason,
	})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}
