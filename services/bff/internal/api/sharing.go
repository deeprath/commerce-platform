package api

import (
	"github.com/labstack/echo/v4"

	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
)

type shareBody struct {
	Grantee string `json:"grantee"`
}

// shareOrder — POST /api/v1/orders/:id/share {"grantee":"<sub>"}
func (s *Server) shareOrder(c echo.Context) error {
	if bearer(c) == "" {
		return c.JSON(401, errs.HTTPError{Status: 401, Code: "UNAUTHENTICATED", Reason: "SIGN_IN_REQUIRED"})
	}
	var in shareBody
	if err := bindJSON(c, &in); err != nil {
		return err
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Order.ShareOrder(ctx, &orderv1.ShareOrderRequest{
		OrderId: c.Param("id"), GranteeSubject: in.Grantee,
	})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

// revokeOrderShare — DELETE /api/v1/orders/:id/share/:grantee
func (s *Server) revokeOrderShare(c echo.Context) error {
	if bearer(c) == "" {
		return c.JSON(401, errs.HTTPError{Status: 401, Code: "UNAUTHENTICATED", Reason: "SIGN_IN_REQUIRED"})
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Order.RevokeOrderShare(ctx, &orderv1.RevokeOrderShareRequest{
		OrderId: c.Param("id"), GranteeSubject: c.Param("grantee"),
	})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

// listOrderShares — GET /api/v1/orders/:id/shares
func (s *Server) listOrderShares(c echo.Context) error {
	if bearer(c) == "" {
		return c.JSON(401, errs.HTTPError{Status: 401, Code: "UNAUTHENTICATED", Reason: "SIGN_IN_REQUIRED"})
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Order.ListOrderShares(ctx, &orderv1.ListOrderSharesRequest{OrderId: c.Param("id")})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}
