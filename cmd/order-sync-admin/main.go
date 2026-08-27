package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"order-sync/internal/db"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const adminTimeout = 30 * time.Second

var requiredTables = []string{
	"orders",
	"order_items",
	"sku_classifications",
	"webhook_events",
	"payments",
	"sync_jobs",
}

type adminDB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func main() {
	command := "verify"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if command != "migrate" && command != "verify" && command != "replay" {
		_, _ = fmt.Fprintln(os.Stderr, "usage: order-sync-admin [migrate|verify|replay <job-id>]")
		os.Exit(2)
	}
	if command == "replay" {
		if _, err := replayJobID(os.Args); err != nil {
			fatalUsage(err)
		}
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		fatal(errors.New("DATABASE_URL is required"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), adminTimeout)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		fatal(errors.New("error create database pool: " + err.Error()))
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		fatal(errors.New("database is unavailable: " + err.Error()))
	}

	if command == "migrate" {
		if err := db.NewMigrator(pool).Migrate(ctx); err != nil {
			fatal(err)
		}
		fmt.Println("database migration applied")
	}
	if command == "replay" {
		jobID, _ := replayJobID(os.Args)
		if err := replayDeadJob(ctx, pool, jobID); err != nil {
			fatal(err)
		}
		fmt.Printf("DEAD sync job %d reset to PENDING\n", jobID)
		return
	}

	if err := verifySchema(ctx, pool); err != nil {
		fatal(err)
	}
	fmt.Println("database schema verified")
}

func replayJobID(args []string) (int64, error) {
	if len(args) != 3 {
		return 0, errors.New("usage: order-sync-admin replay <job-id>")
	}
	jobID, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil || jobID <= 0 {
		return 0, errors.New("job-id must be a positive integer")
	}
	return jobID, nil
}

func fatalUsage(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}

func replayDeadJob(ctx context.Context, pool adminDB, jobID int64) error {
	result, err := pool.Exec(ctx, `
		UPDATE sync_jobs
		SET status = 'PENDING',
			due_at = NOW(),
			attempts = 0,
			locked_at = NULL,
			waiting_since = NULL,
			last_error = NULL,
			sap_internal_id = NULL,
			synced_at = NULL,
			updated_at = NOW()
		WHERE id = $1 AND status = 'DEAD'
	`, jobID)
	if err != nil {
		return fmt.Errorf("replay DEAD sync job %d: %w", jobID, err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("sync job %d does not exist or is not DEAD", jobID)
	}
	return nil
}

func verifySchema(ctx context.Context, pool *pgxpool.Pool) error {
	for _, table := range requiredTables {
		var found *string
		if err := pool.QueryRow(ctx, "SELECT to_regclass($1)", "public."+table).Scan(&found); err != nil {
			return errors.New("check table " + table + ": " + err.Error())
		}
		if found == nil {
			return errors.New("required table " + table + " is missing")
		}
	}
	var seededSKUs int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM sku_classifications WHERE sku = ANY($1)", []string{"NUKI-SL3", "NUKI-BRIDGE", "NUKI-SMART-HOSTING"}).Scan(&seededSKUs); err != nil {
		return errors.New("check seeded SKU classifications: " + err.Error())
	}
	if seededSKUs != 3 {
		return errors.New("expected 3 seeded SKU classifications, found " + strconv.Itoa(seededSKUs))
	}
	return nil
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
