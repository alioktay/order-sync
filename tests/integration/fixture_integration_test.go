//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"order-sync/internal/db"
)

const (
	testPostgresImage    = "postgres:17-alpine"
	testPostgresDatabase = "order_sync_test"
	testPostgresUser     = "order_sync_test"
	testPostgresPassword = "integration-password"
)

var (
	integrationDB        *pgxpool.Pool
	integrationContainer *postgres.PostgresContainer
)

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		var err error
		integrationContainer, err = postgres.Run(
			ctx,
			testPostgresImage,
			postgres.WithDatabase(testPostgresDatabase),
			postgres.WithUsername(testPostgresUser),
			postgres.WithPassword(testPostgresPassword),
			testcontainers.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).
					WithStartupTimeout(90*time.Second),
			),
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "integration tests could not start PostgreSQL Testcontainer; ensure Docker is running or set TEST_DATABASE_URL: %v\n", err)
			os.Exit(1)
		}
		databaseURL, err = integrationContainer.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			fmt.Fprintf(os.Stderr, "integration tests could not retrieve the PostgreSQL Testcontainer connection string: %v\n", err)
			_ = integrationContainer.Terminate(context.Background())
			os.Exit(1)
		}
	}

	var err error
	integrationDB, err = pgxpool.New(ctx, databaseURL)
	if err == nil {
		err = integrationDB.Ping(ctx)
	}
	if err == nil {
		err = db.NewMigrator(integrationDB).Migrate(ctx)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration tests could not initialize PostgreSQL schema: %v\n", err)
		if integrationDB != nil {
			integrationDB.Close()
		}
		if integrationContainer != nil {
			_ = integrationContainer.Terminate(context.Background())
		}
		os.Exit(1)
	}

	code := m.Run()
	integrationDB.Close()
	if integrationContainer != nil {
		if err := integrationContainer.Terminate(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not terminate PostgreSQL Testcontainer: %v\n", err)
		}
	}
	os.Exit(code)
}

func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if integrationDB == nil {
		t.Fatal("integration PostgreSQL pool is not initialized")
	}
	if _, err := integrationDB.Exec(context.Background(), `
		TRUNCATE payments, webhook_events, sync_jobs, sku_classifications, order_items, orders
		RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("reset integration database: %v", err)
	}
	if _, err := integrationDB.Exec(context.Background(), `
		INSERT INTO sku_classifications (sku, category) VALUES
			('NUKI-SL3', 'HARDWARE'),
			('NUKI-BRIDGE', 'HARDWARE'),
			('NUKI-SMART-HOSTING', 'DIGITAL')`); err != nil {
		t.Fatalf("seed SKU classifications: %v", err)
	}
	return integrationDB
}
