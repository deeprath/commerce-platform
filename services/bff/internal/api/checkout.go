package api

import (
	"github.com/labstack/echo/v4"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	paymentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payment/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
)

type addressBody struct {
	FullName    string `json:"full_name"`
	Line1       string `json:"line1"`
	Line2       string `json:"line2"`
	City        string `json:"city"`
	Region      string `json:"region"`
	PostalCode  string `json:"postal_code"`
	CountryCode string `json:"country_code"`
	Phone       string `json:"phone"`
}

type checkoutBody struct {
	ShipTo             addressBody `json:"ship_to"`
	CouponCode         string      `json:"coupon_code"`
	CurrencyCode       string      `json:"currency_code"`
	PaymentMethodToken string      `json:"payment_method_token"`
}

func (s *Server) checkout(c echo.Context) error {
	if bearer(c) == "" {
		return c.JSON(401, errs.HTTPError{Status: 401, Code: "UNAUTHENTICATED", Reason: "SIGN_IN_REQUIRED"})
	}
	var in checkoutBody
	if err := c.Bind(&in); err != nil {
		return c.JSON(400, errs.HTTPError{Status: 400, Code: "INVALID_ARGUMENT", Reason: "BAD_JSON"})
	}
	if in.CurrencyCode == "" {
		in.CurrencyCode = "USD"
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Order.CreateOrder(ctx, &orderv1.CreateOrderRequest{
		CartId:             s.cartID(c),
		CurrencyCode:       in.CurrencyCode,
		CouponCode:         in.CouponCode,
		PaymentMethodToken: in.PaymentMethodToken,
		ShipTo: &commonv1.Address{
			FullName: in.ShipTo.FullName, Line1: in.ShipTo.Line1, Line2: in.ShipTo.Line2,
			City: in.ShipTo.City, Region: in.ShipTo.Region, PostalCode: in.ShipTo.PostalCode,
			CountryCode: in.ShipTo.CountryCode, Phone: in.ShipTo.Phone,
		},
	})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 201, res)
}

type confirmBody struct {
	PaymentID string `json:"payment_id"`
	Outcome   string `json:"outcome"` // "authorize" | "fail"
}

// confirmCheckout stands in for the PSP SDK / webhook in the sandbox: it drives
// the payment intent, which then advances the order saga via Kafka.
func (s *Server) confirmCheckout(c echo.Context) error {
	if bearer(c) == "" {
		return c.JSON(401, errs.HTTPError{Status: 401, Code: "UNAUTHENTICATED", Reason: "SIGN_IN_REQUIRED"})
	}
	var in confirmBody
	if err := c.Bind(&in); err != nil || in.PaymentID == "" {
		return c.JSON(400, errs.HTTPError{Status: 400, Code: "INVALID_ARGUMENT", Reason: "PAYMENT_ID_REQUIRED"})
	}
	outcome := paymentv1.ConfirmPaymentRequest_OUTCOME_AUTHORIZE
	if in.Outcome == "fail" {
		outcome = paymentv1.ConfirmPaymentRequest_OUTCOME_FAIL
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Payment.ConfirmPayment(ctx, &paymentv1.ConfirmPaymentRequest{
		PaymentId: in.PaymentID, Outcome: outcome,
	})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

func (s *Server) listOrders(c echo.Context) error {
	if bearer(c) == "" {
		return c.JSON(401, errs.HTTPError{Status: 401, Code: "UNAUTHENTICATED", Reason: "SIGN_IN_REQUIRED"})
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Order.ListOrders(ctx, &orderv1.ListOrdersRequest{Page: page(c)})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

func (s *Server) getOrder(c echo.Context) error {
	if bearer(c) == "" {
		return c.JSON(401, errs.HTTPError{Status: 401, Code: "UNAUTHENTICATED", Reason: "SIGN_IN_REQUIRED"})
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Order.GetOrder(ctx, &orderv1.GetOrderRequest{Id: c.Param("id")})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}
