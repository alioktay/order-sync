package shop

import (
	"context"
	"errors"
	"testing"
	"time"

	"order-sync/internal/config"
	"order-sync/internal/contracts"
)

type shopTransaction struct {
	repos contracts.TransactionRepositories
}

func (t shopTransaction) Run(_ context.Context, work func(contracts.TransactionRepositories) (contracts.WebhookResult, error)) (contracts.WebhookResult, error) {
	return work(t.repos)
}

type shopEvents struct {
	markedPaymentOrder string
	markedEvent        string
	record             bool
	recordErr          error
	markedErr          error
	markedPaymentErr   error
}

func (e *shopEvents) Record(context.Context, string, contracts.EventType, any) (bool, error) {
	return !e.record, e.recordErr
}
func (e *shopEvents) FindPayload(context.Context, contracts.EventType, string) ([]byte, error) {
	return nil, nil
}
func (e *shopEvents) MarkProcessed(_ context.Context, _ contracts.EventType, id string) error {
	if e.markedErr != nil {
		return e.markedErr
	}
	e.markedEvent = id
	return nil
}
func (e *shopEvents) MarkPaymentEventsProcessed(_ context.Context, orderID string) error {
	if e.markedPaymentErr != nil {
		return e.markedPaymentErr
	}
	e.markedPaymentOrder = orderID
	return nil
}

type shopOrders struct {
	createID    int64
	returnZero  bool
	createErr   error
	addErr      error
	paidID      int64
	paidErr     error
	delay       int
	scheduleErr error
	cancelErr   error
	status      *contracts.OrderStatus
	statusErr   error
	existing    *contracts.StoredOrder
	findErr     error
}

type shopSKUClassifier struct {
	hardware bool
}

func (c shopSKUClassifier) HasHardware(context.Context, []string) (bool, error) {
	return c.hardware, nil
}

func (o *shopOrders) Create(context.Context, string, string) (int64, error) {
	if o.returnZero {
		return 0, o.createErr
	}
	if o.createID == 0 {
		return 7, o.createErr
	}
	return o.createID, o.createErr
}
func (o *shopOrders) FindID(context.Context, string) (int64, error) { return 0, nil }
func (o *shopOrders) Find(context.Context, string) (*contracts.StoredOrder, error) {
	return o.existing, o.findErr
}

func TestServiceAcceptsMatchingShopOrderWithNewEventID(t *testing.T) {
	hardware := true
	notHardware := false
	orders := &shopOrders{returnZero: true, existing: &contracts.StoredOrder{
		ID: 7, CustomerEmail: "buyer@example.com",
		Items: []contracts.OrderItem{{SKU: "NUKI-SL3", Quantity: 1, Price: 169, IsHardware: &hardware}, {SKU: "NUKI-BRIDGE", Quantity: 2, Price: 49, IsHardware: &notHardware}},
	}}
	events := &shopEvents{}
	service := NewService(shopTransaction{repos: contracts.TransactionRepositories{Orders: orders, Events: events, Payments: &shopPayments{}}}, config.Config{})

	result, err := service.Process(context.Background(), Webhook{EventID: "shop-replay", OrderID: "order-1", CustomerEmail: "buyer@example.com", Items: []contracts.OrderItem{{SKU: "NUKI-BRIDGE", Quantity: 2, Price: 49, IsHardware: &notHardware}, {SKU: "NUKI-SL3", Quantity: 1, Price: 169, IsHardware: &hardware}}})
	if err != nil || !result.Duplicate || events.markedEvent != "shop-replay" {
		t.Fatalf("matching replay = %+v, events=%+v, err=%v", result, events, err)
	}
}

func TestServiceReturnsCurrentStateForDuplicateShopEvent(t *testing.T) {
	hardware := true
	orders := &shopOrders{
		returnZero: true,
		existing: &contracts.StoredOrder{
			ID: 7, CustomerEmail: "buyer@example.com",
			Items: []contracts.OrderItem{{SKU: "NUKI-SL3", Quantity: 1, Price: 169, IsHardware: &hardware}},
		},
		status: &contracts.OrderStatus{PaymentStatus: contracts.PaymentStatusCompleted, Status: contracts.OrderStatePaid, SyncStatus: contracts.SyncStatusSynced},
	}
	service := NewService(shopTransaction{repos: contracts.TransactionRepositories{Orders: orders, Events: &shopEvents{record: true}, Payments: &shopPayments{}}}, config.Config{})

	result, err := service.Process(context.Background(), Webhook{EventID: "shop-duplicate", OrderID: "order-1", CustomerEmail: "buyer@example.com", Items: []contracts.OrderItem{{SKU: "NUKI-SL3", Quantity: 1, Price: 169, IsHardware: &hardware}}})
	if err != nil || !result.Duplicate || result.Message != "Shop event already processed" || result.PaymentStatus != contracts.PaymentStatusCompleted || result.OrderStatus != contracts.OrderStatePaid || result.SyncStatus != contracts.SyncStatusSynced {
		t.Fatalf("duplicate result = %+v, %v", result, err)
	}
}

func TestServiceRejectsConflictingShopOrderPayload(t *testing.T) {
	hardware := true
	base := contracts.StoredOrder{ID: 7, CustomerEmail: "buyer@example.com", Items: []contracts.OrderItem{{SKU: "NUKI-SL3", Quantity: 1, Price: 169, IsHardware: &hardware}}}
	tests := []Webhook{
		{CustomerEmail: "other@example.com", Items: base.Items},
		{CustomerEmail: base.CustomerEmail, Items: []contracts.OrderItem{{SKU: "NUKI-BRIDGE", Quantity: 1, Price: 169, IsHardware: &hardware}}},
		{CustomerEmail: base.CustomerEmail, Items: []contracts.OrderItem{{SKU: "NUKI-SL3", Quantity: 2, Price: 169, IsHardware: &hardware}}},
		{CustomerEmail: base.CustomerEmail, Items: []contracts.OrderItem{{SKU: "NUKI-SL3", Quantity: 1, Price: 170, IsHardware: &hardware}}},
		{CustomerEmail: base.CustomerEmail, Items: []contracts.OrderItem{{SKU: "NUKI-SL3", Quantity: 1, Price: 169}}},
	}
	for _, payload := range tests {
		orders := &shopOrders{returnZero: true, existing: &base}
		payload.EventID, payload.OrderID = "shop-conflict", "order-1"
		_, err := NewService(shopTransaction{repos: contracts.TransactionRepositories{Orders: orders, Events: &shopEvents{record: true}, Payments: &shopPayments{}}}, config.Config{}).Process(context.Background(), payload)
		if !errors.Is(err, contracts.ErrOrderPayloadConflict) {
			t.Fatalf("conflicting payload error = %v", err)
		}
	}
}
func (o *shopOrders) FindStatus(context.Context, string) (*contracts.OrderStatus, error) {
	return o.status, o.statusErr
}
func (o *shopOrders) ListItems(context.Context, int64) ([]contracts.OrderItem, error) {
	return nil, nil
}
func (o *shopOrders) MarkPaid(_ context.Context, id int64, _ string) error {
	o.paidID = id
	return o.paidErr
}
func (o *shopOrders) AddItems(context.Context, int64, []contracts.OrderItem) error { return o.addErr }
func (o *shopOrders) ScheduleSync(_ context.Context, _ int64, delay int) error {
	o.delay = delay
	return o.scheduleErr
}
func (o *shopOrders) Cancel(context.Context, int64) error { return o.cancelErr }

type shopPayments struct {
	state   *contracts.PaymentState
	findErr error
	linkErr error
	linked  int64
}

func (p *shopPayments) Upsert(context.Context, string, contracts.PaymentStatus, *int64, *time.Time) (contracts.PaymentState, error) {
	return contracts.PaymentState{}, nil
}
func (p *shopPayments) Find(context.Context, string) (*contracts.PaymentState, error) {
	return p.state, p.findErr
}
func (p *shopPayments) LinkOrder(_ context.Context, _ string, id int64) error {
	p.linked = id
	return p.linkErr
}
func (p *shopPayments) Cancel(context.Context, string, *int64) (contracts.PaymentState, error) {
	return contracts.PaymentState{}, nil
}

func TestServiceReconcilesCompletedPayment(t *testing.T) {
	paidAt := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	orders := &shopOrders{}
	events := &shopEvents{}
	payments := &shopPayments{state: &contracts.PaymentState{Status: "COMPLETED", PaidAt: &paidAt}}
	service := NewService(shopTransaction{repos: contracts.TransactionRepositories{
		Orders: orders, SKUClassifier: shopSKUClassifier{hardware: true}, Events: events, Payments: payments,
	}}, config.Config{HardwareSyncDelaySeconds: 30})

	result, err := service.Process(context.Background(), Webhook{
		EventID: "shop-1", OrderID: "order-1", CustomerEmail: "buyer@example.com",
		Items: []contracts.OrderItem{{SKU: "NUKI-SL3", Quantity: 1}},
	})
	if err != nil || result.Message != "Order stored" {
		t.Fatalf("Process() = %+v, %v", result, err)
	}
	if orders.paidID != 7 || orders.delay != 30 || payments.linked != 7 || events.markedPaymentOrder != "order-1" || events.markedEvent != "shop-1" {
		t.Fatalf("reconciliation state = order %+v, payment %+v, events %+v", orders, payments, events)
	}
}

func TestServiceLinksNonCompletedPaymentWithoutMarkingOrderPaid(t *testing.T) {
	orders := &shopOrders{}
	payments := &shopPayments{state: &contracts.PaymentState{Status: "FAILED"}}
	service := NewService(shopTransaction{repos: contracts.TransactionRepositories{
		Orders: orders, Events: &shopEvents{}, Payments: payments,
	}}, config.Config{HardwareSyncDelaySeconds: 30})
	if _, err := service.Process(context.Background(), Webhook{EventID: "shop-1", OrderID: "order-1", CustomerEmail: "buyer@example.com", Items: []contracts.OrderItem{{SKU: "digital", Quantity: 1}}}); err != nil {
		t.Fatal(err)
	}
	if payments.linked != 7 || orders.paidID != 0 {
		t.Fatalf("non-completed payment state = payment %+v, order %+v", payments, orders)
	}
}

func TestServiceShopCriticalBranches(t *testing.T) {
	paidAt := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	base := func(orders *shopOrders, events *shopEvents, payments *shopPayments, classifier shopSKUClassifier) *Service {
		return NewService(shopTransaction{repos: contracts.TransactionRepositories{
			Orders: orders, SKUClassifier: classifier, Events: events, Payments: payments,
		}}, config.Config{HardwareSyncDelaySeconds: 9})
	}
	payload := Webhook{EventID: "shop", OrderID: "order", CustomerEmail: "buyer@example.com", Items: []contracts.OrderItem{{SKU: "sku"}}}

	t.Run("duplicate and creation failures", func(t *testing.T) {
		duplicateOrders := &shopOrders{returnZero: true, existing: &contracts.StoredOrder{CustomerEmail: "buyer@example.com", Items: payload.Items}}
		if result, err := base(duplicateOrders, &shopEvents{record: true}, &shopPayments{}, shopSKUClassifier{}).Process(context.Background(), payload); err != nil || !result.Duplicate {
			t.Fatalf("duplicate = %+v, %v", result, err)
		}
		if _, err := base(&shopOrders{createErr: errors.New("create failed")}, &shopEvents{}, &shopPayments{}, shopSKUClassifier{}).Process(context.Background(), payload); err == nil {
			t.Fatal("create failure unexpectedly succeeded")
		}
		if _, err := base(&shopOrders{returnZero: true}, &shopEvents{}, &shopPayments{}, shopSKUClassifier{}).Process(context.Background(), payload); err == nil {
			t.Fatal("zero order ID unexpectedly succeeded")
		}
		if _, err := base(&shopOrders{}, &shopEvents{recordErr: errors.New("record failed")}, &shopPayments{}, shopSKUClassifier{}).Process(context.Background(), payload); err == nil {
			t.Fatal("record failure unexpectedly succeeded")
		}
	})

	t.Run("reconciliation and result errors", func(t *testing.T) {
		if _, err := base(&shopOrders{}, &shopEvents{}, &shopPayments{findErr: errors.New("find failed")}, shopSKUClassifier{}).Process(context.Background(), payload); err == nil {
			t.Fatal("payment lookup failure unexpectedly succeeded")
		}
		if _, err := base(&shopOrders{addErr: errors.New("items failed")}, &shopEvents{}, &shopPayments{}, shopSKUClassifier{}).Process(context.Background(), payload); err == nil {
			t.Fatal("item failure unexpectedly succeeded")
		}
		status := &contracts.OrderStatus{PaymentStatus: contracts.PaymentStatusCompleted, Status: contracts.OrderStatePaid, SyncStatus: contracts.SyncStatusSynced}
		orders := &shopOrders{status: status}
		result, err := base(orders, &shopEvents{}, &shopPayments{}, shopSKUClassifier{}).Process(context.Background(), payload)
		if err != nil || result.PaymentStatus != contracts.PaymentStatusCompleted || result.OrderStatus != contracts.OrderStatePaid || result.SyncStatus != contracts.SyncStatusSynced {
			t.Fatalf("status projection = %+v, %v", result, err)
		}
		if _, err := base(&shopOrders{statusErr: errors.New("status failed")}, &shopEvents{}, &shopPayments{}, shopSKUClassifier{}).Process(context.Background(), payload); err == nil {
			t.Fatal("status failure unexpectedly succeeded")
		}
	})

	t.Run("cancelled payment is reconciled", func(t *testing.T) {
		events := &shopEvents{}
		orders := &shopOrders{}
		payments := &shopPayments{state: &contracts.PaymentState{Status: contracts.PaymentStatusCancelled}}
		if result, err := base(orders, events, payments, shopSKUClassifier{}).Process(context.Background(), payload); err != nil || result.Message != "Order stored" || events.markedPaymentOrder != "order" {
			t.Fatalf("cancel reconciliation = %+v, %v; events=%+v", result, err, events)
		}
		if _, err := base(&shopOrders{cancelErr: errors.New("cancel failed")}, &shopEvents{}, &shopPayments{state: &contracts.PaymentState{Status: contracts.PaymentStatusCancelled}}, shopSKUClassifier{}).Process(context.Background(), payload); err == nil {
			t.Fatal("cancel reconciliation failure unexpectedly succeeded")
		}
		if _, err := base(&shopOrders{}, &shopEvents{markedPaymentErr: errors.New("mark payment failed")}, &shopPayments{state: &contracts.PaymentState{Status: contracts.PaymentStatusCancelled}}, shopSKUClassifier{}).Process(context.Background(), payload); err == nil {
			t.Fatal("payment event marking failure unexpectedly succeeded")
		}
	})

	t.Run("completed payment requires paid timestamp", func(t *testing.T) {
		payments := &shopPayments{state: &contracts.PaymentState{Status: contracts.PaymentStatusCompleted}}
		if _, err := base(&shopOrders{}, &shopEvents{}, payments, shopSKUClassifier{}).Process(context.Background(), payload); err == nil {
			t.Fatal("completed payment without timestamp unexpectedly succeeded")
		}
		payments.state.PaidAt = &paidAt
		if _, err := base(&shopOrders{paidErr: errors.New("paid failed")}, &shopEvents{}, payments, shopSKUClassifier{hardware: true}).Process(context.Background(), payload); err == nil {
			t.Fatal("mark-paid failure unexpectedly succeeded")
		}
		if _, err := base(&shopOrders{scheduleErr: errors.New("schedule failed")}, &shopEvents{}, payments, shopSKUClassifier{hardware: true}).Process(context.Background(), payload); err == nil {
			t.Fatal("schedule failure unexpectedly succeeded")
		}
		if _, err := base(&shopOrders{}, &shopEvents{markedPaymentErr: errors.New("mark payment failed")}, payments, shopSKUClassifier{hardware: true}).Process(context.Background(), payload); err == nil {
			t.Fatal("completed payment event failure unexpectedly succeeded")
		}
	})

	t.Run("payment link failure propagates", func(t *testing.T) {
		if _, err := base(&shopOrders{}, &shopEvents{}, &shopPayments{state: &contracts.PaymentState{Status: contracts.PaymentStatusFailed}, linkErr: errors.New("link failed")}, shopSKUClassifier{}).Process(context.Background(), payload); err == nil {
			t.Fatal("payment link failure unexpectedly succeeded")
		}
	})
}
