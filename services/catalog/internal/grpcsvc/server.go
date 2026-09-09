// Package grpcsvc adapts the CatalogService gRPC contract to the domain + store.
// It maps proto <-> domain, enforces the catalog_manager role on writes, and
// returns errs-typed errors that pkg/grpcx turns into gRPC statuses.
package grpcsvc

import (
	"context"
	"encoding/base64"
	"strings"
	"time"

	catalogv1 "github.com/deeprath/commerce-platform/gen/go/commerce/catalog/v1"
	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/grpcx"
	"github.com/deeprath/commerce-platform/services/catalog/internal/domain"
	"github.com/deeprath/commerce-platform/services/catalog/internal/store"
)

const roleCatalogManager = "catalog_manager"

// Server implements catalogv1.CatalogServiceServer.
type Server struct {
	catalogv1.UnimplementedCatalogServiceServer
	store *store.Store
}

func New(s *store.Store) *Server { return &Server{store: s} }

func (s *Server) GetProduct(ctx context.Context, req *catalogv1.GetProductRequest) (*catalogv1.GetProductResponse, error) {
	var (
		p   *domain.Product
		err error
	)
	switch {
	case req.GetId() != "":
		p, err = s.store.Get(ctx, req.GetId())
	case req.GetSlug() != "":
		p, err = s.store.GetBySlug(ctx, req.GetSlug())
	default:
		return nil, errs.New(errs.KindInvalidArgument, "ID_OR_SLUG_REQUIRED", "provide id or slug")
	}
	if err != nil {
		return nil, err
	}
	// Anonymous callers only see ACTIVE products.
	if p.Status != domain.StatusActive && !hasRole(ctx, roleCatalogManager) {
		return nil, errs.New(errs.KindNotFound, "PRODUCT_NOT_FOUND", "no such product")
	}
	return &catalogv1.GetProductResponse{Product: toProto(p)}, nil
}

func (s *Server) ListProducts(ctx context.Context, req *catalogv1.ListProductsRequest) (*catalogv1.ListProductsResponse, error) {
	limit := int(req.GetPage().GetPageSize())
	cur, err := decodeCursor(req.GetPage().GetPageToken())
	if err != nil {
		return nil, err
	}
	items, next, err := s.store.List(ctx, req.GetCategoryId(), limit, cur)
	if err != nil {
		return nil, err
	}
	return &catalogv1.ListProductsResponse{
		Products: toProtos(items),
		Page:     &commonv1.PageResponse{NextPageToken: encodeCursor(next), TotalSize: -1},
	}, nil
}

func (s *Server) BatchGetProducts(ctx context.Context, req *catalogv1.BatchGetProductsRequest) (*catalogv1.BatchGetProductsResponse, error) {
	if len(req.GetIds()) > 200 {
		return nil, errs.New(errs.KindInvalidArgument, "TOO_MANY_IDS", "at most 200 ids per call")
	}
	items, err := s.store.BatchGet(ctx, req.GetIds())
	if err != nil {
		return nil, err
	}
	return &catalogv1.BatchGetProductsResponse{Products: toProtos(items)}, nil
}

func (s *Server) CreateProduct(ctx context.Context, req *catalogv1.CreateProductRequest) (*catalogv1.CreateProductResponse, error) {
	if err := grpcx.RequireRole(ctx, roleCatalogManager); err != nil {
		return nil, err
	}
	sub := ""
	if p := auth.FromContext(ctx); p != nil {
		sub = p.Subject
	}
	p, err := domain.NewProduct(
		req.GetSlug(), req.GetTitle(), req.GetDescription(), req.GetCategoryId(),
		fromProtoMoney(req.GetListPrice()), req.GetMediaKeys(), req.GetAttributes(), sub,
	)
	if err != nil {
		return nil, err
	}
	saved, err := s.store.Create(ctx, p)
	if err != nil {
		return nil, err
	}
	return &catalogv1.CreateProductResponse{Product: toProto(saved)}, nil
}

func (s *Server) UpdateProduct(ctx context.Context, req *catalogv1.UpdateProductRequest) (*catalogv1.UpdateProductResponse, error) {
	if err := grpcx.RequireRole(ctx, roleCatalogManager); err != nil {
		return nil, err
	}
	if req.GetId() == "" {
		return nil, errs.New(errs.KindInvalidArgument, "ID_REQUIRED", "id is required")
	}
	p, err := s.store.Get(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	status := p.Status // keep current unless the caller sets one
	if req.GetStatus() != catalogv1.ProductStatus_PRODUCT_STATUS_UNSPECIFIED {
		status = protoStatusToDomain(req.GetStatus())
	}
	if err := p.ApplyUpdate(
		req.GetTitle(), req.GetDescription(), req.GetCategoryId(),
		fromProtoMoney(req.GetListPrice()), req.GetMediaKeys(), req.GetAttributes(), status,
	); err != nil {
		return nil, err
	}
	saved, err := s.store.Update(ctx, p)
	if err != nil {
		return nil, err
	}
	return &catalogv1.UpdateProductResponse{Product: toProto(saved)}, nil
}

func (s *Server) ArchiveProduct(ctx context.Context, req *catalogv1.ArchiveProductRequest) (*catalogv1.ArchiveProductResponse, error) {
	if err := grpcx.RequireRole(ctx, roleCatalogManager); err != nil {
		return nil, err
	}
	saved, err := s.store.Archive(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	return &catalogv1.ArchiveProductResponse{Product: toProto(saved)}, nil
}

// --- mapping helpers ---

func hasRole(ctx context.Context, r string) bool {
	p := auth.FromContext(ctx)
	return p != nil && p.HasRole(r)
}

func toProtos(ps []*domain.Product) []*catalogv1.Product {
	out := make([]*catalogv1.Product, 0, len(ps))
	for _, p := range ps {
		out = append(out, toProto(p))
	}
	return out
}

func toProto(p *domain.Product) *catalogv1.Product {
	return &catalogv1.Product{
		Id:          p.ID,
		Slug:        p.Slug,
		Title:       p.Title,
		Description: p.Description,
		CategoryId:  p.CategoryID,
		ListPrice: &commonv1.Money{
			CurrencyCode: p.ListPrice.CurrencyCode,
			Units:        p.ListPrice.Units,
			Nanos:        p.ListPrice.Nanos,
		},
		MediaKeys:  p.MediaKeys,
		Status:     protoStatus(p.Status),
		Attributes: p.Attributes,
	}
}

func protoStatus(s domain.Status) catalogv1.ProductStatus {
	switch s {
	case domain.StatusDraft:
		return catalogv1.ProductStatus_PRODUCT_STATUS_DRAFT
	case domain.StatusActive:
		return catalogv1.ProductStatus_PRODUCT_STATUS_ACTIVE
	case domain.StatusArchived:
		return catalogv1.ProductStatus_PRODUCT_STATUS_ARCHIVED
	default:
		return catalogv1.ProductStatus_PRODUCT_STATUS_UNSPECIFIED
	}
}

func protoStatusToDomain(s catalogv1.ProductStatus) domain.Status {
	switch s {
	case catalogv1.ProductStatus_PRODUCT_STATUS_ACTIVE:
		return domain.StatusActive
	case catalogv1.ProductStatus_PRODUCT_STATUS_ARCHIVED:
		return domain.StatusArchived
	default:
		return domain.StatusDraft
	}
}

func fromProtoMoney(m *commonv1.Money) domain.Money {
	if m == nil {
		return domain.Money{}
	}
	return domain.Money{CurrencyCode: m.GetCurrencyCode(), Units: m.GetUnits(), Nanos: m.GetNanos()}
}

// Cursor codec: base64("<unixnano>|<id>").

func encodeCursor(c *store.Cursor) string {
	if c == nil {
		return ""
	}
	raw := c.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(tok string) (*store.Cursor, error) {
	if tok == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return nil, errs.New(errs.KindInvalidArgument, "BAD_CURSOR", "page_token is not valid")
	}
	ts, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return nil, errs.New(errs.KindInvalidArgument, "BAD_CURSOR", "page_token is malformed")
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return nil, errs.New(errs.KindInvalidArgument, "BAD_CURSOR", "page_token timestamp is invalid")
	}
	return &store.Cursor{CreatedAt: t, ID: id}, nil
}
