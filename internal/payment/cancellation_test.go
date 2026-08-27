package payment

import (
	"context"
	"testing"
	"time"

	"order-sync/internal/config"
	"order-sync/internal/contracts"
)

type cancellationOrders struct {
	paymentOrders
	cancelledID int64
}

func (*cancellationOrders) Find(context.Context, string) (*contracts.StoredOrder, error) {
	return nil, nil
}

func (o *cancellationOrders) Cancel(_ context.Context, id int64) error {
	o.cancelledID = id
	return nil
}

type cancellationSKUClassifier struct{ hardware bool }

func (c cancellationSKUClassifier) HasHardware(context.Context, []string) (bool, error) {
	return c.hardware, nil
}

func TestValidatePaymentWebhookAcceptsCancelled(t *testing.T) {
	webhook := Webhook{
		EventID:          "payment-cancelled",
		ReferenceOrderID: "order-1",
		PaymentStatus:    contracts.PaymentStatusCancelled,
		Timestamp:        "2026-08-29T10:00:00Z",
	}
	if issues := ValidatePaymentWebhook(webhook); len(issues) != 0 {
		t.Fatalf("cancelled payment webhook has issues: %+v", issues)
	}
}

func TestServiceCancelsHardwareOrder(t *testing.T) {
	orderID := int64(7)
	hardware := true
	orders := &cancellationOrders{paymentOrders: paymentOrders{findID: orderID, items: []contracts.OrderItem{{SKU: "NUKI-SL3", IsHardware: &hardware}}}}
	events := &paymentEvents{}
	aggregate := &paymentAggregate{state: contracts.PaymentState{ID: 11, Status: contracts.PaymentStatusCancelled}}
	service := NewService(paymentTransaction{repos: contracts.TransactionRepositories{
		Orders:        orders,
		Events:        events,
		Payments:      aggregate,
		SKUClassifier: cancellationSKUClassifier{hardware: true},
	}}, config.Config{})

	result, err := service.Process(context.Background(), Webhook{
		EventID:          "payment-cancelled",
		ReferenceOrderID: "order-1",
		PaymentStatus:    contracts.PaymentStatusCancelled,
		Timestamp:        time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC).Format(time.RFC3339),
	})
	if err != nil || result.Message != MessageOrderCancelled {
		t.Fatalf("Process() = %+v, %v", result, err)
	}
	if orders.cancelledID != orderID || events.marked != "payment-cancelled" {
		t.Fatalf("cancellation side effects = order %d, event %q", orders.cancelledID, events.marked)
	}
}

func TestServiceRejectsDigitalOrderCancellation(t *testing.T) {
	orderID := int64(7)
	orders := &cancellationOrders{paymentOrders: paymentOrders{findID: orderID, items: []contracts.OrderItem{{SKU: "DIGITAL"}}}}
	service := NewService(paymentTransaction{repos: contracts.TransactionRepositories{
		Orders:        orders,
		Events:        &paymentEvents{},
		Payments:      &paymentAggregate{},
		SKUClassifier: cancellationSKUClassifier{},
	}}, config.Config{})

	_, err := service.Process(context.Background(), Webhook{
		EventID:          "payment-cancelled-digital",
		ReferenceOrderID: "order-1",
		PaymentStatus:    contracts.PaymentStatusCancelled,
		Timestamp:        "2026-08-29T10:00:00Z",
	})
	if err != contracts.ErrDigitalCancellation {
		t.Fatalf("digital cancellation error = %v, want %v", err, contracts.ErrDigitalCancellation)
	}
	if orders.cancelledID != 0 {
		t.Fatal("digital cancellation unexpectedly cancelled the order")
	}
}
