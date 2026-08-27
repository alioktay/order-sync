//go:build integration

package integration_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"order-sync/internal/contracts"
	"order-sync/internal/infrastructure/postgres/repositories"
	"order-sync/internal/infrastructure/sap"
	ordersync "order-sync/internal/sync"
)

func TestFinalizedHardwarePaymentCannotCancelScheduledSAPDelivery(t *testing.T) {
	pool := integrationPool(t)
	recorder := newSAPRecorder("")
	sapServer := httptest.NewServer(recorder)
	defer sapServer.Close()

	cfg := integrationConfig(sapServer.URL)
	cfg.HardwareSyncDelaySeconds = 10
	service := integrationService(pool, cfg)
	worker := ordersync.NewWorker(repositories.NewSyncJobRepository(pool), sap.NewClient(cfg), cfg, integrationLogger())
	worker.Start()
	defer stopWorker(t, worker)

	order := shopPayload("shop-cancel", "ORD-CANCEL", "NUKI-SL3")
	if _, err := service.shopService.Process(context.Background(), order); err != nil {
		t.Fatal(err)
	}
	if _, err := service.paymentService.Process(context.Background(), paymentPayload("pay-complete", order.OrderID)); err != nil {
		t.Fatal(err)
	}
	_, err := service.paymentService.Process(context.Background(), paymentPayloadWithStatus("pay-cancel", order.OrderID, string(contracts.PaymentStatusCancelled)))
	if !errors.Is(err, contracts.ErrPaymentFinalized) {
		t.Fatalf("cancellation error = %v, want ErrPaymentFinalized", err)
	}

	status, err := service.orders.GetOrderStatus(context.Background(), order.OrderID)
	if err != nil || status == nil || status.Status != contracts.OrderStatePaid || status.SyncStatus == contracts.SyncStatusCancelled {
		t.Fatalf("finalized order status = %+v, %v", status, err)
	}
	if got := recorder.count(order.OrderID); got != 0 {
		t.Fatalf("delayed order reached SAP %d times", got)
	}
}

func TestCancellationBeforeShopOrderIsReconciled(t *testing.T) {
	pool := integrationPool(t)
	service := integrationService(pool, integrationConfig(""))
	orderID := "ORD-CANCEL-BEFORE-SHOP"
	if _, err := service.paymentService.Process(context.Background(), paymentPayloadWithStatus("pay-cancel-first", orderID, string(contracts.PaymentStatusCancelled))); err != nil {
		t.Fatal(err)
	}
	if _, err := service.shopService.Process(context.Background(), shopPayload("shop-cancel-first", orderID, "NUKI-SL3")); err != nil {
		t.Fatal(err)
	}
	status, err := service.orders.GetOrderStatus(context.Background(), orderID)
	if err != nil || status == nil || status.Status != contracts.OrderStateCancelled || status.PaymentStatus != contracts.PaymentStatusCancelled || status.SyncStatus != "NOT_SCHEDULED" {
		t.Fatalf("out-of-order cancellation status = %+v, %v", status, err)
	}
	var processed int64
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM webhook_events WHERE event_type = 'PAYMENT' AND payload->>'reference_order_id' = $1 AND processed_at IS NOT NULL`, orderID).Scan(&processed); err != nil {
		t.Fatal(err)
	}
	if processed != 1 {
		t.Fatalf("processed payment events = %d, want 1", processed)
	}
}

func TestDigitalCancellationIsRejectedWithoutChangingOrder(t *testing.T) {
	pool := integrationPool(t)
	service := integrationService(pool, integrationConfig(""))
	order := shopPayload("shop-digital-cancel", "ORD-DIGITAL-CANCEL", "NUKI-SMART-HOSTING")
	if _, err := service.shopService.Process(context.Background(), order); err != nil {
		t.Fatal(err)
	}
	if _, err := service.paymentService.Process(context.Background(), paymentPayload("pay-digital-cancel", order.OrderID)); err != nil {
		t.Fatal(err)
	}
	_, err := service.paymentService.Process(context.Background(), paymentPayloadWithStatus("pay-digital-cancel-request", order.OrderID, string(contracts.PaymentStatusCancelled)))
	if !errors.Is(err, contracts.ErrPaymentFinalized) {
		t.Fatalf("digital cancellation error = %v, want ErrPaymentFinalized", err)
	}
	status, err := service.orders.GetOrderStatus(context.Background(), order.OrderID)
	if err != nil || status == nil || status.Status != contracts.OrderStatePaid || status.PaymentStatus != contracts.PaymentStatusCompleted {
		t.Fatalf("digital cancellation changed order = %+v, %v", status, err)
	}
}
