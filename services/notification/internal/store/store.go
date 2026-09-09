// Package store persists notifications and writes the notification.sent outbox
// row in the same transaction. The Kafka consumer dedupes on processed_events.
package store

import (
	"context"
	"embed"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	notificationv1 "github.com/deeprath/commerce-platform/gen/go/commerce/notification/v1"
	pkgerrs "github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/notification/internal/domain"
)

//go:embed migrations/*.sql
var Migrations embed.FS

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const cols = `id, owner_id, kind, channel, status, subject, body, ref_id, created_at`

// Record persists one notification plus its commerce.notification.sent outbox
// row and records eventID in processed_events, atomically. A duplicate eventID
// short-circuits with (nil, nil). eventID may be "" for RPC-originated sends.
func (s *Store) Record(ctx context.Context, n domain.Notification, eventID string) (*domain.Notification, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if eventID != "" {
		_, err := tx.Exec(ctx, `INSERT INTO processed_events (event_id) VALUES ($1)`, eventID)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, nil // already processed
		}
		if err != nil {
			return nil, wrap(err)
		}
	}

	var out domain.Notification
	var ch, st string
	if err := tx.QueryRow(ctx, `
		INSERT INTO notifications (owner_id, kind, channel, status, subject, body, ref_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING `+cols,
		n.OwnerID, n.Kind, string(n.Channel), string(n.Status), n.Subject, n.Body, n.RefID,
	).Scan(&out.ID, &out.OwnerID, &out.Kind, &ch, &st, &out.Subject, &out.Body, &out.RefID, &out.CreatedAt); err != nil {
		return nil, wrap(err)
	}
	out.Channel, out.Status = domain.Channel(ch), domain.Status(st)

	evt := &notificationv1.NotificationSent{
		NotificationId: out.ID, OwnerId: out.OwnerID, Kind: out.Kind,
		Channel: string(out.Channel), RefId: out.RefID,
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	b, err := proto.Marshal(evt)
	if err != nil {
		return nil, pkgerrs.Wrap(err, pkgerrs.KindInternal, "EVENT_MARSHAL", "marshal NotificationSent")
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO outbox (topic, key, payload) VALUES ($1,$2,$3)`,
		"commerce.notification.sent", []byte(out.OwnerID), b); err != nil {
		return nil, wrap(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, wrap(err)
	}
	return &out, nil
}

// List returns the caller's notifications, newest first, keyset-paginated by
// created_at (cursor is an RFC3339Nano timestamp).
func (s *Store) List(ctx context.Context, ownerID string, limit int, before string) ([]*domain.Notification, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	cursor := time.Now().Add(time.Hour)
	if before != "" {
		if t, err := time.Parse(time.RFC3339Nano, before); err == nil {
			cursor = t
		}
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+cols+` FROM notifications
		WHERE owner_id = $1 AND created_at < $2
		ORDER BY created_at DESC LIMIT $3`,
		ownerID, cursor, limit+1)
	if err != nil {
		return nil, "", wrap(err)
	}
	defer rows.Close()

	var out []*domain.Notification
	for rows.Next() {
		var n domain.Notification
		var ch, st string
		if err := rows.Scan(&n.ID, &n.OwnerID, &n.Kind, &ch, &st, &n.Subject, &n.Body, &n.RefID, &n.CreatedAt); err != nil {
			return nil, "", wrap(err)
		}
		n.Channel, n.Status = domain.Channel(ch), domain.Status(st)
		out = append(out, &n)
	}
	if err := rows.Err(); err != nil {
		return nil, "", wrap(err)
	}

	next := ""
	if len(out) > limit {
		next = out[limit-1].CreatedAt.Format(time.RFC3339Nano)
		out = out[:limit]
	}
	return out, next, nil
}

func wrap(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return pkgerrs.New(pkgerrs.KindNotFound, "NOT_FOUND", "not found")
	}
	return pkgerrs.Wrap(err, pkgerrs.KindInternal, "DB_ERROR", "database error: "+err.Error())
}
