// Package grpcsvc implements the SANDBOX PaymentService. ConfirmPayment stands
// in for the shopper completing payment with the PSP / a PSP webhook; in
// production that path is a signature-verified webhook on the public edge.
package grpcsvc

import (
	"context"
	"time"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	paymentv1 "github.com/deeprath/commerce-platform/gen/go/commerce/payment/v1"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/payment/internal/store"
)

// declineToken forces ConfirmPayment(AUTHORIZE) to fail — the sandbox's
// "declined card".
const declineToken = "pm_card_declined"

type Server struct {
	paymentv1.UnimplementedPaymentServiceServer
	store *store.Store
}

func New(s *store.Store) *Server { return &Server{store: s} }

func (s *Server) CreatePayment(ctx context.Context, req *paymentv1.CreatePaymentRequest) (*paymentv1.Payment, error) {
	if req.GetOrderId() == "" {
		return nil, errs.New(errs.KindInvalidArgument, "ORDER_ID_REQUIRED", "order_id is required")
	}
	amt := req.GetAmount()
	cents := amt.GetUnits()*100 + int64(amt.GetNanos())/10_000_000
	if cents <= 0 {
		return nil, errs.New(errs.KindInvalidArgument, "BAD_AMOUNT", "amount must be positive")
	}
	p, err := s.store.Create(ctx, req.GetOrderId(), cents, amt.GetCurrencyCode(), req.GetMethodToken())
	if err != nil {
		return nil, err
	}
	return toProto(p, true), nil
}

func (s *Server) ConfirmPayment(ctx context.Context, req *paymentv1.ConfirmPaymentRequest) (*paymentv1.Payment, error) {
	cur, err := s.store.Get(ctx, req.GetPaymentId())
	if err != nil {
		return nil, err
	}

	to, reason := "AUTHORIZED", ""
	switch req.GetOutcome() {
	case paymentv1.ConfirmPaymentRequest_OUTCOME_FAIL:
		to, reason = "FAILED", "SHOPPER_ABANDONED"
	case paymentv1.ConfirmPaymentRequest_OUTCOME_AUTHORIZE:
		if cur.MethodToken == declineToken {
			to, reason = "FAILED", "CARD_DECLINED"
		}
	default:
		return nil, errs.New(errs.KindInvalidArgument, "OUTCOME_REQUIRED", "outcome must be AUTHORIZE or FAIL")
	}

	p, err := s.store.Transition(ctx, req.GetPaymentId(), to, reason)
	if err != nil {
		return nil, err
	}
	return toProto(p, false), nil
}

func (s *Server) Refund(ctx context.Context, req *paymentv1.RefundRequest) (*paymentv1.Payment, error) {
	p, err := s.store.Transition(ctx, req.GetPaymentId(), "REFUNDED", "")
	if err != nil {
		return nil, err
	}
	return toProto(p, false), nil
}

func (s *Server) Void(ctx context.Context, req *paymentv1.VoidRequest) (*paymentv1.Payment, error) {
	p, err := s.store.Transition(ctx, req.GetPaymentId(), "VOIDED", "")
	if err != nil {
		return nil, err
	}
	return toProto(p, false), nil
}

func toProto(p *store.Payment, withSecret bool) *paymentv1.Payment {
	out := &paymentv1.Payment{
		PaymentId: p.ID, OrderId: p.OrderID,
		Amount: &commonv1.Money{
			CurrencyCode: p.Currency,
			Units:        p.AmountCents / 100,
			Nanos:        int32(p.AmountCents%100) * 10_000_000,
		},
		Status:    statusToProto(p.Status),
		CreatedAt: p.CreatedAt.Format(time.RFC3339),
	}
	if withSecret {
		out.ClientSecret = p.ClientSecret
	}
	return out
}

func statusToProto(s string) paymentv1.PaymentStatus {
	switch s {
	case "REQUIRES_CONFIRMATION":
		return paymentv1.PaymentStatus_PAYMENT_STATUS_REQUIRES_CONFIRMATION
	case "AUTHORIZED":
		return paymentv1.PaymentStatus_PAYMENT_STATUS_AUTHORIZED
	case "FAILED":
		return paymentv1.PaymentStatus_PAYMENT_STATUS_FAILED
	case "REFUNDED":
		return paymentv1.PaymentStatus_PAYMENT_STATUS_REFUNDED
	case "VOIDED":
		return paymentv1.PaymentStatus_PAYMENT_STATUS_VOIDED
	default:
		return paymentv1.PaymentStatus_PAYMENT_STATUS_UNSPECIFIED
	}
}
