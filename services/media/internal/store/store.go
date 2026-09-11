// Package store persists asset metadata and, on confirmation, writes the
// commerce.media.asset_ready outbox row in the same transaction.
package store

import (
	"context"
	"embed"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	mediav1 "github.com/deeprath/commerce-platform/gen/go/commerce/media/v1"
	pkgerrs "github.com/deeprath/commerce-platform/pkg/errs"
	"github.com/deeprath/commerce-platform/services/media/internal/domain"
)

//go:embed migrations/*.sql
var Migrations embed.FS

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const cols = `key, bucket, content_type, size_bytes, status, created_by, created_at, updated_at`

// Insert records a new PENDING asset.
func (s *Store) Insert(ctx context.Context, a *domain.Asset) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO assets (key, bucket, content_type, size_bytes, status, created_by)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		a.Key, a.Bucket, a.ContentType, a.SizeBytes, string(a.Status), a.CreatedBy)
	return wrap(err)
}

func (s *Store) Get(ctx context.Context, key string) (*domain.Asset, error) {
	return scan(s.pool.QueryRow(ctx, `SELECT `+cols+` FROM assets WHERE key=$1`, key))
}

// MarkReady persists the READY transition and the asset_ready outbox row atomically.
func (s *Store) MarkReady(ctx context.Context, a *domain.Asset, servedURL string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return wrap(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`UPDATE assets SET status='READY', size_bytes=$2, updated_at=now() WHERE key=$1`,
		a.Key, a.SizeBytes); err != nil {
		return wrap(err)
	}

	evt := &mediav1.AssetReady{
		Key: a.Key, Url: servedURL, ContentType: a.ContentType,
		SizeBytes: a.SizeBytes, OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	payload, err := proto.Marshal(evt)
	if err != nil {
		return pkgerrs.Wrap(err, pkgerrs.KindInternal, "EVENT_MARSHAL", "cannot marshal AssetReady")
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO outbox (topic, key, payload) VALUES ($1,$2,$3)`,
		"commerce.media.asset_ready", []byte(a.Key), payload); err != nil {
		return wrap(err)
	}
	return wrap(tx.Commit(ctx))
}

type rowScanner interface{ Scan(...any) error }

func scan(r rowScanner) (*domain.Asset, error) {
	var a domain.Asset
	var status string
	err := r.Scan(&a.Key, &a.Bucket, &a.ContentType, &a.SizeBytes, &status, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, pkgerrs.New(pkgerrs.KindNotFound, "ASSET_NOT_FOUND", "no such asset")
	}
	if err != nil {
		return nil, wrap(err)
	}
	a.Status = domain.Status(status)
	return &a, nil
}

func wrap(err error) error {
	if err == nil {
		return nil
	}
	return pkgerrs.Wrap(err, pkgerrs.KindInternal, "DB_ERROR", "database error: "+err.Error())
}
