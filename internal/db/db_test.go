package db

import (
	"context"
	"errors"
	"strings"
	"testing"

	"order-sync/internal/config"
	"order-sync/internal/contracts"
	"order-sync/migrations"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type migrationExecutorStub struct {
	err   error
	query string
}

func (s *migrationExecutorStub) Exec(_ context.Context, query string, _ ...any) (pgconn.CommandTag, error) {
	s.query = query
	return pgconn.CommandTag{}, s.err
}

type transactionBeginnerStub struct {
	tx  pgx.Tx
	err error
}

func (s transactionBeginnerStub) Begin(context.Context) (pgx.Tx, error) { return s.tx, s.err }

type transactionStub struct {
	pgx.Tx
	commitErr  error
	committed  bool
	rolledBack bool
}

func (s *transactionStub) Commit(context.Context) error {
	s.committed = true
	return s.commitErr
}

func (s *transactionStub) Rollback(context.Context) error {
	s.rolledBack = true
	return nil
}

func TestNewPoolFromConfig(t *testing.T) {
	pool, err := NewPoolFromConfig(config.Config{DatabaseURL: "postgresql://user:password@localhost:5432/orders"})
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()

	if pool, err = NewPoolFromConfig(config.Config{DatabaseURL: "://invalid"}); err == nil || pool != nil {
		t.Fatalf("NewPoolFromConfig() = %v, %v; want nil, error", pool, err)
	}
}

func TestMigratorExecutesEmbeddedMigration(t *testing.T) {
	executor := &migrationExecutorStub{}
	migrator := &migrator{pool: executor}
	if err := migrator.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if executor.query != migrations.SQL || executor.query == "" {
		t.Fatal("migrator did not execute the embedded migration")
	}

	expected := errors.New("migration failed")
	executor.err = expected
	if err := migrator.Migrate(context.Background()); !errors.Is(err, expected) {
		t.Fatalf("Migrate() error = %v", err)
	}
}

func TestInitialMigrationDefinesCurrentSchema(t *testing.T) {
	for _, fragment := range []string{
		"is_hardware BOOLEAN",
		"waiting_since TIMESTAMPTZ",
	} {
		if !strings.Contains(migrations.SQL, fragment) {
			t.Errorf("migration is missing direct schema definition %q", fragment)
		}
	}

	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS webhook_events",
		"idx_webhook_events_payment_reference_order_id",
	} {
		if !strings.Contains(migrations.SQL, fragment) {
			t.Errorf("migration is missing current-schema operation %q", fragment)
		}
	}
}

func TestNewDatabaseAdapters(t *testing.T) {
	if _, ok := NewMigrator(nil).(*migrator); !ok {
		t.Fatal("NewMigrator() returned the wrong implementation")
	}
	if _, ok := NewTransactionRunner(nil).(*PostgresTransactionRunner); !ok {
		t.Fatal("NewTransactionRunner() returned the wrong implementation")
	}
}

func TestTransactionRunner(t *testing.T) {
	ctx := context.Background()
	expectedResult := contracts.WebhookResult{Message: "stored"}

	t.Run("begin failure", func(t *testing.T) {
		expected := errors.New("begin failed")
		runner := &PostgresTransactionRunner{pool: transactionBeginnerStub{err: expected}}
		result, err := runner.Run(ctx, func(contracts.TransactionRepositories) (contracts.WebhookResult, error) {
			t.Fatal("work must not run")
			return contracts.WebhookResult{}, nil
		})
		if !errors.Is(err, expected) || result != (contracts.WebhookResult{}) {
			t.Fatalf("Run() = %+v, %v", result, err)
		}
	})

	t.Run("work failure rolls back", func(t *testing.T) {
		tx := &transactionStub{}
		expected := errors.New("work failed")
		runner := &PostgresTransactionRunner{pool: transactionBeginnerStub{tx: tx}}
		result, err := runner.Run(ctx, func(repositories contracts.TransactionRepositories) (contracts.WebhookResult, error) {
			if repositories.Orders == nil || repositories.Events == nil {
				t.Fatal("transaction repositories were not provided")
			}
			return expectedResult, expected
		})
		if !errors.Is(err, expected) || !tx.rolledBack || tx.committed || result != (contracts.WebhookResult{}) {
			t.Fatalf("Run() = %+v, %v (rollback=%v, commit=%v)", result, err, tx.rolledBack, tx.committed)
		}
	})

	t.Run("commit failure", func(t *testing.T) {
		expected := errors.New("commit failed")
		tx := &transactionStub{commitErr: expected}
		runner := &PostgresTransactionRunner{pool: transactionBeginnerStub{tx: tx}}
		result, err := runner.Run(ctx, func(contracts.TransactionRepositories) (contracts.WebhookResult, error) { return expectedResult, nil })
		if !errors.Is(err, expected) || !tx.committed || result != (contracts.WebhookResult{}) {
			t.Fatalf("Run() = %+v, %v (commit=%v)", result, err, tx.committed)
		}
	})

	t.Run("success", func(t *testing.T) {
		tx := &transactionStub{}
		runner := &PostgresTransactionRunner{pool: transactionBeginnerStub{tx: tx}}
		result, err := runner.Run(ctx, func(contracts.TransactionRepositories) (contracts.WebhookResult, error) { return expectedResult, nil })
		if err != nil || !tx.committed || !tx.rolledBack || result != expectedResult {
			t.Fatalf("Run() = %+v, %v (rollback=%v, commit=%v)", result, err, tx.rolledBack, tx.committed)
		}
	})
}
