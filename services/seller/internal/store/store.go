// Package store persists shops and writes commerce.shop.* outbox rows in the
// same transaction as the change that produced them.
package store

import (
	"context"
	"embed"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	sellerv1 "github.com/deeprath/commerce-platform/gen/go/commerce/seller/v1"
	pkgerrs "github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/seller/internal/domain"
)

//go:embed migrations/*.sql
var Migrations embed.FS

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const cols = `id, owner_id, name, slug, description, contact_email, status,
	suspension_reason, created_at, updated_at`

// Create inserts a shop and its commerce.shop.created outbox row. It returns
// ALREADY_EXISTS if the owner already has a shop or the slug is taken.
func (s *Store) Create(ctx context.Context, sh *domain.Shop) (*domain.Shop, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	out, err := scanOne(ctx, tx, `
		INSERT INTO shops (owner_id, name, slug, description, contact_email, status)
		VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING `+cols,
		sh.OwnerID, sh.Name, sh.Slug, sh.Description, sh.ContactEmail, string(sh.Status))
	if isUniqueViolation(err) {
		return nil, pkgerrs.New(pkgerrs.KindAlreadyExists, "SHOP_EXISTS",
			"you already have a shop, or that name is taken")
	}
	if err != nil {
		return nil, err
	}

	if err := emit(ctx, tx, "commerce.shop.created", out.ID, &sellerv1.ShopCreated{
		ShopId: out.ID, OwnerId: out.OwnerID, Name: out.Name, Slug: out.Slug, OccurredAt: nowRFC3339(),
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

// GetByOwner returns the shop owned by ownerID, NOT_FOUND if none.
func (s *Store) GetByOwner(ctx context.Context, ownerID string) (*domain.Shop, error) {
	return scanOne(ctx, s.pool, `SELECT `+cols+` FROM shops WHERE owner_id = $1`, ownerID)
}

// GetByID returns one shop, NOT_FOUND for a missing / malformed id.
func (s *Store) GetByID(ctx context.Context, id string) (*domain.Shop, error) {
	if err := notFoundID(id); err != nil {
		return nil, err
	}
	return scanOne(ctx, s.pool, `SELECT `+cols+` FROM shops WHERE id = $1`, id)
}

// GetBySlug returns one shop by slug, NOT_FOUND if none.
func (s *Store) GetBySlug(ctx context.Context, slug string) (*domain.Shop, error) {
	return scanOne(ctx, s.pool, `SELECT `+cols+` FROM shops WHERE slug = $1`, slug)
}

// Update rewrites the display fields of the shop owned by ownerID.
func (s *Store) Update(ctx context.Context, ownerID, name, description, contactEmail string) (*domain.Shop, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := scanOne(ctx, tx, `SELECT `+cols+` FROM shops WHERE owner_id = $1 FOR UPDATE`, ownerID)
	if err != nil {
		return nil, err
	}
	if err := cur.ApplyUpdate(name, description, contactEmail); err != nil {
		return nil, err
	}
	out, err := scanOne(ctx, tx, `
		UPDATE shops SET name = $2, description = $3, contact_email = $4, updated_at = now()
		WHERE id = $1 RETURNING `+cols,
		cur.ID, cur.Name, cur.Description, cur.ContactEmail)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

// List returns shops newest first, keyset-paginated by created_at (cursor is an
// RFC3339Nano timestamp). An empty status means "any".
func (s *Store) List(ctx context.Context, status domain.Status, limit int, before string) ([]*domain.Shop, string, error) {
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
		SELECT `+cols+` FROM shops
		WHERE ($1 = '' OR status = $1) AND created_at < $2
		ORDER BY created_at DESC LIMIT $3`,
		string(status), cursor, limit+1)
	if err != nil {
		return nil, "", wrap(err)
	}
	defer rows.Close()

	var out []*domain.Shop
	for rows.Next() {
		sh, err := scanRow(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, sh)
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

// Transition applies fn (Activate / Suspend) under a row lock and, if it
// reports a change, emits the matching commerce.shop.* event. Idempotent.
func (s *Store) Transition(ctx context.Context, id string, fn func(*domain.Shop) (bool, error)) (*domain.Shop, error) {
	if err := notFoundID(id); err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := scanOne(ctx, tx, `SELECT `+cols+` FROM shops WHERE id = $1 FOR UPDATE`, id)
	if err != nil {
		return nil, err
	}
	changed, err := fn(cur)
	if err != nil {
		return nil, err
	}
	if !changed {
		return cur, nil
	}

	out, err := scanOne(ctx, tx, `
		UPDATE shops SET status = $2, suspension_reason = $3, updated_at = now()
		WHERE id = $1 RETURNING `+cols,
		cur.ID, string(cur.Status), cur.SuspensionReason)
	if err != nil {
		return nil, err
	}

	var evErr error
	switch out.Status {
	case domain.StatusActive:
		evErr = emit(ctx, tx, "commerce.shop.activated", out.ID, &sellerv1.ShopActivated{
			ShopId: out.ID, OwnerId: out.OwnerID, Slug: out.Slug, OccurredAt: nowRFC3339(),
		})
	case domain.StatusSuspended:
		evErr = emit(ctx, tx, "commerce.shop.suspended", out.ID, &sellerv1.ShopSuspended{
			ShopId: out.ID, OwnerId: out.OwnerID, Reason: out.SuspensionReason, OccurredAt: nowRFC3339(),
		})
	}
	if evErr != nil {
		return nil, evErr
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

func emit(ctx context.Context, tx pgx.Tx, topic, key string, m proto.Message) error {
	b, err := proto.Marshal(m)
	if err != nil {
		return pkgerrs.Wrap(err, pkgerrs.KindInternal, "EVENT_MARSHAL", "marshal "+topic)
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox (topic, key, payload) VALUES ($1,$2,$3)`, topic, []byte(key), b)
	return wrap(err)
}

// --- row scanning ---------------------------------------------------------

type rowScanner interface{ Scan(dest ...any) error }

type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func scanOne(ctx context.Context, q querier, sql string, args ...any) (*domain.Shop, error) {
	sh, err := scanRow(q.QueryRow(ctx, sql, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, pkgerrs.New(pkgerrs.KindNotFound, "SHOP_NOT_FOUND", "no such shop")
	}
	return sh, err
}

func scanRow(rs rowScanner) (*domain.Shop, error) {
	var (
		sh domain.Shop
		st string
	)
	if err := rs.Scan(
		&sh.ID, &sh.OwnerID, &sh.Name, &sh.Slug, &sh.Description, &sh.ContactEmail,
		&st, &sh.SuspensionReason, &sh.CreatedAt, &sh.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, wrap(err)
	}
	sh.Status = domain.Status(st)
	return &sh, nil
}

// notFoundID rejects a syntactically invalid UUID before it reaches a
// `WHERE id = $1` on a uuid column (which would be a 500). A malformed id
// definitionally matches no row.
func notFoundID(id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return pkgerrs.New(pkgerrs.KindNotFound, "SHOP_NOT_FOUND", "no such shop")
	}
	return nil
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
