// Package grpcsvc adapts NotificationService to the store + channel.
package grpcsvc

import (
	"context"
	"time"

	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	notificationv1 "github.com/deeprath/commerce-platform/gen/go/commerce/notification/v1"
	"github.com/deeprath/commerce-platform/pkg/auth"
	"github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/notification/internal/channel"
	"github.com/deeprath/commerce-platform/services/notification/internal/domain"
	"github.com/deeprath/commerce-platform/services/notification/internal/store"
)

type Server struct {
	notificationv1.UnimplementedNotificationServiceServer
	store *store.Store
	ch    channel.Sender
}

func New(s *store.Store, ch channel.Sender) *Server { return &Server{store: s, ch: ch} }

func (s *Server) ListNotifications(ctx context.Context, req *notificationv1.ListNotificationsRequest) (*notificationv1.ListNotificationsResponse, error) {
	p := auth.FromContext(ctx)
	if p == nil {
		return nil, errs.New(errs.KindUnauthenticated, "NOT_AUTHENTICATED", "sign-in required")
	}
	items, next, err := s.store.List(ctx, p.Subject,
		int(req.GetPage().GetPageSize()), req.GetPage().GetPageToken())
	if err != nil {
		return nil, err
	}
	out := &notificationv1.ListNotificationsResponse{Page: &commonv1.PageResponse{NextPageToken: next, TotalSize: -1}}
	for _, n := range items {
		out.Notifications = append(out.Notifications, toProto(n))
	}
	return out, nil
}

func (s *Server) SendTest(ctx context.Context, _ *notificationv1.SendTestRequest) (*notificationv1.Notification, error) {
	p := auth.FromContext(ctx)
	if p == nil {
		return nil, errs.New(errs.KindUnauthenticated, "NOT_AUTHENTICATED", "sign-in required")
	}
	rendered, _ := domain.Render("test", nil)
	n := domain.Notification{
		OwnerID: p.Subject, Kind: "test", Channel: domain.ChannelEmail,
		Status: domain.StatusSent, Subject: rendered.Subject, Body: rendered.Body,
	}
	if err := s.ch.Send(ctx, n); err != nil {
		n.Status = domain.StatusFailed
	}
	saved, err := s.store.Record(ctx, n, "")
	if err != nil {
		return nil, err
	}
	return toProto(saved), nil
}

func toProto(n *domain.Notification) *notificationv1.Notification {
	return &notificationv1.Notification{
		Id: n.ID, OwnerId: n.OwnerID, Kind: n.Kind,
		Channel:   channelToProto(n.Channel),
		Status:    statusToProto(n.Status),
		Subject:   n.Subject,
		Body:      n.Body,
		RefId:     n.RefID,
		CreatedAt: n.CreatedAt.Format(time.RFC3339),
	}
}

func channelToProto(c domain.Channel) notificationv1.Channel {
	switch c {
	case domain.ChannelEmail:
		return notificationv1.Channel_CHANNEL_EMAIL
	case domain.ChannelSMS:
		return notificationv1.Channel_CHANNEL_SMS
	case domain.ChannelPush:
		return notificationv1.Channel_CHANNEL_PUSH
	default:
		return notificationv1.Channel_CHANNEL_UNSPECIFIED
	}
}

func statusToProto(s domain.Status) notificationv1.DeliveryStatus {
	switch s {
	case domain.StatusSent:
		return notificationv1.DeliveryStatus_DELIVERY_STATUS_SENT
	case domain.StatusFailed:
		return notificationv1.DeliveryStatus_DELIVERY_STATUS_FAILED
	default:
		return notificationv1.DeliveryStatus_DELIVERY_STATUS_UNSPECIFIED
	}
}
