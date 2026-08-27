package payment

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"order-sync/internal/config"
	"order-sync/internal/contracts"
)

type paymentTransaction struct {
	repos contracts.TransactionRepositories
}

func (t paymentTransaction) Run(_ context.Context, work func(contracts.TransactionRepositories) (contracts.WebhookResult, error)) (contracts.WebhookResult, error) {
	return work(t.repos)
}

type paymentEvents struct {
	marked      string
	record      bool
	recordErr   error
	markedErr   error
	markedCount int
	payload     []byte
	payloadErr  error
}

func (e *paymentEvents) Record(context.Context, string, contracts.EventType, any) (bool, error) {
	if e.recordErr != nil {
		return false, e.recordErr
	}
	if e.record {
		return false, nil
	}
	return true, nil
}
func (e *paymentEvents) FindPayload(context.Context, contracts.EventType, string) ([]byte, error) {
	return e.payload, e.payloadErr
}
func (e *paymentEvents) MarkProcessed(_ context.Context, _ contracts.EventType, id string) error {
	e.markedCount++
	if e.markedErr != nil {
		return e.markedErr
	}
	e.marked = id
	return nil
}

func eventPayload(t *testing.T, payload Webhook) []byte {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func (e *paymentEvents) MarkPaymentEventsProcessed(context.Context, string) error { return nil }

type paymentOrders struct {
	findID      int64
	findErr     error
	paidID      int64
	paidErr     error
	items       []contracts.OrderItem
	itemsErr    error
	delay       int
	scheduleErr error
	cancelErr   error
	status      *contracts.OrderStatus
	statusErr   error
}

func (o *paymentOrders) Create(context.Context, string, string) (int64, error) { return 0, nil }
func (o *paymentOrders) FindID(context.Context, string) (int64, error)         { return o.findID, o.findErr }
func (o *paymentOrders) Find(context.Context, string) (*contracts.StoredOrder, error) {
	return nil, nil
}
func (o *paymentOrders) FindStatus(context.Context, string) (*contracts.OrderStatus, error) {
	return o.status, o.statusErr
}
func (o *paymentOrders) ListItems(context.Context, int64) ([]contracts.OrderItem, error) {
	return o.items, o.itemsErr
}
func (o *paymentOrders) MarkPaid(_ context.Context, id int64, _ string) error {
	o.paidID = id
	return o.paidErr
}
func (o *paymentOrders) AddItems(context.Context, int64, []contracts.OrderItem) error { return nil }
func (o *paymentOrders) ScheduleSync(_ context.Context, _ int64, delay int) error {
	o.delay = delay
	return o.scheduleErr
}
func (o *paymentOrders) Cancel(context.Context, int64) error { return o.cancelErr }

type paymentAggregate struct {
	state       contracts.PaymentState
	find        *contracts.PaymentState
	orderID     *int64
	status      contracts.PaymentStatus
	paidAt      *time.Time
	upsertCount int
	upsertErr   error
	cancelErr   error
}

func (p *paymentAggregate) Upsert(_ context.Context, _ string, status contracts.PaymentStatus, orderID *int64, paidAt *time.Time) (contracts.PaymentState, error) {
	p.status, p.orderID, p.paidAt, p.upsertCount = status, orderID, paidAt, p.upsertCount+1
	return p.state, p.upsertErr
}
func (p *paymentAggregate) Find(context.Context, string) (*contracts.PaymentState, error) {
	return p.find, nil
}
func (p *paymentAggregate) LinkOrder(context.Context, string, int64) error { return nil }
func (p *paymentAggregate) Cancel(_ context.Context, _ string, _ *int64) (contracts.PaymentState, error) {
	return p.state, p.cancelErr
}

type paymentClassifier struct {
	hardware bool
	err      error
}

func (c paymentClassifier) HasHardware(context.Context, []string) (bool, error) {
	return c.hardware, c.err
}

func TestServiceStoresPaymentBeforeShopOrder(t *testing.T) {
	timestamp := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	orders := &paymentOrders{}
	events := &paymentEvents{}
	aggregate := &paymentAggregate{state: contracts.PaymentState{ID: 11, Status: "COMPLETED", PaidAt: &timestamp}}
	service := NewService(paymentTransaction{repos: contracts.TransactionRepositories{
		Orders: orders, Events: events, Payments: aggregate,
	}}, config.Config{})

	result, err := service.Process(context.Background(), Webhook{EventID: "payment-1", ReferenceOrderID: "order-1", PaymentStatus: "COMPLETED", Timestamp: timestamp.Format(time.RFC3339Nano)})
	if err != nil || result.Message != "Payment stored and awaiting shop order" {
		t.Fatalf("Process() = %+v, %v", result, err)
	}
	if aggregate.orderID != nil || events.marked != "payment-1" || aggregate.upsertCount != 1 {
		t.Fatalf("payment-before-shop state = aggregate %+v, events %+v", aggregate, events)
	}
}

func TestServiceReturnsCurrentStateForDuplicatePaymentEvent(t *testing.T) {
	orderID := int64(7)
	orders := &paymentOrders{status: &contracts.OrderStatus{PaymentStatus: contracts.PaymentStatusCompleted, Status: contracts.OrderStatePaid, SyncStatus: contracts.SyncStatusSynced}}
	payload := Webhook{EventID: "payment-duplicate", ReferenceOrderID: "order-1", PaymentStatus: contracts.PaymentStatusCompleted, Timestamp: "2026-08-26T10:00:00Z"}
	payments := &paymentAggregate{find: &contracts.PaymentState{Status: contracts.PaymentStatusCompleted, OrderID: &orderID}}
	service := NewService(paymentTransaction{repos: contracts.TransactionRepositories{Orders: orders, Events: &paymentEvents{record: true, payload: eventPayload(t, payload)}, Payments: payments}}, config.Config{})

	result, err := service.Process(context.Background(), payload)
	if err != nil || !result.Duplicate || result.Message != "Payment event already processed" || result.PaymentStatus != contracts.PaymentStatusCompleted || result.OrderStatus != contracts.OrderStatePaid || result.SyncStatus != contracts.SyncStatusSynced {
		t.Fatalf("duplicate result = %+v, %v", result, err)
	}
}

func TestServiceRejectsConflictingDuplicatePaymentEvent(t *testing.T) {
	stored := Webhook{EventID: "payment-duplicate", ReferenceOrderID: "order-1", PaymentStatus: contracts.PaymentStatusPending, Timestamp: "2026-08-26T10:00:00Z"}
	incoming := stored
	incoming.PaymentStatus = contracts.PaymentStatusFailed
	service := NewService(paymentTransaction{repos: contracts.TransactionRepositories{Orders: &paymentOrders{}, Events: &paymentEvents{record: true, payload: eventPayload(t, stored)}, Payments: &paymentAggregate{find: &contracts.PaymentState{Status: contracts.PaymentStatusFailed}}}}, config.Config{})

	_, err := service.Process(context.Background(), incoming)
	if !errors.Is(err, contracts.ErrPaymentPayloadConflict) {
		t.Fatalf("conflicting duplicate error = %v", err)
	}
}

func TestServiceDoesNotDowngradeCompletedPayment(t *testing.T) {
	orderID := int64(7)
	timestamp := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	orders := &paymentOrders{findID: orderID}
	events := &paymentEvents{}
	aggregate := &paymentAggregate{state: contracts.PaymentState{ID: 11, Status: "COMPLETED", PaidAt: &timestamp}}
	aggregate.find = &aggregate.state
	service := NewService(paymentTransaction{repos: contracts.TransactionRepositories{
		Orders: orders, Events: events, Payments: aggregate,
	}}, config.Config{})

	_, err := service.Process(context.Background(), Webhook{EventID: "payment-2", ReferenceOrderID: "order-1", PaymentStatus: "FAILED", Timestamp: timestamp.Format(time.RFC3339Nano)})
	if !errors.Is(err, contracts.ErrPaymentFinalized) || orders.paidID != 0 || events.marked != "" {
		t.Fatalf("terminal downgrade = orders %+v, events %+v, err %v", orders, events, err)
	}
}

func TestServiceAllowsNonterminalUpdatesAndNoOpsMatchingTerminalStatus(t *testing.T) {
	timestamp := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	t.Run("updates pending payment", func(t *testing.T) {
		state := contracts.PaymentState{Status: contracts.PaymentStatusPending}
		aggregate := &paymentAggregate{state: state, find: &state}
		events := &paymentEvents{}
		service := NewService(paymentTransaction{repos: contracts.TransactionRepositories{Orders: &paymentOrders{}, Events: events, Payments: aggregate}}, config.Config{})
		if _, err := service.Process(context.Background(), Webhook{EventID: "payment-failed", ReferenceOrderID: "order-1", PaymentStatus: contracts.PaymentStatusFailed, Timestamp: timestamp.Format(time.RFC3339)}); err != nil {
			t.Fatal(err)
		}
		if aggregate.upsertCount != 1 || aggregate.status != contracts.PaymentStatusFailed || events.marked != "payment-failed" {
			t.Fatalf("nonterminal update = aggregate=%+v events=%+v", aggregate, events)
		}
	})
	t.Run("replays terminal payment", func(t *testing.T) {
		state := contracts.PaymentState{Status: contracts.PaymentStatusCancelled}
		aggregate := &paymentAggregate{state: state, find: &state}
		events := &paymentEvents{}
		service := NewService(paymentTransaction{repos: contracts.TransactionRepositories{Orders: &paymentOrders{}, Events: events, Payments: aggregate}}, config.Config{})
		result, err := service.Process(context.Background(), Webhook{EventID: "payment-cancelled-replay", ReferenceOrderID: "order-1", PaymentStatus: contracts.PaymentStatusCancelled, Timestamp: timestamp.Add(time.Hour).Format(time.RFC3339)})
		if err != nil || !result.Duplicate || result.PaymentStatus != contracts.PaymentStatusCancelled || aggregate.upsertCount != 0 || events.marked != "payment-cancelled-replay" {
			t.Fatalf("terminal replay = %+v aggregate=%+v events=%+v err=%v", result, aggregate, events, err)
		}
	})
}

func TestServicePaymentCriticalBranches(t *testing.T) {
	timestamp := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	base := func(orders *paymentOrders, events *paymentEvents, payments *paymentAggregate, classifier paymentClassifier) *Service {
		return NewService(paymentTransaction{repos: contracts.TransactionRepositories{
			Orders: orders, Events: events, Payments: payments, SKUClassifier: classifier,
		}}, config.Config{HardwareSyncDelaySeconds: 9})
	}

	t.Run("duplicate and malformed events", func(t *testing.T) {
		orders := &paymentOrders{}
		duplicatePayload := Webhook{EventID: "dup", ReferenceOrderID: "o", PaymentStatus: "FAILED", Timestamp: timestamp.Format(time.RFC3339)}
		duplicatePayment := &paymentAggregate{find: &contracts.PaymentState{Status: contracts.PaymentStatusFailed}}
		if result, err := base(orders, &paymentEvents{record: true, payload: eventPayload(t, duplicatePayload)}, duplicatePayment, paymentClassifier{}).Process(context.Background(), duplicatePayload); err != nil || !result.Duplicate || result.PaymentStatus != contracts.PaymentStatusFailed {
			t.Fatalf("duplicate = %+v, %v", result, err)
		}
		if _, err := base(orders, &paymentEvents{}, &paymentAggregate{}, paymentClassifier{}).Process(context.Background(), Webhook{EventID: "bad", ReferenceOrderID: "o", PaymentStatus: "FAILED", Timestamp: "bad"}); err == nil {
			t.Fatal("malformed timestamp unexpectedly succeeded")
		}
		if _, err := base(orders, &paymentEvents{recordErr: errors.New("record failed")}, &paymentAggregate{}, paymentClassifier{}).Process(context.Background(), Webhook{EventID: "record-error", ReferenceOrderID: "o", PaymentStatus: "FAILED", Timestamp: timestamp.Format(time.RFC3339)}); err == nil {
			t.Fatal("record failure unexpectedly succeeded")
		}
		if _, err := base(&paymentOrders{findErr: errors.New("find failed")}, &paymentEvents{}, &paymentAggregate{}, paymentClassifier{}).Process(context.Background(), Webhook{EventID: "find-error", ReferenceOrderID: "o", PaymentStatus: "FAILED", Timestamp: timestamp.Format(time.RFC3339)}); err == nil {
			t.Fatal("find failure unexpectedly succeeded")
		}
	})

	t.Run("completed existing order marks paid and schedules", func(t *testing.T) {
		orders := &paymentOrders{findID: 7, items: []contracts.OrderItem{{SKU: "sku"}}}
		events := &paymentEvents{}
		paidAt := timestamp
		payments := &paymentAggregate{state: contracts.PaymentState{Status: contracts.PaymentStatusCompleted, PaidAt: &paidAt}}
		result, err := base(orders, events, payments, paymentClassifier{hardware: true}).Process(context.Background(), Webhook{EventID: "paid", ReferenceOrderID: "o", PaymentStatus: contracts.PaymentStatusCompleted, Timestamp: timestamp.Format(time.RFC3339)})
		if err != nil || result.Message != MessageOrderMarkedPaid || orders.paidID != 7 || orders.delay != 9 || events.marked != "paid" {
			t.Fatalf("completed = %+v, orders=%+v events=%+v err=%v", result, orders, events, err)
		}
	})

	t.Run("completed payment without paid timestamp fails", func(t *testing.T) {
		orders := &paymentOrders{findID: 7}
		payments := &paymentAggregate{state: contracts.PaymentState{Status: contracts.PaymentStatusCompleted}}
		if _, err := base(orders, &paymentEvents{}, payments, paymentClassifier{}).Process(context.Background(), Webhook{EventID: "missing-paid-at", ReferenceOrderID: "o", PaymentStatus: contracts.PaymentStatusCompleted, Timestamp: timestamp.Format(time.RFC3339)}); err == nil {
			t.Fatal("missing paid_at unexpectedly succeeded")
		}
	})

	t.Run("cancelled hardware order is cancelled", func(t *testing.T) {
		orders := &paymentOrders{findID: 7, items: []contracts.OrderItem{{SKU: "sku"}}}
		events := &paymentEvents{}
		payments := &paymentAggregate{}
		result, err := base(orders, events, payments, paymentClassifier{hardware: true}).Process(context.Background(), Webhook{EventID: "cancel", ReferenceOrderID: "o", PaymentStatus: contracts.PaymentStatusCancelled, Timestamp: timestamp.Format(time.RFC3339)})
		if err != nil || result.Message != MessageOrderCancelled || orders.cancelErr != nil || events.marked != "cancel" {
			t.Fatalf("cancel = %+v, orders=%+v events=%+v err=%v", result, orders, events, err)
		}
	})

	t.Run("cancellation and completion error branches propagate", func(t *testing.T) {
		if _, err := base(&paymentOrders{findID: 7, items: []contracts.OrderItem{{SKU: "sku"}}}, &paymentEvents{}, &paymentAggregate{}, paymentClassifier{err: errors.New("classify failed")}).Process(context.Background(), Webhook{EventID: "cancel-classify", ReferenceOrderID: "o", PaymentStatus: contracts.PaymentStatusCancelled, Timestamp: timestamp.Format(time.RFC3339)}); err == nil {
			t.Fatal("cancellation classification failure unexpectedly succeeded")
		}
		if _, err := base(&paymentOrders{}, &paymentEvents{markedErr: errors.New("mark failed")}, &paymentAggregate{cancelErr: errors.New("cancel payment failed")}, paymentClassifier{}).Process(context.Background(), Webhook{EventID: "cancel-payment", ReferenceOrderID: "o", PaymentStatus: contracts.PaymentStatusCancelled, Timestamp: timestamp.Format(time.RFC3339)}); err == nil {
			t.Fatal("payment cancellation failure unexpectedly succeeded")
		}
		if _, err := base(&paymentOrders{}, &paymentEvents{markedErr: errors.New("mark failed")}, &paymentAggregate{}, paymentClassifier{}).Process(context.Background(), Webhook{EventID: "cancel-mark", ReferenceOrderID: "o", PaymentStatus: contracts.PaymentStatusCancelled, Timestamp: timestamp.Format(time.RFC3339)}); err == nil {
			t.Fatal("cancellation event failure unexpectedly succeeded")
		}
		paidAt := timestamp
		if _, err := base(&paymentOrders{findID: 7, itemsErr: errors.New("items failed")}, &paymentEvents{}, &paymentAggregate{state: contracts.PaymentState{Status: contracts.PaymentStatusCompleted, PaidAt: &paidAt}}, paymentClassifier{}).Process(context.Background(), Webhook{EventID: "complete-items", ReferenceOrderID: "o", PaymentStatus: contracts.PaymentStatusCompleted, Timestamp: timestamp.Format(time.RFC3339)}); err == nil {
			t.Fatal("completion item failure unexpectedly succeeded")
		}
		if _, err := base(&paymentOrders{findID: 7, items: []contracts.OrderItem{{SKU: "sku"}}}, &paymentEvents{markedErr: errors.New("mark failed")}, &paymentAggregate{state: contracts.PaymentState{Status: contracts.PaymentStatusCompleted, PaidAt: &paidAt}}, paymentClassifier{hardware: true}).Process(context.Background(), Webhook{EventID: "complete-mark", ReferenceOrderID: "o", PaymentStatus: contracts.PaymentStatusCompleted, Timestamp: timestamp.Format(time.RFC3339)}); err == nil {
			t.Fatal("completion event failure unexpectedly succeeded")
		}
	})

	t.Run("digital cancellation is rejected", func(t *testing.T) {
		orders := &paymentOrders{findID: 7, items: []contracts.OrderItem{{SKU: "digital"}}}
		if _, err := base(orders, &paymentEvents{}, &paymentAggregate{}, paymentClassifier{}).Process(context.Background(), Webhook{EventID: "digital-cancel", ReferenceOrderID: "o", PaymentStatus: contracts.PaymentStatusCancelled, Timestamp: timestamp.Format(time.RFC3339)}); err != contracts.ErrDigitalCancellation {
			t.Fatalf("digital cancellation error = %v", err)
		}
	})

	t.Run("cancellation before shop order records cancelled payment", func(t *testing.T) {
		events := &paymentEvents{}
		result, err := base(&paymentOrders{}, events, &paymentAggregate{}, paymentClassifier{}).Process(context.Background(), Webhook{EventID: "cancel-before-shop", ReferenceOrderID: "o", PaymentStatus: contracts.PaymentStatusCancelled, Timestamp: timestamp.Format(time.RFC3339)})
		if err != nil || result.Message != MessagePaymentRecorded || events.marked != "cancel-before-shop" {
			t.Fatalf("cancel before shop = %+v, events=%+v err=%v", result, events, err)
		}
	})

	t.Run("payment and completion persistence failures propagate", func(t *testing.T) {
		payload := Webhook{EventID: "failure", ReferenceOrderID: "o", PaymentStatus: contracts.PaymentStatusFailed, Timestamp: timestamp.Format(time.RFC3339)}
		if _, err := base(&paymentOrders{}, &paymentEvents{}, &paymentAggregate{upsertErr: errors.New("upsert failed")}, paymentClassifier{}).Process(context.Background(), payload); err == nil {
			t.Fatal("upsert failure unexpectedly succeeded")
		}
		if _, err := base(&paymentOrders{}, &paymentEvents{markedErr: errors.New("mark failed")}, &paymentAggregate{}, paymentClassifier{}).Process(context.Background(), payload); err == nil {
			t.Fatal("mark failure unexpectedly succeeded")
		}
		orders := &paymentOrders{findID: 7, items: []contracts.OrderItem{{SKU: "sku"}}}
		paidAt := timestamp
		payments := &paymentAggregate{state: contracts.PaymentState{Status: contracts.PaymentStatusCompleted, PaidAt: &paidAt}}
		if _, err := base(orders, &paymentEvents{}, payments, paymentClassifier{err: errors.New("classify failed")}).Process(context.Background(), Webhook{EventID: "classify", ReferenceOrderID: "o", PaymentStatus: contracts.PaymentStatusCompleted, Timestamp: timestamp.Format(time.RFC3339)}); err == nil {
			t.Fatal("classification failure unexpectedly succeeded")
		}
		if _, err := base(&paymentOrders{findID: 7, items: []contracts.OrderItem{{SKU: "sku"}}, paidErr: errors.New("paid failed")}, &paymentEvents{}, payments, paymentClassifier{hardware: true}).Process(context.Background(), Webhook{EventID: "paid-error", ReferenceOrderID: "o", PaymentStatus: contracts.PaymentStatusCompleted, Timestamp: timestamp.Format(time.RFC3339)}); err == nil {
			t.Fatal("mark paid failure unexpectedly succeeded")
		}
		if _, err := base(&paymentOrders{findID: 7, items: []contracts.OrderItem{{SKU: "sku"}}, scheduleErr: errors.New("schedule failed")}, &paymentEvents{}, payments, paymentClassifier{hardware: true}).Process(context.Background(), Webhook{EventID: "schedule-error", ReferenceOrderID: "o", PaymentStatus: contracts.PaymentStatusCompleted, Timestamp: timestamp.Format(time.RFC3339)}); err == nil {
			t.Fatal("schedule failure unexpectedly succeeded")
		}
	})

	t.Run("result projection and cancellation errors propagate", func(t *testing.T) {
		status := &contracts.OrderStatus{PaymentStatus: contracts.PaymentStatusFailed, Status: contracts.OrderStatePending, SyncStatus: contracts.SyncStatusPending}
		orders := &paymentOrders{findID: 7, status: status}
		result, err := base(orders, &paymentEvents{}, &paymentAggregate{state: contracts.PaymentState{Status: contracts.PaymentStatusFailed}}, paymentClassifier{}).Process(context.Background(), Webhook{EventID: "status", ReferenceOrderID: "o", PaymentStatus: contracts.PaymentStatusFailed, Timestamp: timestamp.Format(time.RFC3339)})
		if err != nil || result.OrderStatus != contracts.OrderStatePending {
			t.Fatalf("status result = %+v, %v", result, err)
		}
		if _, err := base(&paymentOrders{findID: 7, statusErr: errors.New("status failed")}, &paymentEvents{}, &paymentAggregate{}, paymentClassifier{}).Process(context.Background(), Webhook{EventID: "status-error", ReferenceOrderID: "o", PaymentStatus: contracts.PaymentStatusFailed, Timestamp: timestamp.Format(time.RFC3339)}); err == nil {
			t.Fatal("status failure unexpectedly succeeded")
		}
		if _, err := base(&paymentOrders{findID: 7, itemsErr: errors.New("items failed")}, &paymentEvents{}, &paymentAggregate{}, paymentClassifier{hardware: true}).Process(context.Background(), Webhook{EventID: "cancel-items-error", ReferenceOrderID: "o", PaymentStatus: contracts.PaymentStatusCancelled, Timestamp: timestamp.Format(time.RFC3339)}); err == nil {
			t.Fatal("item cancellation failure unexpectedly succeeded")
		}
		if _, err := base(&paymentOrders{findID: 7, items: []contracts.OrderItem{{SKU: "sku"}}, cancelErr: errors.New("cancel order failed")}, &paymentEvents{}, &paymentAggregate{}, paymentClassifier{hardware: true}).Process(context.Background(), Webhook{EventID: "cancel-order-error", ReferenceOrderID: "o", PaymentStatus: contracts.PaymentStatusCancelled, Timestamp: timestamp.Format(time.RFC3339)}); err == nil {
			t.Fatal("order cancellation failure unexpectedly succeeded")
		}
	})
}
