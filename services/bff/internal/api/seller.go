package api

import (
	"github.com/labstack/echo/v4"

	catalogv1 "github.com/deeprath/commerce-platform/gen/go/commerce/catalog/v1"
	sellerv1 "github.com/deeprath/commerce-platform/gen/go/commerce/seller/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
)

// createShop — POST /api/v1/seller/shops
func (s *Server) createShop(c echo.Context) error {
	if !requireAuth(c) {
		return nil
	}
	var in sellerv1.CreateShopRequest
	if err := bindProto(c, &in); err != nil {
		return err
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Seller.CreateShop(ctx, &in)
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 201, res)
}

// getMyShop — GET /api/v1/seller/shops/me
func (s *Server) getMyShop(c echo.Context) error {
	if !requireAuth(c) {
		return nil
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Seller.GetMyShop(ctx, &sellerv1.GetMyShopRequest{})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

// updateShop — PUT /api/v1/seller/shops/me
func (s *Server) updateShop(c echo.Context) error {
	if !requireAuth(c) {
		return nil
	}
	var in sellerv1.UpdateShopRequest
	if err := bindProto(c, &in); err != nil {
		return err
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Seller.UpdateShop(ctx, &in)
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

// getShop — GET /api/v1/shops/:slug (public; the service hides non-ACTIVE shops)
func (s *Server) getShop(c echo.Context) error {
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Seller.GetShop(ctx, &sellerv1.GetShopRequest{
		Selector: &sellerv1.GetShopRequest_Slug{Slug: c.Param("slug")},
	})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

type staffBody struct {
	Subject string `json:"subject"`
}

// addShopStaff — POST /api/v1/seller/shops/me/staff {"subject":"<sub>"}
func (s *Server) addShopStaff(c echo.Context) error {
	if !requireAuth(c) {
		return nil
	}
	var in staffBody
	if err := bindJSON(c, &in); err != nil {
		return err
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Seller.AddShopStaff(ctx, &sellerv1.AddShopStaffRequest{StaffSubject: in.Subject})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

// removeShopStaff — DELETE /api/v1/seller/shops/me/staff/:subject
func (s *Server) removeShopStaff(c echo.Context) error {
	if !requireAuth(c) {
		return nil
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Seller.RemoveShopStaff(ctx, &sellerv1.RemoveShopStaffRequest{StaffSubject: c.Param("subject")})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

// listShopStaff — GET /api/v1/seller/shops/me/staff
func (s *Server) listShopStaff(c echo.Context) error {
	if !requireAuth(c) {
		return nil
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Seller.ListShopStaff(ctx, &sellerv1.ListShopStaffRequest{})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

// createShopProduct — POST /api/v1/seller/products
// The BFF resolves the caller's shop and creates the product under it; the
// catalog service then enforces `shop#staff`.
func (s *Server) createShopProduct(c echo.Context) error {
	if !requireAuth(c) {
		return nil
	}
	var in catalogv1.CreateProductRequest
	if err := bindProto(c, &in); err != nil {
		return err
	}
	ctx, cancel := outCtx(c)
	defer cancel()

	shop, err := s.cl.Seller.GetMyShop(ctx, &sellerv1.GetMyShopRequest{})
	if err != nil {
		return fail(c, err) // NOT_FOUND when the caller has no shop
	}
	if shop.GetStatus() != sellerv1.ShopStatus_SHOP_STATUS_ACTIVE {
		return c.JSON(409, errs.HTTPError{Status: 409, Code: "SHOP_NOT_ACTIVE", Reason: "SHOP_NOT_ACTIVE"})
	}
	in.ShopId = shop.GetId() // caller cannot spoof another shop
	res, err := s.cl.Catalog.CreateProduct(ctx, &in)
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 201, res)
}

// updateShopProduct — PUT /api/v1/seller/products/:id (catalog checks product#manager)
func (s *Server) updateShopProduct(c echo.Context) error {
	if !requireAuth(c) {
		return nil
	}
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

// archiveShopProduct — POST /api/v1/seller/products/:id/archive
func (s *Server) archiveShopProduct(c echo.Context) error {
	if !requireAuth(c) {
		return nil
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Catalog.ArchiveProduct(ctx, &catalogv1.ArchiveProductRequest{Id: c.Param("id")})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

// adminListShops — GET /api/v1/admin/seller/shops?status=
func (s *Server) adminListShops(c echo.Context) error {
	ctx, cancel := outCtx(c)
	defer cancel()
	req := &sellerv1.ListShopsRequest{Page: page(c)}
	switch c.QueryParam("status") {
	case "PENDING_REVIEW":
		req.Status = sellerv1.ShopStatus_SHOP_STATUS_PENDING_REVIEW
	case "ACTIVE":
		req.Status = sellerv1.ShopStatus_SHOP_STATUS_ACTIVE
	case "SUSPENDED":
		req.Status = sellerv1.ShopStatus_SHOP_STATUS_SUSPENDED
	}
	res, err := s.cl.Seller.ListShops(ctx, req)
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

// adminActivateShop — POST /api/v1/admin/seller/shops/:id/activate
func (s *Server) adminActivateShop(c echo.Context) error {
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Seller.ActivateShop(ctx, &sellerv1.ActivateShopRequest{Id: c.Param("id")})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}

// adminSuspendShop — POST /api/v1/admin/seller/shops/:id/suspend {"reason": "..."}
func (s *Server) adminSuspendShop(c echo.Context) error {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := bindJSON(c, &body); err != nil {
		return err
	}
	ctx, cancel := outCtx(c)
	defer cancel()
	res, err := s.cl.Seller.SuspendShop(ctx, &sellerv1.SuspendShopRequest{Id: c.Param("id"), Reason: body.Reason})
	if err != nil {
		return fail(c, err)
	}
	return writeProto(c, 200, res)
}
