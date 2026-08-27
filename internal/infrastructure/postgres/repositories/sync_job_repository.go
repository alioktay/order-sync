package repositories

import (
	"context"
	"errors"
	"fmt"
	"order-sync/internal/contracts"
	"order-sync/internal/infrastructure/postgres/queries"
	"order-sync/internal/sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type syncJobPool interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type syncJobListenerPool interface {
	Acquire(context.Context) (*pgxpool.Conn, error)
}

type SyncJobRepository struct {
	pool         syncJobPool
	listenerPool syncJobListenerPool
}

func NewSyncJobRepository(pool *pgxpool.Pool) sync.JobRepository {
	return &SyncJobRepository{pool: pool, listenerPool: pool}
}

var _ sync.JobRepository = (*SyncJobRepository)(nil)

func (r *SyncJobRepository) ClaimDue(ctx context.Context) (*sync.Job, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin SAP job claim transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var job sync.Job
	var paidAt time.Time
	var dueAt time.Time
	err = tx.QueryRow(ctx, queries.ClaimJob).Scan(&job.ID, &job.Status, &job.Attempts, &job.OrderID, &job.CustomerEmail, &paidAt, &dueAt, &job.WaitingSince)
	if errors.Is(err, pgx.ErrNoRows) {
		if err = tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit empty SAP job claim: %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim due SAP job: %w", err)
	}
	if _, err = tx.Exec(ctx, queries.LockJob, job.ID); err != nil {
		return nil, fmt.Errorf("lock SAP job %d: %w", job.ID, err)
	}
	job.PaidAt = paidAt.Format(time.RFC3339Nano)
	job.DueAt = &dueAt
	job.Items, err = loadJobItems(ctx, tx, job.ID)
	if err != nil {
		return nil, fmt.Errorf("load items for SAP job %d: %w", job.ID, err)
	}
	job.Attempts++
	if err = tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit SAP job %d claim: %w", job.ID, err)
	}
	return &job, nil
}

func (r *SyncJobRepository) NextWake(ctx context.Context) (*time.Time, error) {
	var nextWake *time.Time
	if err := r.pool.QueryRow(ctx, queries.NextWake).Scan(&nextWake); err != nil {
		return nil, fmt.Errorf("find next SAP job wake-up: %w", err)
	}
	return nextWake, nil
}

func (r *SyncJobRepository) Watch(ctx context.Context) (<-chan struct{}, <-chan error, error) {
	if r.listenerPool == nil {
		return nil, nil, errors.New("sync job listener is not configured")
	}
	connectCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := r.listenerPool.Acquire(connectCtx)
	if err != nil {
		return nil, nil, fmt.Errorf("acquire sync job listener connection: %w", err)
	}
	if _, err = conn.Exec(connectCtx, `LISTEN sync_jobs`); err != nil {
		conn.Release()
		return nil, nil, fmt.Errorf("listen for sync job changes: %w", err)
	}

	wakeups := make(chan struct{}, 1)
	listenerErrors := make(chan error, 1)
	go func() {
		defer conn.Release()
		defer close(wakeups)
		defer close(listenerErrors)
		for {
			_, waitErr := conn.Conn().WaitForNotification(ctx)
			if waitErr != nil {
				if ctx.Err() == nil {
					listenerErrors <- waitErr
				}
				return
			}
			select {
			case wakeups <- struct{}{}:
			default:
			}
		}
	}()
	return wakeups, listenerErrors, nil
}

func loadJobItems(ctx context.Context, tx DBTX, jobID int64) ([]contracts.OrderItem, error) {
	rows, err := tx.Query(ctx, queries.JobItems, jobID)
	if err != nil {
		return nil, fmt.Errorf("query items for SAP job %d: %w", jobID, err)
	}
	defer rows.Close()
	items := make([]contracts.OrderItem, 0)
	for rows.Next() {
		var item syncItem
		if err = rows.Scan(&item.SKU, &item.Quantity, &item.Price, &item.IsHardware); err != nil {
			return nil, fmt.Errorf("scan item for SAP job %d: %w", jobID, err)
		}
		items = append(items, item.OrderItem())
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate items for SAP job %d: %w", jobID, err)
	}
	return items, nil
}

type syncItem struct {
	SKU        string
	Quantity   int
	Price      float64
	IsHardware *bool
}

func (i syncItem) OrderItem() (item contracts.OrderItem) {
	item.SKU, item.Quantity, item.Price, item.IsHardware = i.SKU, i.Quantity, i.Price, i.IsHardware
	return
}
func (r *SyncJobRepository) MarkSynced(ctx context.Context, id int64, sapID string) error {
	_, err := r.pool.Exec(ctx, queries.MarkSynced, id, sapID)
	if err != nil {
		return fmt.Errorf("mark SAP job %d synced: %w", id, err)
	}
	return nil
}
func (r *SyncJobRepository) MarkFailed(ctx context.Context, id int64, status contracts.SyncStatus, dueAt time.Time, message string) error {
	_, err := r.pool.Exec(ctx, queries.MarkFailed, id, status, dueAt, message)
	if err != nil {
		return fmt.Errorf("mark SAP job %d %s: %w", id, status, err)
	}
	return nil
}
