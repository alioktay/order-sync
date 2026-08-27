package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"order-sync/internal/contracts"
	"order-sync/internal/infrastructure/postgres/queries"

	"github.com/jackc/pgx/v5"
)

var ErrPaymentFinalized = contracts.ErrPaymentFinalized

type PaymentRepository struct{ db DBTX }

func NewPaymentRepository(db DBTX) *PaymentRepository {
	return &PaymentRepository{db: db}
}

var _ contracts.PaymentRepository = (*PaymentRepository)(nil)

func (r *PaymentRepository) Upsert(ctx context.Context, referenceOrderID string, status contracts.PaymentStatus, orderID *int64, paidAt *time.Time) (contracts.PaymentState, error) {
	state, err := r.scanState(r.db.QueryRow(ctx, queries.UpsertPayment, referenceOrderID, nullableInt64(orderID), status, nullableTime(paidAt)))
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.PaymentState{}, ErrPaymentFinalized
	}
	if err != nil {
		return contracts.PaymentState{}, fmt.Errorf("upsert payment %q: %w", referenceOrderID, err)
	}
	return state, nil
}

func (r *PaymentRepository) Cancel(ctx context.Context, referenceOrderID string, orderID *int64) (contracts.PaymentState, error) {
	state, err := r.scanState(r.db.QueryRow(ctx, queries.CancelPayment, referenceOrderID, nullableInt64(orderID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return contracts.PaymentState{}, ErrPaymentFinalized
	}
	if err != nil {
		return contracts.PaymentState{}, fmt.Errorf("cancel payment %q: %w", referenceOrderID, err)
	}
	return state, nil
}

func (r *PaymentRepository) Find(ctx context.Context, referenceOrderID string) (*contracts.PaymentState, error) {
	state, err := r.scanState(r.db.QueryRow(ctx, queries.FindPayment, referenceOrderID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find payment %q: %w", referenceOrderID, err)
	}
	return &state, nil
}

func (r *PaymentRepository) LinkOrder(ctx context.Context, referenceOrderID string, orderID int64) error {
	_, err := r.db.Exec(ctx, queries.LinkPaymentOrder, referenceOrderID, orderID)
	if err != nil {
		return fmt.Errorf("link payment %q to order %d: %w", referenceOrderID, orderID, err)
	}
	return nil
}

func (r *PaymentRepository) scanState(row pgx.Row) (contracts.PaymentState, error) {
	var state contracts.PaymentState
	err := row.Scan(&state.ID, &state.ReferenceOrderID, &state.OrderID, &state.Status, &state.PaidAt, &state.CreatedAt, &state.UpdatedAt)
	return state, err
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}
