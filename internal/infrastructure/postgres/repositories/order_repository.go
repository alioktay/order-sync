package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"order-sync/internal/contracts"
	"order-sync/internal/infrastructure/postgres/queries"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type OrderRepository struct{ db DBTX }

func NewOrderRepository(db DBTX) *OrderRepository { return &OrderRepository{db: db} }

var _ contracts.OrderRepository = (*OrderRepository)(nil)

func (r *OrderRepository) Create(ctx context.Context, orderID, customerEmail string) (int64, error) {
	var id int64
	err := r.db.QueryRow(ctx, queries.InsertOrder, orderID, customerEmail).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("create order %q: %w", orderID, err)
	}
	return id, nil
}
func (r *OrderRepository) FindID(ctx context.Context, orderID string) (int64, error) {
	var id int64
	err := r.db.QueryRow(ctx, queries.FindOrderID, orderID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("find order %q: %w", orderID, err)
	}
	return id, nil
}
func (r *OrderRepository) Find(ctx context.Context, orderID string) (*contracts.StoredOrder, error) {
	var order contracts.StoredOrder
	err := r.db.QueryRow(ctx, queries.FindOrder, orderID).Scan(&order.ID, &order.CustomerEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find order %q: %w", orderID, err)
	}
	order.Items, err = r.ListItems(ctx, order.ID)
	if err != nil {
		return nil, err
	}
	return &order, nil
}
func (r *OrderRepository) FindStatus(ctx context.Context, orderID string) (*contracts.OrderStatus, error) {
	var status contracts.OrderStatus
	var paidAt, dueAt *time.Time
	var lastError, sapID *string
	err := r.db.QueryRow(ctx, queries.FindStatus, orderID).Scan(&status.OrderID, &status.CustomerEmail, &status.Status, &status.PaymentStatus, &paidAt, &status.SyncStatus, &dueAt, &status.Attempts, &lastError, &sapID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find status for order %q: %w", orderID, err)
	}
	status.PaidAt, status.DueAt = formatTime(paidAt), formatTime(dueAt)
	status.LastError, status.SAPInternalID = lastError, sapID
	return &status, nil
}

func formatTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.Format(time.RFC3339Nano)
	return &formatted
}
func (r *OrderRepository) ListItems(ctx context.Context, id int64) ([]contracts.OrderItem, error) {
	rows, err := r.db.Query(ctx, queries.FindItems, id)
	if err != nil {
		return nil, fmt.Errorf("list items for order %d: %w", id, err)
	}
	defer rows.Close()
	var result []contracts.OrderItem
	for rows.Next() {
		var item contracts.OrderItem
		if err = rows.Scan(&item.SKU, &item.Quantity, &item.Price, &item.IsHardware); err != nil {
			return nil, fmt.Errorf("scan item for order %d: %w", id, err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate items for order %d: %w", id, err)
	}
	return result, nil
}
func (r *OrderRepository) MarkPaid(ctx context.Context, id int64, paidAt string) error {
	_, err := r.db.Exec(ctx, queries.MarkPaid, id, paidAt)
	if err != nil {
		return fmt.Errorf("mark order %d paid: %w", id, err)
	}
	return nil
}
func (r *OrderRepository) AddItems(ctx context.Context, id int64, items []contracts.OrderItem) error {
	for _, item := range items {
		if _, err := r.db.Exec(ctx, queries.InsertItem, id, item.SKU, item.Quantity, item.Price, item.IsHardware); err != nil {
			return fmt.Errorf("insert item %q for order %d: %w", item.SKU, id, err)
		}
	}
	return nil
}
func (r *OrderRepository) ScheduleSync(ctx context.Context, id int64, delay int) error {
	_, err := r.db.Exec(ctx, queries.ScheduleSync, id, delay)
	if err != nil {
		return fmt.Errorf("schedule SAP sync for order %d: %w", id, err)
	}
	return nil
}
func (r *OrderRepository) Cancel(ctx context.Context, id int64) error {
	var cancelled int64
	if err := r.db.QueryRow(ctx, queries.CancelOrder, id).Scan(&cancelled); errors.Is(err, pgx.ErrNoRows) {
		return contracts.ErrOrderCannotCancel
	} else if err != nil {
		return fmt.Errorf("cancel order %d: %w", id, err)
	}
	if _, err := r.db.Exec(ctx, queries.MarkCancelledOrder, id); err != nil {
		return fmt.Errorf("cancel order %d: %w", id, err)
	}
	if _, err := r.db.Exec(ctx, queries.CancelJob, id); err != nil {
		return fmt.Errorf("cancel SAP job for order %d: %w", id, err)
	}
	return nil
}

func NewPoolOrderRepository(pool *pgxpool.Pool) contracts.OrderRepository {
	return NewOrderRepository(pool)
}
