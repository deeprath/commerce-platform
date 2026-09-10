package grpcsvc

import (
	"context"
	"strings"

	orderv1 "github.com/deeprath/commerce-platform/gen/go/commerce/order/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/fga"
	"github.com/deeprath/commerce-platform/services/order/internal/authz"
)

// ownedOrder resolves an order the caller must own. Operators do NOT bypass
// this: sharing is a customer action on their own order.
func (s *Server) ownedOrder(ctx context.Context, orderID string) (string, error) {
	p := auth.FromContext(ctx)
	if p == nil {
		return "", errs.New(errs.KindUnauthenticated, "NOT_AUTHENTICATED", "sign-in required")
	}
	if s.fga == nil {
		return "", errs.New(errs.KindUnavailable, "SHARING_DISABLED", "order sharing is not configured")
	}
	if _, err := s.store.Get(ctx, orderID, p.Subject); err != nil {
		return "", err // NotFound both for a missing order and one the caller doesn't own
	}
	return p.Subject, nil
}

func (s *Server) ShareOrder(ctx context.Context, req *orderv1.ShareOrderRequest) (*orderv1.ShareOrderResponse, error) {
	owner, err := s.ownedOrder(ctx, req.GetOrderId())
	if err != nil {
		return nil, err
	}
	grantee := strings.TrimSpace(req.GetGranteeSubject())
	if grantee == "" {
		return nil, errs.New(errs.KindInvalidArgument, "GRANTEE_REQUIRED", "grantee_subject is required")
	}
	if grantee == owner {
		return nil, errs.New(errs.KindInvalidArgument, "GRANTEE_IS_OWNER", "you already own this order")
	}
	err = s.fga.Write(ctx, authz.UserObject(grantee), authz.RelationViewer, authz.OrderObject(req.GetOrderId()))
	if err != nil && !fga.IsAlreadyExists(err) {
		return nil, errs.Wrap(err, errs.KindInternal, "SHARE_FAILED", "could not record the share")
	}
	return &orderv1.ShareOrderResponse{}, nil
}

func (s *Server) RevokeOrderShare(ctx context.Context, req *orderv1.RevokeOrderShareRequest) (*orderv1.RevokeOrderShareResponse, error) {
	if _, err := s.ownedOrder(ctx, req.GetOrderId()); err != nil {
		return nil, err
	}
	grantee := strings.TrimSpace(req.GetGranteeSubject())
	if grantee == "" {
		return nil, errs.New(errs.KindInvalidArgument, "GRANTEE_REQUIRED", "grantee_subject is required")
	}
	err := s.fga.Delete(ctx, authz.UserObject(grantee), authz.RelationViewer, authz.OrderObject(req.GetOrderId()))
	if err != nil && !fga.IsNotFound(err) {
		return nil, errs.Wrap(err, errs.KindInternal, "REVOKE_FAILED", "could not remove the share")
	}
	return &orderv1.RevokeOrderShareResponse{}, nil
}

func (s *Server) ListOrderShares(ctx context.Context, req *orderv1.ListOrderSharesRequest) (*orderv1.ListOrderSharesResponse, error) {
	if _, err := s.ownedOrder(ctx, req.GetOrderId()); err != nil {
		return nil, err
	}
	tuples, err := s.fga.Read(ctx, authz.OrderObject(req.GetOrderId()))
	if err != nil {
		return nil, errs.Wrap(err, errs.KindInternal, "LIST_SHARES_FAILED", "could not read shares")
	}
	out := &orderv1.ListOrderSharesResponse{}
	for _, t := range tuples {
		if t.Relation != authz.RelationViewer {
			continue
		}
		out.GranteeSubjects = append(out.GranteeSubjects, strings.TrimPrefix(t.User, "user:"))
	}
	return out, nil
}
