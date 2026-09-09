package api

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	cartv1 "github.com/deeprath/commerce-platform/gen/go/commerce/cart/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
)

const cartCookie = "cart_id"

// cartID returns the caller's cart id, minting one (and setting the cookie) on
// first use. The id is an opaque, unguessable token.
func (s *Server) cartID(c echo.Context) string {
	if ck, err := c.Cookie(cartCookie); err == nil && ck.Value != "" {
		return ck.Value
	}
	id := "c_" + uuid.NewString()
	http.SetCookie(c.Response().Writer, &http.Cookie{
		Name: cartCookie, Value: id, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 60 * 60 * 24 * 30,
	})
	return id
}

func (s *Server) getCart(c echo.Context) error {
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Cart.GetCart(ctx, &cartv1.GetCartRequest{CartId: s.cartID(c)})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

type addItemBody struct {
	ProductID string `json:"product_id"`
	Quantity  int32  `json:"quantity"`
}

func (s *Server) addCartItem(c echo.Context) error {
	var in addItemBody
	if err := c.Bind(&in); err != nil {
		return c.JSON(400, errs.HTTPError{Status: 400, Code: "INVALID_ARGUMENT", Reason: "BAD_JSON"})
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Cart.AddItem(ctx, &cartv1.AddItemRequest{
		CartId: s.cartID(c), ProductId: in.ProductID, Quantity: in.Quantity,
	})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

type qtyBody struct {
	Quantity int32 `json:"quantity"`
}

func (s *Server) setCartItem(c echo.Context) error {
	var in qtyBody
	if err := c.Bind(&in); err != nil {
		return c.JSON(400, errs.HTTPError{Status: 400, Code: "INVALID_ARGUMENT", Reason: "BAD_JSON"})
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Cart.SetItemQuantity(ctx, &cartv1.SetItemQuantityRequest{
		CartId: s.cartID(c), ProductId: c.Param("productId"), Quantity: in.Quantity,
	})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

func (s *Server) removeCartItem(c echo.Context) error {
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Cart.RemoveItem(ctx, &cartv1.RemoveItemRequest{
		CartId: s.cartID(c), ProductId: c.Param("productId"),
	})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

func (s *Server) clearCart(c echo.Context) error {
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Cart.Clear(ctx, &cartv1.ClearRequest{CartId: s.cartID(c)})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}
