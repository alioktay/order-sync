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

type fakeDB struct {
	execFn     func(context.Context, string, ...any) (pgconn.CommandTag, error)
	queryFn    func(context.Context, string, ...any) (pgx.Rows, error)
	queryRowFn func(context.Context, string, ...any) pgx.Row
}

func (f fakeDB) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if f.execFn == nil {
		return pgconn.CommandTag{}, nil
	}
	return f.execFn(ctx, query, args...)
}

func (f fakeDB) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	if f.queryFn == nil {
		return nil, errors.New("unexpected query")
	}
	return f.queryFn(ctx, query, args...)
}

func (f fakeDB) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	if f.queryRowFn == nil {
		return fakeRow{err: errors.New("unexpected query row")}
	}
	return f.queryRowFn(ctx, query, args...)
}

type fakeRow struct {
	scan func(...any) error
	err  error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.scan != nil {
		return r.scan(dest...)
	}
	return r.err
}

type fakeRows struct {
	rows    [][]any
	index   int
	err     error
	scanErr error
	closed  bool
}

type fakeTransaction struct {
	pgx.Tx
	row         pgx.Row
	rows        pgx.Rows
	execErr     error
	queryErr    error
	commitErr   error
	committed   bool
	rolledBack  bool
	lastQueryID int64
}

func (t *fakeTransaction) Commit(context.Context) error {
	t.committed = true
	return t.commitErr
}
func (t *fakeTransaction) Rollback(context.Context) error {
	t.rolledBack = true
	return nil
}
func (t *fakeTransaction) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, t.execErr
}
func (t *fakeTransaction) Query(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
	if len(args) > 0 {
		t.lastQueryID = args[0].(int64)
	}
	return t.rows, t.queryErr
}
func (t *fakeTransaction) QueryRow(context.Context, string, ...any) pgx.Row { return t.row }

type fakeSyncPool struct {
	tx         pgx.Tx
	beginErr   error
	execErr    error
	execArgs   []any
	queryRowFn func(context.Context, string, ...any) pgx.Row
}

func (p *fakeSyncPool) Begin(context.Context) (pgx.Tx, error) { return p.tx, p.beginErr }
func (p *fakeSyncPool) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	p.execArgs = args
	return pgconn.CommandTag{}, p.execErr
}
func (p *fakeSyncPool) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	if p.queryRowFn == nil {
		return fakeRow{err: errors.New("unexpected query row")}
	}
	return p.queryRowFn(ctx, query, args...)
}

func (r *fakeRows) Close()                                       { r.closed = true }
func (r *fakeRows) Err() error                                   { return r.err }
func (r *fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *fakeRows) RawValues() [][]byte                          { return nil }
func (r *fakeRows) Conn() *pgx.Conn                              { return nil }
func (r *fakeRows) Next() bool {
	if r.index >= len(r.rows) {
		r.closed = true
		return false
	}
	r.index++
	return true
}
func (r *fakeRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	if r.index == 0 || r.index > len(r.rows) {
		return errors.New("scan called without Next")
	}
	values := r.rows[r.index-1]
	if len(values) != len(dest) {
		return errors.New("destination count mismatch")
	}
	for i := range values {
		if err := assignValue(dest[i], values[i]); err != nil {
			return err
		}
	}
	return nil
}
func (r *fakeRows) Values() ([]any, error) {
	if r.index == 0 || r.index > len(r.rows) {
		return nil, errors.New("values called without Next")
	}
	return r.rows[r.index-1], nil
}

func assignValue(destination, value any) error {
	switch target := destination.(type) {
	case *string:
		*target = value.(string)
	case *int:
		*target = value.(int)
	case *int64:
		*target = value.(int64)
	case *float64:
		*target = value.(float64)
	case *[]byte:
		*target = value.([]byte)
	case **bool:
		if value == nil {
			*target = nil
			return nil
		}
		switch booleanValue := value.(type) {
		case bool:
			*target = &booleanValue
		case *bool:
			*target = booleanValue
		default:
			return errors.New("expected bool value")
		}
	default:
		return errors.New("unsupported destination")
	}
	return nil
}

func TestWebhookEventRepositoryRecord(t *testing.T) {
	t.Run("records new event", func(t *testing.T) {
		repository := NewWebhookEventRepository(fakeDB{queryRowFn: func(context.Context, string, ...any) pgx.Row {
			return fakeRow{scan: func(dest ...any) error {
				*dest[0].(*string) = "evt-1"
				return nil
			}}
		}})
		created, err := repository.Record(context.Background(), "evt-1", "shop", map[string]string{"order": "one"})
		if err != nil || !created {
			t.Fatalf("Record() = %v, %v; want true, nil", created, err)
		}
	})

	t.Run("recognizes duplicate", func(t *testing.T) {
		repository := NewWebhookEventRepository(fakeDB{queryRowFn: func(context.Context, string, ...any) pgx.Row {
			return fakeRow{err: pgx.ErrNoRows}
		}})
		created, err := repository.Record(context.Background(), "evt-1", "shop", struct{}{})
		if err != nil || created {
			t.Fatalf("Record() = %v, %v; want false, nil", created, err)
		}
	})

	t.Run("propagates serialization failure", func(t *testing.T) {
		repository := NewWebhookEventRepository(fakeDB{})
		if created, err := repository.Record(context.Background(), "evt-1", "shop", make(chan int)); err == nil || created {
			t.Fatalf("Record() = %v, %v; want false, error", created, err)
		}
	})

	t.Run("propagates database failure", func(t *testing.T) {
		databaseErr := errors.New("insert failed")
		repository := NewWebhookEventRepository(fakeDB{queryRowFn: func(context.Context, string, ...any) pgx.Row {
			return fakeRow{err: databaseErr}
		}})
		created, err := repository.Record(context.Background(), "evt-1", "shop", struct{}{})
		if !errors.Is(err, databaseErr) || created {
			t.Fatalf("Record() = %v, %v; want false, database error", created, err)
		}
	})
}

func TestWebhookEventRepositoryFindPayload(t *testing.T) {
	t.Run("returns stored payload", func(t *testing.T) {
		repository := NewWebhookEventRepository(fakeDB{queryRowFn: func(context.Context, string, ...any) pgx.Row {
			return fakeRow{scan: func(dest ...any) error {
				*dest[0].(*[]byte) = []byte(`{"event_id":"evt-1"}`)
				return nil
			}}
		}})
		payload, err := repository.FindPayload(context.Background(), "shop", "evt-1")
		if err != nil || string(payload) != `{"event_id":"evt-1"}` {
			t.Fatalf("FindPayload() = %q, %v", payload, err)
		}
	})

	t.Run("returns nil for a missing event", func(t *testing.T) {
		repository := NewWebhookEventRepository(fakeDB{queryRowFn: func(context.Context, string, ...any) pgx.Row {
			return fakeRow{err: pgx.ErrNoRows}
		}})
		payload, err := repository.FindPayload(context.Background(), "shop", "evt-1")
		if err != nil || payload != nil {
			t.Fatalf("FindPayload() = %q, %v", payload, err)
		}
	})
}

func TestWebhookEventRepositoryMarksEvents(t *testing.T) {
	expected := errors.New("update failed")
	calls := 0
	repository := NewWebhookEventRepository(fakeDB{execFn: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
		calls++
		return pgconn.CommandTag{}, expected
	}})
	if err := repository.MarkProcessed(context.Background(), contracts.EventTypeShop, "evt-1"); !errors.Is(err, expected) {
		t.Fatalf("MarkProcessed() error = %v", err)
	}
	if err := repository.MarkPaymentEventsProcessed(context.Background(), "order-1"); !errors.Is(err, expected) {
		t.Fatalf("MarkPaymentEventsProcessed() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("Exec calls = %d, want 2", calls)
	}
}

func TestOrderRepositoryReads(t *testing.T) {
	t.Run("creates and finds IDs", func(t *testing.T) {
		repository := NewOrderRepository(fakeDB{queryRowFn: func(context.Context, string, ...any) pgx.Row {
			return fakeRow{scan: func(dest ...any) error { *dest[0].(*int64) = 42; return nil }}
		}})
		id, err := repository.Create(context.Background(), "order-1", "buyer@example.com")
		if err != nil || id != 42 {
			t.Fatalf("Create() = %d, %v", id, err)
		}
		id, err = repository.FindID(context.Background(), "order-1")
		if err != nil || id != 42 {
			t.Fatalf("FindID() = %d, %v", id, err)
		}
	})

	t.Run("returns zero for missing IDs", func(t *testing.T) {
		repository := NewOrderRepository(fakeDB{queryRowFn: func(context.Context, string, ...any) pgx.Row { return fakeRow{err: pgx.ErrNoRows} }})
		if id, err := repository.Create(context.Background(), "", ""); err != nil || id != 0 {
			t.Fatalf("Create() = %d, %v", id, err)
		}
		if id, err := repository.FindID(context.Background(), "missing"); err != nil || id != 0 {
			t.Fatalf("FindID() = %d, %v", id, err)
		}
	})

	t.Run("reads status", func(t *testing.T) {
		paidAt := time.Date(2026, 8, 26, 10, 0, 0, 123, time.UTC)
		dueAt := paidAt.Add(time.Minute)
		attempts, lastError, sapID := 2, "temporary", "sap-7"
		repository := NewOrderRepository(fakeDB{queryRowFn: func(context.Context, string, ...any) pgx.Row {
			return fakeRow{scan: func(dest ...any) error {
				*dest[0].(*string) = "order-1"
				*dest[1].(*string) = "buyer@example.com"
				*dest[2].(*contracts.OrderState) = contracts.OrderStatePaid
				*dest[3].(*contracts.PaymentStatus) = contracts.PaymentStatusCompleted
				*dest[4].(**time.Time) = &paidAt
				*dest[5].(*contracts.SyncStatus) = contracts.SyncStatusPending
				*dest[6].(**time.Time) = &dueAt
				*dest[7].(**int) = &attempts
				*dest[8].(**string) = &lastError
				*dest[9].(**string) = &sapID
				return nil
			}}
		}})
		status, err := repository.FindStatus(context.Background(), "order-1")
		if err != nil || status == nil || status.PaidAt == nil || *status.PaidAt != paidAt.Format(time.RFC3339Nano) || status.DueAt == nil || *status.DueAt != dueAt.Format(time.RFC3339Nano) {
			t.Fatalf("FindStatus() = %+v, %v", status, err)
		}
	})

	t.Run("returns nil for missing status", func(t *testing.T) {
		repository := NewOrderRepository(fakeDB{queryRowFn: func(context.Context, string, ...any) pgx.Row { return fakeRow{err: pgx.ErrNoRows} }})
		status, err := repository.FindStatus(context.Background(), "missing")
		if err != nil || status != nil {
			t.Fatalf("FindStatus() = %+v, %v", status, err)
		}
	})
}

func TestOrderRepositoryListsItems(t *testing.T) {
	rows := &fakeRows{rows: [][]any{{"sku-1", 2, 12.5, nil}, {"sku-2", 1, 3.0, false}}}
	repository := NewOrderRepository(fakeDB{queryFn: func(context.Context, string, ...any) (pgx.Rows, error) { return rows, nil }})
	items, err := repository.ListItems(context.Background(), 42)
	if err != nil || len(items) != 2 || items[0].SKU != "sku-1" || items[0].IsHardware != nil || items[1].IsHardware == nil || *items[1].IsHardware || !rows.closed {
		t.Fatalf("ListItems() = %+v, %v (closed=%v)", items, err, rows.closed)
	}

	expected := errors.New("query failed")
	repository = NewOrderRepository(fakeDB{queryFn: func(context.Context, string, ...any) (pgx.Rows, error) { return nil, expected }})
	if items, err = repository.ListItems(context.Background(), 42); !errors.Is(err, expected) || items != nil {
		t.Fatalf("ListItems() = %+v, %v; want query error", items, err)
	}

	expected = errors.New("scan failed")
	repository = NewOrderRepository(fakeDB{queryFn: func(context.Context, string, ...any) (pgx.Rows, error) {
		return &fakeRows{rows: [][]any{{"sku", 1, 1.0, nil}}, scanErr: expected}, nil
	}})
	if _, err = repository.ListItems(context.Background(), 42); !errors.Is(err, expected) {
		t.Fatalf("ListItems() scan error = %v", err)
	}
}

func TestOrderRepositoryWrites(t *testing.T) {
	expected := errors.New("write failed")
	calls := 0
	repository := NewOrderRepository(fakeDB{execFn: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
		calls++
		if calls == 2 {
			return pgconn.CommandTag{}, expected
		}
		return pgconn.CommandTag{}, nil
	}})
	items := []contracts.OrderItem{{SKU: "one", Quantity: 1}, {SKU: "two", Quantity: 2}, {SKU: "three", Quantity: 3}}
	if err := repository.AddItems(context.Background(), 42, items); !errors.Is(err, expected) {
		t.Fatalf("AddItems() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("AddItems() calls = %d, want 2", calls)
	}

	repository = NewOrderRepository(fakeDB{})
	if err := repository.MarkPaid(context.Background(), 42, "2026-08-26T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := repository.ScheduleSync(context.Background(), 42, 30); err != nil {
		t.Fatal(err)
	}

	override := false
	var insertArgs []any
	repository = NewOrderRepository(fakeDB{execFn: func(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
		insertArgs = args
		return pgconn.CommandTag{}, nil
	}})
	if err := repository.AddItems(context.Background(), 42, []contracts.OrderItem{{SKU: "forced-digital", Quantity: 1, IsHardware: &override}}); err != nil {
		t.Fatal(err)
	}
	if len(insertArgs) != 5 || insertArgs[4] != &override {
		t.Fatalf("AddItems() args = %#v, want nullable override as fifth argument", insertArgs)
	}
}

func TestLoadJobItems(t *testing.T) {
	rows := &fakeRows{rows: [][]any{{"hardware", 2, 49.5, true}}}
	database := fakeDB{queryFn: func(context.Context, string, ...any) (pgx.Rows, error) { return rows, nil }}
	items, err := loadJobItems(context.Background(), database, 7)
	if err != nil || len(items) != 1 || items[0].SKU != "hardware" || items[0].Quantity != 2 || items[0].IsHardware == nil || !*items[0].IsHardware || !rows.closed {
		t.Fatalf("loadJobItems() = %+v, %v (closed=%v)", items, err, rows.closed)
	}

	expected := errors.New("rows failed")
	database = fakeDB{queryFn: func(context.Context, string, ...any) (pgx.Rows, error) { return &fakeRows{err: expected}, nil }}
	if _, err = loadJobItems(context.Background(), database, 7); !errors.Is(err, expected) {
		t.Fatalf("loadJobItems() rows error = %v", err)
	}
}

func TestSyncJobRepositoryClaimDue(t *testing.T) {
	ctx := context.Background()

	t.Run("begin failure", func(t *testing.T) {
		expected := errors.New("begin failed")
		repository := &SyncJobRepository{pool: &fakeSyncPool{beginErr: expected}}
		if job, err := repository.ClaimDue(ctx); !errors.Is(err, expected) || job != nil {
			t.Fatalf("ClaimDue() = %+v, %v", job, err)
		}
	})

	t.Run("no due job", func(t *testing.T) {
		tx := &fakeTransaction{row: fakeRow{err: pgx.ErrNoRows}}
		repository := &SyncJobRepository{pool: &fakeSyncPool{tx: tx}}
		job, err := repository.ClaimDue(ctx)
		if err != nil || job != nil || !tx.committed || !tx.rolledBack {
			t.Fatalf("ClaimDue() = %+v, %v (commit=%v rollback=%v)", job, err, tx.committed, tx.rolledBack)
		}
	})

	t.Run("claim query failure", func(t *testing.T) {
		expected := errors.New("claim failed")
		tx := &fakeTransaction{row: fakeRow{err: expected}}
		repository := &SyncJobRepository{pool: &fakeSyncPool{tx: tx}}
		if job, err := repository.ClaimDue(ctx); !errors.Is(err, expected) || job != nil || !tx.rolledBack {
			t.Fatalf("ClaimDue() = %+v, %v", job, err)
		}
	})

	t.Run("lock failure", func(t *testing.T) {
		expected := errors.New("lock failed")
		tx := &fakeTransaction{
			row:     fakeRow{scan: scanClaimedJob(7, 1, "order-7", "buyer@example.com", time.Now(), time.Now())},
			execErr: expected,
		}
		repository := &SyncJobRepository{pool: &fakeSyncPool{tx: tx}}
		if _, err := repository.ClaimDue(ctx); !errors.Is(err, expected) {
			t.Fatalf("ClaimDue() error = %v", err)
		}
	})

	t.Run("loads and commits job", func(t *testing.T) {
		paidAt := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
		rows := &fakeRows{rows: [][]any{{"hardware", 2, 99.5, true}}}
		tx := &fakeTransaction{
			row:  fakeRow{scan: scanClaimedJob(7, 1, "order-7", "buyer@example.com", paidAt, paidAt)},
			rows: rows,
		}
		repository := &SyncJobRepository{pool: &fakeSyncPool{tx: tx}}
		job, err := repository.ClaimDue(ctx)
		if err != nil || job == nil || job.ID != 7 || job.Status != "PENDING" || job.Attempts != 2 || job.PaidAt != paidAt.Format(time.RFC3339Nano) || job.DueAt == nil || !job.DueAt.Equal(paidAt) || len(job.Items) != 1 || !tx.committed || tx.lastQueryID != 7 {
			t.Fatalf("ClaimDue() = %+v, %v (commit=%v queryID=%d)", job, err, tx.committed, tx.lastQueryID)
		}
	})

	t.Run("commit failure", func(t *testing.T) {
		expected := errors.New("commit failed")
		tx := &fakeTransaction{row: fakeRow{err: pgx.ErrNoRows}, commitErr: expected}
		repository := &SyncJobRepository{pool: &fakeSyncPool{tx: tx}}
		if _, err := repository.ClaimDue(ctx); !errors.Is(err, expected) {
			t.Fatalf("ClaimDue() error = %v", err)
		}
	})
}

func scanClaimedJob(id int64, attempts int, orderID, email string, paidAt, dueAt time.Time) func(...any) error {
	return func(dest ...any) error {
		*dest[0].(*int64) = id
		*dest[1].(*contracts.SyncStatus) = contracts.SyncStatusPending
		*dest[2].(*int) = attempts
		*dest[3].(*string) = orderID
		*dest[4].(*string) = email
		*dest[5].(*time.Time) = paidAt
		*dest[6].(*time.Time) = dueAt
		*dest[7].(**time.Time) = nil
		return nil
	}
}

func TestSyncJobRepositoryUpdates(t *testing.T) {
	expected := errors.New("update failed")
	pool := &fakeSyncPool{execErr: expected}
	repository := &SyncJobRepository{pool: pool}
	if err := repository.MarkSynced(context.Background(), 7, "SAP-7"); !errors.Is(err, expected) {
		t.Fatalf("MarkSynced() error = %v", err)
	}
	if len(pool.execArgs) != 2 {
		t.Fatalf("MarkSynced() args = %#v", pool.execArgs)
	}
	if err := repository.MarkFailed(context.Background(), 7, "DEAD", time.Now(), "failed"); !errors.Is(err, expected) {
		t.Fatalf("MarkFailed() error = %v", err)
	}
	if len(pool.execArgs) != 4 {
		t.Fatalf("MarkFailed() args = %#v", pool.execArgs)
	}

	if _, ok := NewSyncJobRepository(nil).(*SyncJobRepository); !ok {
		t.Fatal("NewSyncJobRepository() returned the wrong implementation")
	}
	if NewPoolOrderRepository(nil) == nil {
		t.Fatal("NewPoolOrderRepository() returned nil")
	}
	if formatTime(nil) != nil {
		t.Fatal("formatTime(nil) must return nil")
	}
}

func TestSyncJobRepositoryNextWake(t *testing.T) {
	want := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	repository := &SyncJobRepository{pool: &fakeSyncPool{queryRowFn: func(context.Context, string, ...any) pgx.Row {
		return fakeRow{scan: func(dest ...any) error {
			*dest[0].(**time.Time) = &want
			return nil
		}}
	}}}
	got, err := repository.NextWake(context.Background())
	if err != nil || got == nil || !got.Equal(want) {
		t.Fatalf("NextWake() = %v, %v; want %v", got, err, want)
	}
}
