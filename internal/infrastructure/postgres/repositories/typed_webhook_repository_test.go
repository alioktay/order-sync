package repositories

import (
	"context"
	"errors"
	"testing"
	"time"

	"order-sync/internal/contracts"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPaymentRepositoryUpsertAndFind(t *testing.T) {
	orderID := int64(42)
	paidAt := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	createdAt := paidAt.Add(-time.Hour)
	updatedAt := paidAt.Add(time.Minute)
	row := fakeRow{scan: func(dest ...any) error {
		*dest[0].(*int64) = 7
		*dest[1].(*string) = "order-1"
		*dest[2].(**int64) = &orderID
		*dest[3].(*contracts.PaymentStatus) = contracts.PaymentStatusCompleted
		*dest[4].(**time.Time) = &paidAt
		*dest[5].(*time.Time) = createdAt
		*dest[6].(*time.Time) = updatedAt
		return nil
	}}
	database := fakeDB{queryRowFn: func(context.Context, string, ...any) pgx.Row { return row }}
	repository := NewPaymentRepository(database)
	state, err := repository.Upsert(context.Background(), "order-1", "COMPLETED", &orderID, &paidAt)
	if err != nil || state.ID != 7 || state.OrderID == nil || *state.OrderID != orderID || state.PaidAt == nil || !state.PaidAt.Equal(paidAt) {
		t.Fatalf("Upsert() = %+v, %v", state, err)
	}
	found, err := repository.Find(context.Background(), "order-1")
	if err != nil || found == nil || found.ID != state.ID {
		t.Fatalf("Find() = %+v, %v", found, err)
	}

	repository = NewPaymentRepository(fakeDB{queryRowFn: func(context.Context, string, ...any) pgx.Row { return fakeRow{err: pgx.ErrNoRows} }})
	found, err = repository.Find(context.Background(), "missing")
	if err != nil || found != nil {
		t.Fatalf("missing Find() = %+v, %v", found, err)
	}
}

func TestPaymentRepositoryPropagatesWrites(t *testing.T) {
	expected := errors.New("payment write failed")
	repository := NewPaymentRepository(fakeDB{queryRowFn: func(context.Context, string, ...any) pgx.Row { return fakeRow{err: expected} }})
	if _, err := repository.Upsert(context.Background(), "order-1", "FAILED", nil, nil); !errors.Is(err, expected) {
		t.Fatalf("Upsert() error = %v", err)
	}
	repository = NewPaymentRepository(fakeDB{execFn: func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, expected }})
	if err := repository.LinkOrder(context.Background(), "order-1", 7); !errors.Is(err, expected) {
		t.Fatalf("LinkOrder() error = %v", err)
	}
}
