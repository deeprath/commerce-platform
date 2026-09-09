// Package api is the BFF's HTTP surface (Echo). It shapes per-view JSON,
// forwards the caller's bearer token to gRPC as "authorization" metadata,
// and maps gRPC statuses onto HTTP responses.
package api

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	catalogv1 "github.com/deeprath/commerce-platform/gen/go/commerce/catalog/v1"
	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	mediav1 "github.com/deeprath/commerce-platform/gen/go/commerce/media/v1"
	searchv1 "github.com/deeprath/commerce-platform/gen/go/commerce/search/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/bff/internal/auth"
	"github.com/deeprath/commerce-platform/services/bff/internal/clients"
)

type Server struct {
	cl     *clients.Set
	broker *auth.Broker
}

func New(cl *clients.Set, broker *auth.Broker) *Server { return &Server{cl: cl, broker: broker} }

// Router builds the Echo instance with all routes and middleware.
func (s *Server) Router(allowedOrigins []string) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins:     allowedOrigins,
		AllowCredentials: true,
		AllowMethods:     []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete},
	}))

	e.GET("/healthz", func(c echo.Context) error { return c.String(200, "ok") })

	v1 := e.Group("/api/v1")

	// --- auth ---
	v1.POST("/auth/login", s.login)
	v1.POST("/auth/logout", s.logout)

	// --- browse (anonymous) ---
	v1.GET("/catalog/products", s.listProducts)
	v1.GET("/catalog/products/:slug", s.getProduct)
	v1.GET("/search/autocomplete", s.autocomplete)

	// --- cart (guest or signed-in; a cart_id cookie is minted on first use) ---
	v1.GET("/cart", s.getCart)
	v1.POST("/cart/items", s.addCartItem)
	v1.PUT("/cart/items/:productId", s.setCartItem)
	v1.DELETE("/cart/items/:productId", s.removeCartItem)
	v1.POST("/cart/clear", s.clearCart)

	// --- checkout & orders (require sign-in) ---
	v1.POST("/checkout", s.checkout)
	v1.POST("/checkout/confirm", s.confirmCheckout)
	v1.GET("/orders", s.listOrders)
	v1.GET("/orders/:id", s.getOrder)

	// --- admin (bearer/cookie forwarded; services enforce the role) ---
	adm := v1.Group("/admin")
	adm.POST("/catalog/products", s.createProduct)
	adm.PUT("/catalog/products/:id", s.updateProduct)
	adm.POST("/catalog/products/:id/archive", s.archiveProduct)
	adm.POST("/media/uploads", s.createUpload)
	adm.POST("/media/uploads/confirm", s.confirmUpload)

	return e
}

// --- helpers ---

var marshaler = protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}

func writeProto(c echo.Context, code int, m proto.Message) error {
	b, err := marshaler.Marshal(m)
	if err != nil {
		return c.JSON(500, errs.HTTPError{Status: 500, Code: "INTERNAL", Reason: "MARSHAL"})
	}
	return c.Blob(code, echo.MIMEApplicationJSON, b)
}

func fail(c echo.Context, err error) error {
	h := errs.FromGRPC(err)
	return c.JSON(h.Status, h)
}

// outCtx returns a context carrying the caller's bearer token (from the
// Authorization header or the access_token cookie) as gRPC metadata.
func outCtx(c echo.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(c.Request().Context(), 10*time.Second)
	tok := bearer(c)
	if tok != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
	}
	return ctx, cancel
}

func bearer(c echo.Context) string {
	if h := c.Request().Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	if ck, err := c.Cookie("access_token"); err == nil {
		return ck.Value
	}
	return ""
}

func page(c echo.Context) *commonv1.PageRequest {
	p := &commonv1.PageRequest{PageToken: c.QueryParam("page_token")}
	// ParseInt with bitSize 32 guarantees the value fits int32; services clamp
	// the actual page size again.
	if n, err := strconv.ParseInt(c.QueryParam("page_size"), 10, 32); err == nil && n > 0 {
		p.PageSize = int32(n)
	}
	return p
}

// --- browse handlers ---

func (s *Server) listProducts(c echo.Context) error {
	ctx, cancel := outCtx(c)
	defer cancel()
	req := &searchv1.SearchRequest{
		Query:      c.QueryParam("q"),
		CategoryId: c.QueryParam("category_id"),
		Page:       page(c),
	}
	switch c.QueryParam("sort") {
	case "newest":
		req.Sort = searchv1.SortOrder_SORT_ORDER_NEWEST
	case "price_asc":
		req.Sort = searchv1.SortOrder_SORT_ORDER_PRICE_ASC
	case "price_desc":
		req.Sort = searchv1.SortOrder_SORT_ORDER_PRICE_DESC
	}
	res, err := s.cl.Search.Search(ctx, req)
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

func (s *Server) getProduct(c echo.Context) error {
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Catalog.GetProduct(ctx, &catalogv1.GetProductRequest{Slug: c.Param("slug")})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

func (s *Server) autocomplete(c echo.Context) error {
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Search.Autocomplete(ctx, &searchv1.AutocompleteRequest{Prefix: c.QueryParam("q")})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

// --- admin handlers ---

func (s *Server) createProduct(c echo.Context) error {
	var in catalogv1.CreateProductRequest
	if err := bindProto(c, &in); err != nil {
		return err
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Catalog.CreateProduct(ctx, &in)
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 201, res)
}

func (s *Server) updateProduct(c echo.Context) error {
	var in catalogv1.UpdateProductRequest
	if err := bindProto(c, &in); err != nil {
		return err
	}
	in.Id = c.Param("id")
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Catalog.UpdateProduct(ctx, &in)
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

func (s *Server) archiveProduct(c echo.Context) error {
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Catalog.ArchiveProduct(ctx, &catalogv1.ArchiveProductRequest{Id: c.Param("id")})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

func (s *Server) createUpload(c echo.Context) error {
	var in mediav1.CreateUploadURLRequest
	if err := bindProto(c, &in); err != nil {
		return err
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Media.CreateUploadURL(ctx, &in)
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

func (s *Server) confirmUpload(c echo.Context) error {
	var in mediav1.ConfirmUploadRequest
	if err := bindProto(c, &in); err != nil {
		return err
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Media.ConfirmUpload(ctx, &in)
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

func bindProto(c echo.Context, m proto.Message) error {
	body, err := readBody(c)
	if err != nil {
		return c.JSON(400, errs.HTTPError{Status: 400, Code: "INVALID_ARGUMENT", Reason: "BODY_READ"})
	}
	if err := protojson.Unmarshal(body, m); err != nil {
		return c.JSON(400, errs.HTTPError{Status: 400, Code: "INVALID_ARGUMENT", Reason: "BAD_JSON"})
	}
	return nil
}

func readBody(c echo.Context) ([]byte, error) {
	defer func() { _ = c.Request().Body.Close() }()
	return io.ReadAll(io.LimitReader(c.Request().Body, 1<<20))
}
