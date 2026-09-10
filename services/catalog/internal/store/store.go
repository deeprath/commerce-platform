// Package store is the catalog's PostgreSQL persistence. Every product change is
// written together with its commerce.catalog.product_changed outbox row in one
// transaction (no dual-write).
package store

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	catalogv1 "github.com/deeprath/commerce-platform/gen/go/commerce/catalog/v1"
	commonv1 "github.com/deeprath/commerce-platform/gen/go/commerce/common/v1"
	pkgerrs "github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/catalog/internal/domain"
)

//go:embed migrations/*.sql
var Migrations embed.FS

// Store is the product repository.
type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const productCols = `id, slug, title, description, category_id,
	price_currency, price_units, price_nanos, media_keys, status, attributes,
	shop_id, created_by, created_at, updated_at`

// Create inserts p (status DRAFT) and its product_changed outbox row atomically.
func (s *Store) Create(ctx context.Context, p *domain.Product) (*domain.Product, error) {
	return s.writeTx(ctx, catalogv1.ChangeType_CHANGE_TYPE_CREATED, func(tx pgx.Tx) (*domain.Product, error) {
		row := tx.QueryRow(ctx, `
			INSERT INTO products
				(slug, title, description, category_id,
				 price_currency, price_units, price_nanos, media_keys, status, attributes, shop_id, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			RETURNING `+productCols,
			p.Slug, p.Title, p.Description, p.CategoryID,
			p.ListPrice.CurrencyCode, p.ListPrice.Units, p.ListPrice.Nanos,
			nonNil(p.MediaKeys), string(p.Status), mustJSON(p.Attributes), nullUUID(p.ShopID), p.CreatedBy)
		return scanProduct(row)
	})
}

// Update writes the mutable fields of an existing product + its outbox row.
func (s *Store) Update(ctx context.Context, p *domain.Product) (*domain.Product, error) {
	return s.writeTx(ctx, catalogv1.ChangeType_CHANGE_TYPE_UPDATED, func(tx pgx.Tx) (*domain.Product, error) {
		row := tx.QueryRow(ctx, `
			UPDATE products SET
				title=$2, description=$3, category_id=$4,
				price_currency=$5, price_units=$6, price_nanos=$7,
				media_keys=$8, status=$9, attributes=$10, updated_at=now()
			WHERE id=$1
			RETURNING `+productCols,
			p.ID, p.Title, p.Description, p.CategoryID,
			p.ListPrice.CurrencyCode, p.ListPrice.Units, p.ListPrice.Nanos,
			nonNil(p.MediaKeys), string(p.Status), mustJSON(p.Attributes))
		return scanProduct(row)
	})
}

// Archive soft-deletes and emits an ARCHIVED change.
func (s *Store) Archive(ctx context.Context, id string) (*domain.Product, error) {
	return s.writeTx(ctx, catalogv1.ChangeType_CHANGE_TYPE_ARCHIVED, func(tx pgx.Tx) (*domain.Product, error) {
		row := tx.QueryRow(ctx,
			`UPDATE products SET status='ARCHIVED', updated_at=now() WHERE id=$1 RETURNING `+productCols, id)
		return scanProduct(row)
	})
}

func (s *Store) Get(ctx context.Context, id string) (*domain.Product, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+productCols+` FROM products WHERE id=$1`, id)
	return scanProduct(row)
}

func (s *Store) GetBySlug(ctx context.Context, slug string) (*domain.Product, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+productCols+` FROM products WHERE slug=$1`, slug)
	return scanProduct(row)
}

// List returns ACTIVE products newest-first, optionally filtered by category,
// paginated by a created_at|id keyset cursor.
func (s *Store) List(ctx context.Context, categoryID string, limit int, cursor *Cursor) ([]*domain.Product, *Cursor, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	args := []any{limit + 1}
	q := `SELECT ` + productCols + ` FROM products WHERE status='ACTIVE'`
	if categoryID != "" {
		args = append(args, categoryID)
		q += ` AND category_id=$2`
	}
	if cursor != nil {
		args = append(args, cursor.CreatedAt, cursor.ID)
		q += ` AND (created_at, id) < ($` + strconv.Itoa(len(args)-1) + `, $` + strconv.Itoa(len(args)) + `)`
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT $1`

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, nil, wrapPG(err)
	}
	defer rows.Close()

	var out []*domain.Product
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, wrapPG(err)
	}

	var next *Cursor
	if len(out) > limit {
		last := out[limit-1]
		next = &Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
		out = out[:limit]
	}
	return out, next, nil
}

// ListByShop returns a shop's products of ALL statuses (draft/active/archived),
// newest-first, keyset-paginated. For the seller's own management view.
func (s *Store) ListByShop(ctx context.Context, shopID string, limit int, cursor *Cursor) ([]*domain.Product, *Cursor, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	args := []any{limit + 1, shopID}
	q := `SELECT ` + productCols + ` FROM products WHERE shop_id = $2`
	if cursor != nil {
		args = append(args, cursor.CreatedAt, cursor.ID)
		q += ` AND (created_at, id) < ($3, $4)`
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT $1`

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, nil, wrapPG(err)
	}
	defer rows.Close()

	var out []*domain.Product
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, wrapPG(err)
	}
	var next *Cursor
	if len(out) > limit {
		last := out[limit-1]
		next = &Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
		out = out[:limit]
	}
	return out, next, nil
}

func (s *Store) BatchGet(ctx context.Context, ids []string) ([]*domain.Product, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+productCols+` FROM products WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, wrapPG(err)
	}
	defer rows.Close()
	var out []*domain.Product
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, wrapPG(rows.Err())
}

// Cursor is an opaque keyset pagination position.
type Cursor struct {
	CreatedAt time.Time
	ID        string
}

// --- internals ---

func (s *Store) writeTx(ctx context.Context, change catalogv1.ChangeType, fn func(pgx.Tx) (*domain.Product, error)) (*domain.Product, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, wrapPG(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	p, err := fn(tx)
	if err != nil {
		return nil, err
	}
	if err := insertOutbox(ctx, tx, change, p); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapPG(err)
	}
	return p, nil
}

func insertOutbox(ctx context.Context, tx pgx.Tx, change catalogv1.ChangeType, p *domain.Product) error {
	evt := &catalogv1.ProductChanged{
		ProductId:  p.ID,
		Change:     change,
		Product:    toProto(p),
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	payload, err := proto.Marshal(evt)
	if err != nil {
		return pkgerrs.Wrap(err, pkgerrs.KindInternal, "EVENT_MARSHAL", "cannot marshal ProductChanged")
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO outbox (topic, key, payload) VALUES ($1, $2, $3)`,
		"commerce.catalog.product_changed", []byte(p.ID), payload)
	return wrapPG(err)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanProduct(row scanner) (*domain.Product, error) {
	var (
		p         domain.Product
		status    string
		attrsJSON []byte
		shopID    *string
	)
	err := row.Scan(
		&p.ID, &p.Slug, &p.Title, &p.Description, &p.CategoryID,
		&p.ListPrice.CurrencyCode, &p.ListPrice.Units, &p.ListPrice.Nanos,
		&p.MediaKeys, &status, &attrsJSON, &shopID, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, pkgerrs.New(pkgerrs.KindNotFound, "PRODUCT_NOT_FOUND", "no such product")
	}
	if err != nil {
		return nil, wrapPG(err)
	}
	if shopID != nil {
		p.ShopID = *shopID
	}
	p.Status = domain.Status(status)
	if len(attrsJSON) > 0 {
		if err := json.Unmarshal(attrsJSON, &p.Attributes); err != nil {
			return nil, pkgerrs.Wrap(err, pkgerrs.KindInternal, "ATTRS_DECODE", "corrupt attributes json")
		}
	}
	return &p, nil
}

func wrapPG(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return pkgerrs.Wrap(err, pkgerrs.KindAlreadyExists, "SLUG_TAKEN", "a product with that slug already exists")
		case "22P02": // invalid_text_representation — e.g. a non-UUID id
			return pkgerrs.New(pkgerrs.KindNotFound, "PRODUCT_NOT_FOUND", "no such product")
		}
		// Keep the pg detail in Message (logged, never returned to the client).
		return pkgerrs.Wrap(err, pkgerrs.KindInternal, "DB_ERROR", "pg "+pgErr.Code+": "+pgErr.Message)
	}
	return pkgerrs.Wrap(err, pkgerrs.KindInternal, "DB_ERROR", "database error: "+err.Error())
}

// nonNil turns a nil slice into an empty one so pgx encodes '{}' not NULL.
// nullUUID maps "" -> NULL for a nullable uuid column.
func nullUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// toProto is duplicated from grpcsvc to avoid an import cycle; kept tiny.
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
		Status:     statusToProto(p.Status),
		Attributes: p.Attributes,
	}
}

func statusToProto(s domain.Status) catalogv1.ProductStatus {
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
