package db

import (
	"context"
	"fmt"
	"order-sync/migrations"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Migrator interface{ Migrate(context.Context) error }

type migrationExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

type migrator struct{ pool migrationExecutor }

func NewMigrator(pool *pgxpool.Pool) Migrator { return &migrator{pool: pool} }
func (m *migrator) Migrate(ctx context.Context) error {
	_, err := m.pool.Exec(ctx, migrations.SQL)
	if err != nil {
		return fmt.Errorf("execute schema migration: %w", err)
	}
	return nil
}
