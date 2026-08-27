package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestReplayJobIDValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing", args: []string{"order-sync-admin", "replay"}, want: "usage: order-sync-admin replay <job-id>"},
		{name: "invalid", args: []string{"order-sync-admin", "replay", "not-a-number"}, want: "job-id must be a positive integer"},
		{name: "zero", args: []string{"order-sync-admin", "replay", "0"}, want: "job-id must be a positive integer"},
		{name: "negative", args: []string{"order-sync-admin", "replay", "-1"}, want: "job-id must be a positive integer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := replayJobID(tt.args); err == nil || err.Error() != tt.want {
				t.Fatalf("replayJobID() error = %v, want %q", err, tt.want)
			}
		})
	}

	if got, err := replayJobID([]string{"order-sync-admin", "replay", "42"}); err != nil || got != 42 {
		t.Fatalf("replayJobID() = %d, %v; want 42, nil", got, err)
	}
}

type replayDB struct {
	rowsAffected int64
	query        string
	args         []any
}

func (db *replayDB) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	db.query, db.args = query, args
	return pgconn.NewCommandTag("UPDATE " + strconv.FormatInt(db.rowsAffected, 10)), nil
}

func TestReplayDeadJobRejectsMissingOrNonDeadJob(t *testing.T) {
	for _, name := range []string{"missing", "non-dead"} {
		t.Run(name, func(t *testing.T) {
			db := &replayDB{}
			err := replayDeadJob(context.Background(), db, 17)
			if err == nil || !strings.Contains(err.Error(), "does not exist or is not DEAD") {
				t.Fatalf("replayDeadJob() error = %v, want missing/non-DEAD error", err)
			}
		})
	}
}

func TestReplayDeadJobResetsOnlyDeadJobs(t *testing.T) {
	db := &replayDB{rowsAffected: 1}
	if err := replayDeadJob(context.Background(), db, 23); err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"WHERE id = $1 AND status = 'DEAD'",
		"status = 'PENDING'",
		"due_at = NOW()",
		"attempts = 0",
		"locked_at = NULL",
		"waiting_since = NULL",
		"last_error = NULL",
		"sap_internal_id = NULL",
		"synced_at = NULL",
	} {
		if !strings.Contains(db.query, fragment) {
			t.Errorf("replay query missing %q: %s", fragment, db.query)
		}
	}
	if len(db.args) != 1 || db.args[0] != int64(23) {
		t.Fatalf("replay query args = %#v, want [23]", db.args)
	}
}

func TestReplayDeadJobReturnsDatabaseError(t *testing.T) {
	db := replayDBWithError{}
	if err := replayDeadJob(context.Background(), db, 9); err == nil || !strings.Contains(err.Error(), "replay DEAD sync job 9") {
		t.Fatalf("replayDeadJob() error = %v, want wrapped database error", err)
	}
}

type replayDBWithError struct{}

func (replayDBWithError) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("database unavailable")
}
