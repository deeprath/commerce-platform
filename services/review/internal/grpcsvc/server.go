// Package grpcsvc adapts ReviewService to the store. CreateReview enforces the
// verified-purchase rule; ListReviews / GetRatingSummary are public;
// ModerateReview requires the catalog_manager role.
package grpcsvc

import (
	"context"
	"strings"
	"time"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	reviewv1 "github.com/deeprath/commerce-platform/gen/go/commerce/review/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/grpcx"
	"github.com/deeprath/commerce-platform/services/review/internal/domain"
	"github.com/deeprath/commerce-platform/services/review/internal/store"
)

const roleCatalogManager = "catalog_manager"

type Server struct {
	reviewv1.UnimplementedReviewServiceServer
	store *store.Store
}

func New(s *store.Store) *Server { return &Server{store: s} }

func (s *Server) CreateReview(ctx context.Context, req *reviewv1.CreateReviewRequest) (*reviewv1.Review, error) {
	p := auth.FromContext(ctx)
	if p == nil {
		return nil, errs.New(errs.KindUnauthenticated, "NOT_AUTHENTICATED", "sign-in required to review")
	}
	bought, err := s.store.HasPurchased(ctx, p.Subject, req.GetProductId())
	if err != nil {
		return nil, err
	}
	if !bought {
		return nil, errs.New(errs.KindFailedPrecondition, "NOT_PURCHASED",
			"you can only review a product from a confirmed order")
	}

	r, err := domain.NewReview(req.GetProductId(), p.Subject, displayName(p),
		req.GetRating(), req.GetTitle(), req.GetBody())
	if err != nil {
		return nil, err
	}
	saved, err := s.store.Create(ctx, r)
	if err != nil {
		return nil, err
	}
	return toProto(saved), nil
}

func (s *Server) ListReviews(ctx context.Context, req *reviewv1.ListReviewsRequest) (*reviewv1.ListReviewsResponse, error) {
	if req.GetProductId() == "" {
		return nil, errs.New(errs.KindInvalidArgument, "PRODUCT_ID_REQUIRED", "product_id is required")
	}
	items, next, err := s.store.ListPublished(ctx, req.GetProductId(),
		int(req.GetPage().GetPageSize()), req.GetPage().GetPageToken())
	if err != nil {
		return nil, err
	}
	out := &reviewv1.ListReviewsResponse{Page: &commonv1.PageResponse{NextPageToken: next, TotalSize: -1}}
	for _, r := range items {
		out.Reviews = append(out.Reviews, toProto(r))
	}
	return out, nil
}

func (s *Server) GetRatingSummary(ctx context.Context, req *reviewv1.GetRatingSummaryRequest) (*reviewv1.RatingSummary, error) {
	if req.GetProductId() == "" {
		return nil, errs.New(errs.KindInvalidArgument, "PRODUCT_ID_REQUIRED", "product_id is required")
	}
	sum, err := s.store.Summary(ctx, req.GetProductId())
	if err != nil {
		return nil, err
	}
	return &reviewv1.RatingSummary{
		ProductId: sum.ProductID, Average: sum.Average, Count: sum.Count,
		Histogram: sum.Histogram[:],
	}, nil
}

func (s *Server) ModerateReview(ctx context.Context, req *reviewv1.ModerateReviewRequest) (*reviewv1.Review, error) {
	if err := grpcx.RequireRole(ctx, roleCatalogManager); err != nil {
		return nil, err
	}
	to := statusFromProto(req.GetStatus())
	if !domain.ValidModerationTarget(to) {
		return nil, errs.New(errs.KindInvalidArgument, "BAD_STATUS", "status must be PUBLISHED or HIDDEN")
	}
	r, err := s.store.Moderate(ctx, req.GetId(), to)
	if err != nil {
		return nil, err
	}
	return toProto(r), nil
}

func displayName(p *auth.Principal) string {
	if n, _ := p.Raw["name"].(string); strings.TrimSpace(n) != "" {
		return n
	}
	if p.Email != "" {
		if i := strings.IndexByte(p.Email, '@'); i > 0 {
			return p.Email[:i]
		}
		return p.Email
	}
	return "Customer"
}

func toProto(r *domain.Review) *reviewv1.Review {
	return &reviewv1.Review{
		Id: r.ID, ProductId: r.ProductID, AuthorId: r.AuthorID, AuthorName: r.AuthorName,
		Rating: r.Rating, Title: r.Title, Body: r.Body,
		Status:           statusToProto(r.Status),
		VerifiedPurchase: r.VerifiedPurchase,
		CreatedAt:        r.CreatedAt.Format(time.RFC3339),
		UpdatedAt:        r.UpdatedAt.Format(time.RFC3339),
	}
}

func statusToProto(s domain.Status) reviewv1.ReviewStatus {
	switch s {
	case domain.StatusPublished:
		return reviewv1.ReviewStatus_REVIEW_STATUS_PUBLISHED
	case domain.StatusHidden:
		return reviewv1.ReviewStatus_REVIEW_STATUS_HIDDEN
	default:
		return reviewv1.ReviewStatus_REVIEW_STATUS_UNSPECIFIED
	}
}

func statusFromProto(s reviewv1.ReviewStatus) domain.Status {
	switch s {
	case reviewv1.ReviewStatus_REVIEW_STATUS_PUBLISHED:
		return domain.StatusPublished
	case reviewv1.ReviewStatus_REVIEW_STATUS_HIDDEN:
		return domain.StatusHidden
	default:
		return domain.Status("")
	}
}
