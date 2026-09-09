// Package pgx centralises PostgreSQL access: a tuned pgxpool and a goose-based
// migration runner. Every service owns its own database; nothing here reaches
// across service schemas.
package pgx

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// PoolConfig tunes a connection pool. Zero values get sane defaults.
type PoolConfig struct {
	DSN             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

// NewPool parses cfg, applies defaults, connects, and pings.
func NewPool(ctx context.Context, cfg PoolConfig) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("pgx: parse DSN: %w", err)
	}
	pc.MaxConns = orDefaultInt32(cfg.MaxConns, 10)
	pc.MinConns = orDefaultInt32(cfg.MinConns, 2)
	pc.MaxConnLifetime = orDefaultDur(cfg.MaxConnLifetime, time.Hour)
	pc.MaxConnIdleTime = orDefaultDur(cfg.MaxConnIdleTime, 30*time.Minute)
	pc.HealthCheckPeriod = time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("pgx: connect: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgx: ping: %w", err)
	}
	return pool, nil
}

// Migrate runs all up migrations from migrationsFS (a directory of *.sql files)
// against the given DSN. Call it on service startup, before serving.
func Migrate(ctx context.Context, dsn string, migrationsFS fs.FS, dir string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("pgx: open for migrate: %w", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if err := goose.UpContext(ctx, db, dir); err != nil {
		return fmt.Errorf("pgx: goose up: %w", err)
	}
	return nil
}

// ensure database/sql knows the pgx driver (side-effect import guard).
var _ = stdlib.GetDefaultDriver

func orDefaultInt32(v, def int32) int32 {
	if v == 0 {
		return def
	}
	return v
}

func orDefaultDur(v, def time.Duration) time.Duration {
	if v == 0 {
		return def
	}
	return v
}
