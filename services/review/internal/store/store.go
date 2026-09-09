// Package store persists reviews and the verified-purchase index, and writes
// review.* outbox rows in the same transaction as the change.
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

	reviewv1 "github.com/deeprath/commerce-platform/gen/go/commerce/review/v1"
	pkgerrs "github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/review/internal/domain"
)

//go:embed migrations/*.sql
var Migrations embed.FS

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const cols = `id, product_id, author_id, author_name, rating, title, body, status,
	verified_purchase, created_at, updated_at`

// RecordPurchases adds (ownerID, productID) rows for every purchased product and
// records eventID in processed_events, atomically. A duplicate eventID
// short-circuits. Existing (owner, product) pairs are left untouched.
func (s *Store) RecordPurchases(ctx context.Context, ownerID string, productIDs []string, eventID string) error {
	if ownerID == "" || len(productIDs) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if eventID != "" {
		_, err := tx.Exec(ctx, `INSERT INTO processed_events (event_id) VALUES ($1)`, eventID)
		if isUniqueViolation(err) {
			return nil // already processed
		}
		if err != nil {
			return wrap(err)
		}
	}
	for _, pid := range productIDs {
		if pid == "" {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO purchases (owner_id, product_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
			ownerID, pid); err != nil {
			return wrap(err)
		}
	}
	return wrap(tx.Commit(ctx))
}

// HasPurchased reports whether ownerID has a recorded purchase of productID.
func (s *Store) HasPurchased(ctx context.Context, ownerID, productID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM purchases WHERE owner_id = $1 AND product_id = $2)`,
		ownerID, productID).Scan(&exists)
	return exists, wrap(err)
}

// Create inserts a review and its commerce.review.published outbox row. It
// returns ALREADY_EXISTS if the author has already reviewed the product.
func (s *Store) Create(ctx context.Context, r *domain.Review) (*domain.Review, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	out, err := scanOne(ctx, tx, `
		INSERT INTO reviews (product_id, author_id, author_name, rating, title, body, status, verified_purchase)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING `+cols,
		r.ProductID, r.AuthorID, r.AuthorName, r.Rating, r.Title, r.Body, string(r.Status), r.VerifiedPurchase)
	if isUniqueViolation(err) {
		return nil, pkgerrs.New(pkgerrs.KindAlreadyExists, "ALREADY_REVIEWED", "you have already reviewed this product")
	}
	if err != nil {
		return nil, err
	}

	if err := emitPublished(ctx, tx, out); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

// ListPublished returns published reviews for a product, newest first,
// keyset-paginated by created_at (cursor is an RFC3339Nano timestamp).
func (s *Store) ListPublished(ctx context.Context, productID string, limit int, before string) ([]*domain.Review, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	cursor := time.Now().Add(time.Hour)
	if before != "" {
		if t, err := time.Parse(time.RFC3339Nano, before); err == nil {
			cursor = t
		}
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+cols+` FROM reviews
		WHERE product_id = $1 AND status = 'PUBLISHED' AND created_at < $2
		ORDER BY created_at DESC LIMIT $3`,
		productID, cursor, limit+1)
	if err != nil {
		return nil, "", wrap(err)
	}
	defer rows.Close()

	var out []*domain.Review
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, r)
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

// Summary returns the aggregate published rating for a product.
func (s *Store) Summary(ctx context.Context, productID string) (domain.Summary, error) {
	sum := domain.Summary{ProductID: productID}
	rows, err := s.pool.Query(ctx, `
		SELECT rating, count(*) FROM reviews
		WHERE product_id = $1 AND status = 'PUBLISHED'
		GROUP BY rating`, productID)
	if err != nil {
		return sum, wrap(err)
	}
	defer rows.Close()

	var total, weighted int64
	for rows.Next() {
		var rating, n int32
		if err := rows.Scan(&rating, &n); err != nil {
			return sum, wrap(err)
		}
		if rating >= 1 && rating <= 5 {
			sum.Histogram[rating-1] = n
		}
		total += int64(n)
		weighted += int64(rating) * int64(n)
	}
	if err := rows.Err(); err != nil {
		return sum, wrap(err)
	}
	sum.Count = int32(total)
	if total > 0 {
		sum.Average = float64(weighted) / float64(total)
	}
	return sum, nil
}

// Moderate sets a review's status and, if it changed, emits the matching
// review.* event. Idempotent.
func (s *Store) Moderate(ctx context.Context, id string, to domain.Status) (*domain.Review, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := scanOne(ctx, tx, `SELECT `+cols+` FROM reviews WHERE id = $1 FOR UPDATE`, id)
	if err != nil {
		return nil, err
	}
	if cur.Status == to {
		return cur, nil
	}

	out, err := scanOne(ctx, tx,
		`UPDATE reviews SET status = $2, updated_at = now() WHERE id = $1 RETURNING `+cols,
		id, string(to))
	if err != nil {
		return nil, err
	}

	switch to {
	case domain.StatusPublished:
		err = emitPublished(ctx, tx, out)
	case domain.StatusHidden:
		b, mErr := proto.Marshal(&reviewv1.ReviewHidden{
			ReviewId: out.ID, ProductId: out.ProductID, OccurredAt: nowRFC3339(),
		})
		if mErr != nil {
			return nil, pkgerrs.Wrap(mErr, pkgerrs.KindInternal, "EVENT_MARSHAL", "marshal ReviewHidden")
		}
		err = emitOutbox(ctx, tx, "commerce.review.hidden", out.ProductID, b)
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

func emitPublished(ctx context.Context, tx pgx.Tx, r *domain.Review) error {
	b, err := proto.Marshal(&reviewv1.ReviewPublished{
		ReviewId: r.ID, ProductId: r.ProductID, AuthorId: r.AuthorID,
		Rating: r.Rating, OccurredAt: nowRFC3339(),
	})
	if err != nil {
		return pkgerrs.Wrap(err, pkgerrs.KindInternal, "EVENT_MARSHAL", "marshal ReviewPublished")
	}
	return emitOutbox(ctx, tx, "commerce.review.published", r.ProductID, b)
}

func emitOutbox(ctx context.Context, tx pgx.Tx, topic, key string, payload []byte) error {
	_, err := tx.Exec(ctx, `INSERT INTO outbox (topic, key, payload) VALUES ($1,$2,$3)`, topic, []byte(key), payload)
	return wrap(err)
}

// --- row scanning ---------------------------------------------------------

type rowScanner interface {
	Scan(dest ...any) error
}

type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func scanOne(ctx context.Context, q querier, sql string, args ...any) (*domain.Review, error) {
	r, err := scanRow(q.QueryRow(ctx, sql, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, pkgerrs.New(pkgerrs.KindNotFound, "REVIEW_NOT_FOUND", "no such review")
	}
	return r, err
}

func scanRow(rs rowScanner) (*domain.Review, error) {
	var (
		r  domain.Review
		st string
	)
	if err := rs.Scan(
		&r.ID, &r.ProductID, &r.AuthorID, &r.AuthorName, &r.Rating, &r.Title, &r.Body,
		&st, &r.VerifiedPurchase, &r.CreatedAt, &r.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, wrap(err)
	}
	r.Status = domain.Status(st)
	return &r, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func wrap(err error) error {
	if err == nil {
		return nil
	}
	return pkgerrs.Wrap(err, pkgerrs.KindInternal, "DB_ERROR", "database error: "+err.Error())
}
