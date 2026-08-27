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

func TestOrderRepositoryCancellationBranches(t *testing.T) {
	ctx := context.Background()
	newRepository := func(row pgx.Row, execErrs ...error) *OrderRepository {
		calls := 0
		return NewOrderRepository(fakeDB{
			queryRowFn: func(context.Context, string, ...any) pgx.Row { return row },
			execFn: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
				var err error
				if calls < len(execErrs) {
					err = execErrs[calls]
				}
				calls++
				return pgconn.CommandTag{}, err
			},
		})
	}

	t.Run("successful cancellation", func(t *testing.T) {
		repository := newRepository(fakeRow{scan: func(dest ...any) error { *dest[0].(*int64) = 1; return nil }})
		if err := repository.Cancel(ctx, 7); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("cannot cancel missing order", func(t *testing.T) {
		repository := newRepository(fakeRow{err: pgx.ErrNoRows})
		if err := repository.Cancel(ctx, 7); !errors.Is(err, contracts.ErrOrderCannotCancel) {
			t.Fatalf("Cancel() = %v", err)
		}
	})
	t.Run("propagates query and update failures", func(t *testing.T) {
		queryErr := errors.New("cancel query failed")
		if err := newRepository(fakeRow{err: queryErr}).Cancel(ctx, 7); !errors.Is(err, queryErr) {
			t.Fatalf("query error = %v", err)
		}
		updateErr := errors.New("mark cancelled failed")
		if err := newRepository(fakeRow{scan: func(dest ...any) error { *dest[0].(*int64) = 1; return nil }}, updateErr).Cancel(ctx, 7); !errors.Is(err, updateErr) {
			t.Fatalf("order update error = %v", err)
		}
		jobErr := errors.New("cancel job failed")
		if err := newRepository(fakeRow{scan: func(dest ...any) error { *dest[0].(*int64) = 1; return nil }}, nil, jobErr).Cancel(ctx, 7); !errors.Is(err, jobErr) {
			t.Fatalf("job update error = %v", err)
		}
	})
}

func TestPaymentRepositoryCancelAndErrors(t *testing.T) {
	ctx := context.Background()
	row := fakeRow{scan: func(dest ...any) error {
		*dest[0].(*int64) = 3
		*dest[1].(*string) = "order-3"
		*dest[3].(*contracts.PaymentStatus) = contracts.PaymentStatusCancelled
		return nil
	}}
	if state, err := NewPaymentRepository(fakeDB{queryRowFn: func(context.Context, string, ...any) pgx.Row { return row }}).Cancel(ctx, "order-3", nil); err != nil || state.ID != 3 {
		t.Fatalf("Cancel() = %+v, %v", state, err)
	}
	if _, err := NewPaymentRepository(fakeDB{queryRowFn: func(context.Context, string, ...any) pgx.Row { return fakeRow{err: pgx.ErrNoRows} }}).Cancel(ctx, "missing", nil); !errors.Is(err, contracts.ErrPaymentFinalized) {
		t.Fatalf("missing Cancel() error = %v", err)
	}
	wantErr := errors.New("cancel payment failed")
	if _, err := NewPaymentRepository(fakeDB{queryRowFn: func(context.Context, string, ...any) pgx.Row { return fakeRow{err: wantErr} }}).Cancel(ctx, "order-3", nil); !errors.Is(err, wantErr) {
		t.Fatalf("Cancel() error = %v", err)
	}
	if _, err := NewPaymentRepository(fakeDB{queryRowFn: func(context.Context, string, ...any) pgx.Row { return fakeRow{err: pgx.ErrNoRows} }}).Upsert(ctx, "order-3", contracts.PaymentStatusFailed, nil, nil); !errors.Is(err, contracts.ErrPaymentFinalized) {
		t.Fatalf("finalized Upsert() error = %v", err)
	}
	if _, err := NewPaymentRepository(fakeDB{queryRowFn: func(context.Context, string, ...any) pgx.Row { return fakeRow{err: wantErr} }}).Find(ctx, "order-3"); !errors.Is(err, wantErr) {
		t.Fatalf("Find() error = %v", err)
	}
	if err := NewPaymentRepository(fakeDB{execFn: func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil }}).LinkOrder(ctx, "order-3", 3); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryUpdateSuccessAndReadErrors(t *testing.T) {
	ctx := context.Background()
	if err := NewWebhookEventRepository(fakeDB{execFn: func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil }}).MarkProcessed(ctx, contracts.EventTypeShop, "evt"); err != nil {
		t.Fatal(err)
	}
	if err := NewWebhookEventRepository(fakeDB{execFn: func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil }}).MarkPaymentEventsProcessed(ctx, "order"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewOrderRepository(fakeDB{queryRowFn: func(context.Context, string, ...any) pgx.Row { return fakeRow{err: errors.New("scan create")} }}).Create(ctx, "order", "email"); err == nil {
		t.Fatal("Create() scan failure unexpectedly succeeded")
	}
	if _, err := NewOrderRepository(fakeDB{queryRowFn: func(context.Context, string, ...any) pgx.Row { return fakeRow{err: errors.New("scan id")} }}).FindID(ctx, "order"); err == nil {
		t.Fatal("FindID() scan failure unexpectedly succeeded")
	}
	if _, err := NewOrderRepository(fakeDB{queryRowFn: func(context.Context, string, ...any) pgx.Row { return fakeRow{err: errors.New("scan status")} }}).FindStatus(ctx, "order"); err == nil {
		t.Fatal("FindStatus() scan failure unexpectedly succeeded")
	}
	writeErr := errors.New("write failed")
	repository := NewOrderRepository(fakeDB{execFn: func(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, writeErr }})
	if err := repository.MarkPaid(ctx, 1, time.Now().Format(time.RFC3339)); !errors.Is(err, writeErr) {
		t.Fatalf("MarkPaid() = %v", err)
	}
	if err := repository.ScheduleSync(ctx, 1, 1); !errors.Is(err, writeErr) {
		t.Fatalf("ScheduleSync() = %v", err)
	}
}

func TestSyncJobRepositoryReadAndUpdateErrors(t *testing.T) {
	ctx := context.Background()
	wantErr := errors.New("wake failed")
	repository := &SyncJobRepository{pool: &fakeSyncPool{queryRowFn: func(context.Context, string, ...any) pgx.Row { return fakeRow{err: wantErr} }}}
	if _, err := repository.NextWake(ctx); !errors.Is(err, wantErr) {
		t.Fatalf("NextWake() = %v", err)
	}
	rowsErr := errors.New("rows iteration failed")
	rows := &fakeRows{err: rowsErr}
	if _, err := loadJobItems(ctx, fakeDB{queryFn: func(context.Context, string, ...any) (pgx.Rows, error) { return rows, nil }}, 1); !errors.Is(err, rowsErr) {
		t.Fatalf("loadJobItems() = %v", err)
	}
	scanErr := errors.New("item scan failed")
	if _, err := loadJobItems(ctx, fakeDB{queryFn: func(context.Context, string, ...any) (pgx.Rows, error) {
		return &fakeRows{rows: [][]any{{"sku", 1, 2.0, nil}}, scanErr: scanErr}, nil
	}}, 1); !errors.Is(err, scanErr) {
		t.Fatalf("loadJobItems() scan error = %v", err)
	}
	if err := (&SyncJobRepository{pool: &fakeSyncPool{execErr: nil}}).MarkSynced(ctx, 1, "sap"); err != nil {
		t.Fatal(err)
	}
	if err := (&SyncJobRepository{pool: &fakeSyncPool{execErr: nil}}).MarkFailed(ctx, 1, contracts.SyncStatusPending, time.Now(), "retry"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (&SyncJobRepository{}).Watch(ctx); err == nil {
		t.Fatal("Watch() without listener pool unexpectedly succeeded")
	}
}
