// Package channel dispatches a rendered notification to a delivery provider.
// v1 has only the sandbox LogChannel; production adds Email/SMS/Push channels
// behind the same interface.
package channel

import (
	"context"
	"log/slog"

	"github.com/deeprath/commerce-platform/services/notification/internal/domain"
)

// Channel delivers one notification. A returned error marks the notification
// FAILED; the caller still records it.
type Channel interface {
	Send(ctx context.Context, n domain.Notification) error
}

// LogChannel is the sandbox channel: it logs the message and always succeeds.
// It stands in for a real email/SMS provider.
type LogChannel struct{}

func (LogChannel) Send(ctx context.Context, n domain.Notification) error {
	slog.InfoContext(ctx, "notification dispatched (sandbox)",
		slog.String("kind", n.Kind),
		slog.String("owner_id", n.OwnerID),
		slog.String("channel", string(n.Channel)),
		slog.String("subject", n.Subject),
		slog.String("ref_id", n.RefID),
	)
	return nil
}
