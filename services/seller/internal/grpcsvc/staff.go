package grpcsvc

import (
	"context"
	"strings"

	sellerv1 "github.com/deeprath/commerce-platform/gen/go/commerce/seller/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/pkg/fga"
)

// callerShop resolves the shop owned by the caller. Staff management is an
// owner-only action on one's own shop — operators do not bypass it.
func (s *Server) callerShop(ctx context.Context) (shopID, ownerSub string, err error) {
	p := auth.FromContext(ctx)
	if p == nil {
		return "", "", errs.New(errs.KindUnauthenticated, "NOT_AUTHENTICATED", "sign-in required")
	}
	if s.fga == nil {
		return "", "", errs.New(errs.KindUnavailable, "STAFF_DISABLED", "shop staff management is not configured")
	}
	sh, gerr := s.store.GetByOwner(ctx, p.Subject)
	if gerr != nil {
		return "", "", gerr // NotFound when the caller has no shop
	}
	return sh.ID, p.Subject, nil
}

func staffSubject(raw string) (string, error) {
	sub := strings.TrimSpace(raw)
	if sub == "" {
		return "", errs.New(errs.KindInvalidArgument, "SUBJECT_REQUIRED", "staff_subject is required")
	}
	return sub, nil
}

func (s *Server) AddShopStaff(ctx context.Context, req *sellerv1.AddShopStaffRequest) (*sellerv1.AddShopStaffResponse, error) {
	shopID, owner, err := s.callerShop(ctx)
	if err != nil {
		return nil, err
	}
	sub, err := staffSubject(req.GetStaffSubject())
	if err != nil {
		return nil, err
	}
	if sub == owner {
		return nil, errs.New(errs.KindInvalidArgument, "SUBJECT_IS_OWNER", "the owner is already staff")
	}
	// Keep the owner tuple in place (self-heal if CreateShop's best-effort write
	// was lost).
	_ = s.fga.Write(ctx, fga.UserObject(owner), fga.RelationOwner, fga.ShopObject(shopID))
	if werr := s.fga.Write(ctx, fga.UserObject(sub), fga.RelationStaff, fga.ShopObject(shopID)); werr != nil && !fga.IsAlreadyExists(werr) {
		return nil, errs.Wrap(werr, errs.KindInternal, "ADD_STAFF_FAILED", "could not record the staff grant")
	}
	return &sellerv1.AddShopStaffResponse{}, nil
}

func (s *Server) RemoveShopStaff(ctx context.Context, req *sellerv1.RemoveShopStaffRequest) (*sellerv1.RemoveShopStaffResponse, error) {
	shopID, _, err := s.callerShop(ctx)
	if err != nil {
		return nil, err
	}
	sub, err := staffSubject(req.GetStaffSubject())
	if err != nil {
		return nil, err
	}
	if derr := s.fga.Delete(ctx, fga.UserObject(sub), fga.RelationStaff, fga.ShopObject(shopID)); derr != nil && !fga.IsNotFound(derr) {
		return nil, errs.Wrap(derr, errs.KindInternal, "REMOVE_STAFF_FAILED", "could not remove the staff grant")
	}
	return &sellerv1.RemoveShopStaffResponse{}, nil
}

func (s *Server) ListShopStaff(ctx context.Context, _ *sellerv1.ListShopStaffRequest) (*sellerv1.ListShopStaffResponse, error) {
	shopID, owner, err := s.callerShop(ctx)
	if err != nil {
		return nil, err
	}
	tuples, err := s.fga.Read(ctx, fga.ShopObject(shopID))
	if err != nil {
		return nil, errs.Wrap(err, errs.KindInternal, "LIST_STAFF_FAILED", "could not read shop staff")
	}
	out := &sellerv1.ListShopStaffResponse{OwnerSubject: owner}
	for _, t := range tuples {
		if t.Relation != fga.RelationStaff {
			continue
		}
		out.StaffSubjects = append(out.StaffSubjects, strings.TrimPrefix(t.User, "user:"))
	}
	return out, nil
}
