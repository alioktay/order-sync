//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"order-sync/internal/config"
	"order-sync/internal/contracts"
	"order-sync/internal/db"
	"order-sync/internal/infrastructure/postgres/repositories"
	"order-sync/internal/infrastructure/sap"
	"order-sync/internal/orders"
	"order-sync/internal/payment"
	"order-sync/internal/shop"
	ordersync "order-sync/internal/sync"
)

func integrationLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type sapRecorder struct {
	mu         sync.Mutex
	calls      map[string][]time.Time
	payloads   map[string]ordersync.OrderDetails
	failures   map[string]int
	alwaysFail map[string]bool
	retryOrder string
}

func newSAPRecorder(retryOrder string) *sapRecorder {
	return &sapRecorder{calls: make(map[string][]time.Time), payloads: make(map[string]ordersync.OrderDetails), failures: make(map[string]int), alwaysFail: make(map[string]bool), retryOrder: retryOrder}
}

func (s *sapRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var order ordersync.OrderDetails
	if err := json.NewDecoder(r.Body).Decode(&order); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if got := r.Header.Get("idempotency-key"); got != order.OrderID {
		http.Error(w, "missing idempotency key", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.calls[order.OrderID] = append(s.calls[order.OrderID], time.Now())
	s.payloads[order.OrderID] = order
	attempt := len(s.calls[order.OrderID])
	shouldFail := s.alwaysFail[order.OrderID] || s.failures[order.OrderID] > 0
	if s.failures[order.OrderID] > 0 {
		s.failures[order.OrderID]--
	}
	s.mu.Unlock()

	if (order.OrderID == s.retryOrder && attempt == 1) || shouldFail {
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = fmt.Fprintf(w, `{"status":"success","sap_internal_id":"SAP-%s"}`, order.OrderID)
}

func (s *sapRecorder) count(orderID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls[orderID])
}

func (s *sapRecorder) firstCall(orderID string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls[orderID]) == 0 {
		return time.Time{}
	}
	return s.calls[orderID][0]
}

func (s *sapRecorder) payload(orderID string) (ordersync.OrderDetails, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.payloads[orderID]
	return job, ok
}

func (s *sapRecorder) failNext(orderID string, count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[orderID] = count
}

func (s *sapRecorder) failAlways(orderID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alwaysFail[orderID] = true
}

func TestEndToEndTimingRetryIdempotencyAndOutOfOrderPayment(t *testing.T) {
	pool := integrationPool(t)
	recorder := newSAPRecorder("ORD-RETRY")
	sapServer := httptest.NewServer(recorder)
	defer sapServer.Close()

	cfg := integrationConfig(sapServer.URL)
	service := integrationService(pool, cfg)
	worker := ordersync.NewWorker(repositories.NewSyncJobRepository(pool), sap.NewClient(cfg), cfg, integrationLogger())
	worker.Start()
	defer stopWorker(t, worker)

	t.Run("digital order is delivered immediately and duplicate events do not redeliver", func(t *testing.T) {
		shopPayload := shopPayload("shop-digital", "ORD-DIGITAL", "NUKI-SMART-HOSTING")
		paymentPayload := paymentPayload("pay-digital", shopPayload.OrderID)
		if _, err := service.shopService.Process(context.Background(), shopPayload); err != nil {
			t.Fatal(err)
		}
		paidAt := time.Now()
		if _, err := service.paymentService.Process(context.Background(), paymentPayload); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 2*time.Second, func() bool { return recorder.count(shopPayload.OrderID) == 1 })
		if elapsed := recorder.firstCall(shopPayload.OrderID).Sub(paidAt); elapsed > time.Second {
			t.Fatalf("digital delivery was not immediate: %s", elapsed)
		}

		shopResult, err := service.shopService.Process(context.Background(), shopPayload)
		if err != nil || !shopResult.Duplicate {
			t.Fatalf("duplicate shop result: %#v, %v", shopResult, err)
		}
		paymentResult, err := service.paymentService.Process(context.Background(), paymentPayload)
		if err != nil || !paymentResult.Duplicate {
			t.Fatalf("duplicate payment result: %#v, %v", paymentResult, err)
		}
		time.Sleep(100 * time.Millisecond)
		if got := recorder.count(shopPayload.OrderID); got != 1 {
			t.Fatalf("duplicate events caused %d SAP calls", got)
		}
		var shopEvents, paymentEvents, aggregateCount int64
		var paymentStatus string
		if err := pool.QueryRow(context.Background(), `
			SELECT
				(SELECT COUNT(*) FROM webhook_events WHERE event_type = 'SHOP' AND payload->>'order_id' = $1),
				(SELECT COUNT(*) FROM webhook_events WHERE event_type = 'PAYMENT' AND payload->>'reference_order_id' = $1),
				(SELECT COUNT(*) FROM payments WHERE reference_order_id = $1),
				(SELECT status FROM payments WHERE reference_order_id = $1)`, shopPayload.OrderID).Scan(&shopEvents, &paymentEvents, &aggregateCount, &paymentStatus); err != nil {
			t.Fatal(err)
		}
		if shopEvents != 1 || paymentEvents != 1 || aggregateCount != 1 || paymentStatus != "COMPLETED" {
			t.Fatalf("typed webhook state = shop %d, payment %d, aggregate %d, status %q", shopEvents, paymentEvents, aggregateCount, paymentStatus)
		}
	})

	t.Run("payment before hardware order is reconciled without early delivery", func(t *testing.T) {
		orderID := "ORD-HARDWARE"
		if _, err := service.paymentService.Process(context.Background(), paymentPayload("pay-hardware", orderID)); err != nil {
			t.Fatal(err)
		}
		if _, err := service.shopService.Process(context.Background(), shopPayload("shop-hardware", orderID, "NUKI-SL3")); err != nil {
			t.Fatal(err)
		}
		var dueAt time.Time
		if err := pool.QueryRow(context.Background(), `SELECT j.due_at FROM sync_jobs j JOIN orders o ON o.id = j.order_id WHERE o.order_id = $1`, orderID).Scan(&dueAt); err != nil {
			t.Fatal(err)
		}
		var paymentOrderID int64
		var paymentEventsProcessed int64
		if err := pool.QueryRow(context.Background(), `
			SELECT p.order_id, COUNT(*) FILTER (WHERE e.processed_at IS NOT NULL)
			FROM payments p
			JOIN webhook_events e ON e.event_type = 'PAYMENT' AND e.payload->>'reference_order_id' = p.reference_order_id
			WHERE p.reference_order_id = $1
			GROUP BY p.order_id`, orderID).Scan(&paymentOrderID, &paymentEventsProcessed); err != nil {
			t.Fatal(err)
		}
		if paymentOrderID == 0 || paymentEventsProcessed != 1 {
			t.Fatalf("payment reconciliation = order_id %d, processed events %d", paymentOrderID, paymentEventsProcessed)
		}

		time.Sleep(850 * time.Millisecond)
		if got := recorder.count(orderID); got != 0 {
			t.Fatalf("hardware order reached SAP before the cancellation window: %d calls", got)
		}
		waitFor(t, 2*time.Second, func() bool { return recorder.count(orderID) == 1 })
		callAt := recorder.firstCall(orderID)
		if callAt.Before(dueAt) {
			t.Fatalf("hardware delivery occurred at %s before persisted due time %s", callAt, dueAt)
		}
		if lag := callAt.Sub(dueAt); lag > 250*time.Millisecond {
			t.Fatalf("hardware delivery exceeded event-driven scheduling tolerance by %s", lag)
		}
	})

	t.Run("SAP 503 is retried and eventually synchronized", func(t *testing.T) {
		shopPayload := shopPayload("shop-retry", "ORD-RETRY", "NUKI-SMART-HOSTING")
		if _, err := service.shopService.Process(context.Background(), shopPayload); err != nil {
			t.Fatal(err)
		}
		if _, err := service.paymentService.Process(context.Background(), paymentPayload("pay-retry", shopPayload.OrderID)); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 3*time.Second, func() bool { return recorder.count(shopPayload.OrderID) >= 2 })
		waitFor(t, time.Second, func() bool {
			status, err := service.orders.GetOrderStatus(context.Background(), shopPayload.OrderID)
			return err == nil && status != nil && status.SyncStatus == "SYNCED" && status.Attempts != nil && *status.Attempts == 2
		})
	})
}

func TestSAPRecoveryRetriesAndEventuallySynchronizes(t *testing.T) {
	pool := integrationPool(t)
	recorder := newSAPRecorder("")
	sapServer := httptest.NewServer(recorder)
	defer sapServer.Close()

	cfg := integrationConfig(sapServer.URL)
	cfg.SAPMaxAttempts = 1
	cfg.SAPRecoveryWindowSeconds = 10
	service := integrationService(pool, cfg)
	order := shopPayload("shop-recovery", "ORD-RECOVERY", "NUKI-SMART-HOSTING")
	recorder.failNext(order.OrderID, 1)
	worker := ordersync.NewWorker(repositories.NewSyncJobRepository(pool), sap.NewClient(cfg), cfg, integrationLogger())
	worker.Start()
	defer stopWorker(t, worker)

	if _, err := service.shopService.Process(context.Background(), order); err != nil {
		t.Fatal(err)
	}
	if _, err := service.paymentService.Process(context.Background(), paymentPayload("pay-recovery", order.OrderID)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		status, err := service.orders.GetOrderStatus(context.Background(), order.OrderID)
		return err == nil && status != nil && status.SyncStatus == "SYNCED" && status.Attempts != nil && *status.Attempts == 2
	})
	if got := recorder.count(order.OrderID); got != 2 {
		t.Fatalf("recovery made %d SAP calls, want 2", got)
	}
}

func TestSAPRecoveryEventuallyMarksDead(t *testing.T) {
	pool := integrationPool(t)
	recorder := newSAPRecorder("")
	sapServer := httptest.NewServer(recorder)
	defer sapServer.Close()

	cfg := integrationConfig(sapServer.URL)
	cfg.SAPMaxAttempts = 1
	cfg.SAPRecoveryWindowSeconds = 1
	service := integrationService(pool, cfg)
	order := shopPayload("shop-recovery-dead", "ORD-RECOVERY-DEAD", "NUKI-SMART-HOSTING")
	recorder.failAlways(order.OrderID)

	if _, err := service.shopService.Process(context.Background(), order); err != nil {
		t.Fatal(err)
	}
	if _, err := service.paymentService.Process(context.Background(), paymentPayload("pay-recovery-dead", order.OrderID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `
		UPDATE sync_jobs
		SET status = 'WAITING', due_at = NOW(), waiting_since = NOW() - INTERVAL '2 seconds', attempts = 1
		WHERE order_id = (SELECT id FROM orders WHERE order_id = $1)`, order.OrderID); err != nil {
		t.Fatal(err)
	}
	worker := ordersync.NewWorker(repositories.NewSyncJobRepository(pool), sap.NewClient(cfg), cfg, integrationLogger())
	worker.Start()
	defer stopWorker(t, worker)
	waitFor(t, 3*time.Second, func() bool {
		status, err := service.orders.GetOrderStatus(context.Background(), order.OrderID)
		return err == nil && status != nil && status.SyncStatus == "DEAD"
	})
	if got := recorder.count(order.OrderID); got != 0 {
		t.Fatalf("expired recovery made %d SAP calls, want 0", got)
	}
}

func TestStaleProcessingJobIsRecoveredAfterRestart(t *testing.T) {
	pool := integrationPool(t)
	recorder := newSAPRecorder("")
	sapServer := httptest.NewServer(recorder)
	defer sapServer.Close()

	cfg := integrationConfig(sapServer.URL)
	service := integrationService(pool, cfg)
	shopPayload := shopPayload("shop-restart", "ORD-RESTART", "NUKI-SMART-HOSTING")
	if _, err := service.shopService.Process(context.Background(), shopPayload); err != nil {
		t.Fatal(err)
	}
	if _, err := service.paymentService.Process(context.Background(), paymentPayload("pay-restart", shopPayload.OrderID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE sync_jobs SET status = 'PROCESSING', locked_at = NOW() - INTERVAL '3 minutes', attempts = 1 WHERE order_id = (SELECT id FROM orders WHERE order_id = 'ORD-RESTART')`); err != nil {
		t.Fatal(err)
	}

	restartedWorker := ordersync.NewWorker(repositories.NewSyncJobRepository(pool), sap.NewClient(cfg), cfg, integrationLogger())
	restartedWorker.Start()
	defer stopWorker(t, restartedWorker)

	waitFor(t, 2*time.Second, func() bool { return recorder.count(shopPayload.OrderID) == 1 })
	waitFor(t, time.Second, func() bool {
		status, err := service.orders.GetOrderStatus(context.Background(), shopPayload.OrderID)
		return err == nil && status != nil && status.SyncStatus == "SYNCED"
	})
}

func TestPaymentHistoryTerminalStatusAndOrderProjection(t *testing.T) {
	pool := integrationPool(t)
	service := integrationService(pool, integrationConfig(""))
	order := shopPayload("shop-history", "ORD-HISTORY", "NUKI-SMART-HOSTING")
	if _, err := service.shopService.Process(context.Background(), order); err != nil {
		t.Fatal(err)
	}
	if _, err := service.paymentService.Process(context.Background(), paymentPayloadWithStatus("pay-failed", order.OrderID, "FAILED")); err != nil {
		t.Fatal(err)
	}
	completed := paymentPayloadWithStatus("pay-completed", order.OrderID, "COMPLETED")
	if _, err := service.paymentService.Process(context.Background(), completed); err != nil {
		t.Fatal(err)
	}
	if _, err := service.paymentService.Process(context.Background(), paymentPayloadWithStatus("pay-downgrade", order.OrderID, "FAILED")); !errors.Is(err, contracts.ErrPaymentFinalized) {
		t.Fatalf("terminal payment downgrade error = %v", err)
	}
	if result, err := service.paymentService.Process(context.Background(), completed); err != nil || !result.Duplicate {
		t.Fatalf("duplicate completed payment = %+v, %v", result, err)
	}

	var paymentHistory int64
	var aggregateStatus, orderStatus string
	if err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*), MAX(p.status), MAX(o.payment_status)
		FROM webhook_events e
		JOIN payments p ON p.reference_order_id = e.payload->>'reference_order_id'
		JOIN orders o ON o.id = p.order_id
		WHERE e.event_type = 'PAYMENT' AND e.payload->>'reference_order_id' = $1`, order.OrderID).Scan(&paymentHistory, &aggregateStatus, &orderStatus); err != nil {
		t.Fatal(err)
	}
	if paymentHistory != 2 || aggregateStatus != "COMPLETED" || orderStatus != "PAID" {
		t.Fatalf("payment history = %d, aggregate status = %q, order status = %q", paymentHistory, aggregateStatus, orderStatus)
	}
	status, err := service.orders.GetOrderStatus(context.Background(), order.OrderID)
	if err != nil || status == nil || status.PaymentStatus != "COMPLETED" {
		t.Fatalf("order payment status = %+v, %v", status, err)
	}
}

func TestFieldAwareWebhookIdempotency(t *testing.T) {
	pool := integrationPool(t)
	service := integrationService(pool, integrationConfig(""))
	ctx := context.Background()

	order := shopPayload("shared-event", "ORD-FIELD-AWARE", "NUKI-SMART-HOSTING")
	if _, err := service.shopService.Process(ctx, order); err != nil {
		t.Fatal(err)
	}
	replay := order
	replay.EventID = "shop-replay"
	if result, err := service.shopService.Process(ctx, replay); err != nil || !result.Duplicate {
		t.Fatalf("matching shop replay = %+v, %v", result, err)
	}
	conflicts := []shop.Webhook{
		{EventID: "shop-email", OrderID: order.OrderID, CustomerEmail: "other@example.com", Items: order.Items},
		{EventID: "shop-sku", OrderID: order.OrderID, CustomerEmail: order.CustomerEmail, Items: []contracts.OrderItem{{SKU: "NUKI-SL3", Quantity: 1, Price: 39}}},
		{EventID: "shop-quantity", OrderID: order.OrderID, CustomerEmail: order.CustomerEmail, Items: []contracts.OrderItem{{SKU: order.Items[0].SKU, Quantity: 2, Price: 39}}},
		{EventID: "shop-price", OrderID: order.OrderID, CustomerEmail: order.CustomerEmail, Items: []contracts.OrderItem{{SKU: order.Items[0].SKU, Quantity: 1, Price: 40}}},
		func() shop.Webhook {
			override := true
			return shop.Webhook{EventID: "shop-hardware", OrderID: order.OrderID, CustomerEmail: order.CustomerEmail, Items: []contracts.OrderItem{{SKU: order.Items[0].SKU, Quantity: 1, Price: 39, IsHardware: &override}}}
		}(),
	}
	for _, conflict := range conflicts {
		if _, err := service.shopService.Process(ctx, conflict); !errors.Is(err, contracts.ErrOrderPayloadConflict) {
			t.Fatalf("shop conflict %q = %v", conflict.EventID, err)
		}
	}
	var itemCount, jobCount int
	if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM order_items i JOIN orders o ON o.id = i.order_id WHERE o.order_id = $1), (SELECT COUNT(*) FROM sync_jobs j JOIN orders o ON o.id = j.order_id WHERE o.order_id = $1)`, order.OrderID).Scan(&itemCount, &jobCount); err != nil {
		t.Fatal(err)
	}
	if itemCount != 1 || jobCount != 0 {
		t.Fatalf("shop replay duplicated state: items=%d jobs=%d", itemCount, jobCount)
	}

	// Event IDs are only unique inside a webhook type: this payment intentionally
	// reuses the shop event ID while exercising mutable payment state.
	for _, payload := range []payment.Webhook{
		paymentPayloadWithStatus("shared-event", order.OrderID, "PENDING"),
		paymentPayloadWithStatus("payment-failed", order.OrderID, "FAILED"),
		paymentPayloadWithStatus("payment-completed", order.OrderID, "COMPLETED"),
	} {
		if _, err := service.paymentService.Process(ctx, payload); err != nil {
			t.Fatalf("payment %s = %v", payload.PaymentStatus, err)
		}
	}
	if result, err := service.paymentService.Process(ctx, paymentPayloadWithStatus("payment-completed-replay", order.OrderID, "COMPLETED")); err != nil || !result.Duplicate {
		t.Fatalf("terminal replay = %+v, %v", result, err)
	}
	if _, err := service.paymentService.Process(ctx, paymentPayloadWithStatus("payment-regression", order.OrderID, "FAILED")); !errors.Is(err, contracts.ErrPaymentFinalized) {
		t.Fatalf("terminal regression = %v", err)
	}
	var paymentStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM payments WHERE reference_order_id = $1`, order.OrderID).Scan(&paymentStatus); err != nil {
		t.Fatal(err)
	}
	if paymentStatus != "COMPLETED" {
		t.Fatalf("payment status = %q", paymentStatus)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM sync_jobs j JOIN orders o ON o.id = j.order_id WHERE o.order_id = $1`, order.OrderID).Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if jobCount != 1 {
		t.Fatalf("payment updates scheduled %d jobs, want 1", jobCount)
	}
}

func TestSKUClassificationConfiguration(t *testing.T) {
	pool := integrationPool(t)
	service := integrationService(pool, integrationConfig(""))
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO sku_classifications (sku, category) VALUES
			('CUSTOM-HARDWARE', 'HARDWARE'),
			('CUSTOM-DIGITAL', 'DIGITAL')`); err != nil {
		t.Fatal(err)
	}

	classifier := repositories.NewSKUClassifier(pool)
	for _, test := range []struct {
		name string
		skus []string
		want bool
	}{
		{name: "configured hardware", skus: []string{"CUSTOM-HARDWARE"}, want: true},
		{name: "configured non-hardware", skus: []string{"CUSTOM-DIGITAL"}, want: false},
		{name: "unknown SKU", skus: []string{"UNKNOWN-SKU"}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := classifier.HasHardware(context.Background(), test.skus)
			if err != nil || got != test.want {
				t.Fatalf("HasHardware(%v) = %v, %v; want %v, nil", test.skus, got, err, test.want)
			}
		})
	}

	hardwareOrder := shopPayload("shop-custom-hardware", "ORD-CUSTOM-HARDWARE", "CUSTOM-HARDWARE")
	scheduledAt := time.Now()
	if _, err := service.shopService.Process(context.Background(), hardwareOrder); err != nil {
		t.Fatal(err)
	}
	if _, err := service.paymentService.Process(context.Background(), paymentPayload("pay-custom-hardware", hardwareOrder.OrderID)); err != nil {
		t.Fatal(err)
	}
	var hardwareDueAt time.Time
	if err := pool.QueryRow(context.Background(), `SELECT due_at FROM sync_jobs j JOIN orders o ON o.id = j.order_id WHERE o.order_id = $1`, hardwareOrder.OrderID).Scan(&hardwareDueAt); err != nil {
		t.Fatal(err)
	}
	if hardwareDueAt.Before(scheduledAt.Add(time.Second - 100*time.Millisecond)) {
		t.Fatalf("configured hardware SKU was not delayed: due_at=%s", hardwareDueAt)
	}

	if _, err := pool.Exec(context.Background(), `UPDATE sku_classifications SET category = 'DIGITAL', updated_at = NOW() WHERE sku = 'CUSTOM-HARDWARE'`); err != nil {
		t.Fatal(err)
	}
	var unchangedDueAt time.Time
	if err := pool.QueryRow(context.Background(), `SELECT due_at FROM sync_jobs j JOIN orders o ON o.id = j.order_id WHERE o.order_id = $1`, hardwareOrder.OrderID).Scan(&unchangedDueAt); err != nil {
		t.Fatal(err)
	}
	if !unchangedDueAt.Equal(hardwareDueAt) {
		t.Fatalf("classification update changed existing due_at from %s to %s", hardwareDueAt, unchangedDueAt)
	}

	digitalOrder := shopPayload("shop-custom-digital", "ORD-CUSTOM-DIGITAL", "CUSTOM-DIGITAL")
	if _, err := service.shopService.Process(context.Background(), digitalOrder); err != nil {
		t.Fatal(err)
	}
	if _, err := service.paymentService.Process(context.Background(), paymentPayload("pay-custom-digital", digitalOrder.OrderID)); err != nil {
		t.Fatal(err)
	}
	var digitalDueAt time.Time
	if err := pool.QueryRow(context.Background(), `SELECT due_at FROM sync_jobs j JOIN orders o ON o.id = j.order_id WHERE o.order_id = $1`, digitalOrder.OrderID).Scan(&digitalDueAt); err != nil {
		t.Fatal(err)
	}
	if digitalDueAt.After(time.Now().Add(100 * time.Millisecond)) {
		t.Fatalf("configured non-hardware SKU was delayed: due_at=%s", digitalDueAt)
	}
}

func TestHardwareOverridePersistsThroughPaymentFirstAndSAPSync(t *testing.T) {
	pool := integrationPool(t)
	recorder := newSAPRecorder("")
	sapServer := httptest.NewServer(recorder)
	defer sapServer.Close()

	cfg := integrationConfig(sapServer.URL)
	service := integrationService(pool, cfg)
	worker := ordersync.NewWorker(repositories.NewSyncJobRepository(pool), sap.NewClient(cfg), cfg, integrationLogger())
	worker.Start()
	defer stopWorker(t, worker)

	forcedHardware := true
	hardwareOrder := shopPayload("shop-override-hardware", "ORD-OVERRIDE-HARDWARE", "NUKI-SMART-HOSTING")
	hardwareOrder.Items[0].IsHardware = &forcedHardware
	scheduledAt := time.Now()
	if _, err := service.paymentService.Process(context.Background(), paymentPayload("pay-override-hardware", hardwareOrder.OrderID)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.shopService.Process(context.Background(), hardwareOrder); err != nil {
		t.Fatal(err)
	}

	var storedHardware bool
	if err := pool.QueryRow(context.Background(), `SELECT i.is_hardware FROM order_items i JOIN orders o ON o.id = i.order_id WHERE o.order_id = $1`, hardwareOrder.OrderID).Scan(&storedHardware); err != nil {
		t.Fatal(err)
	}
	if !storedHardware {
		t.Fatal("explicit hardware override was not persisted")
	}
	var hardwareDueAt time.Time
	if err := pool.QueryRow(context.Background(), `SELECT j.due_at FROM sync_jobs j JOIN orders o ON o.id = j.order_id WHERE o.order_id = $1`, hardwareOrder.OrderID).Scan(&hardwareDueAt); err != nil {
		t.Fatal(err)
	}
	if hardwareDueAt.Before(scheduledAt.Add(time.Second - 100*time.Millisecond)) {
		t.Fatalf("explicit hardware override was not scheduled with a delay: due_at=%s", hardwareDueAt)
	}
	waitFor(t, 2*time.Second, func() bool { return recorder.count(hardwareOrder.OrderID) == 1 })
	job, ok := recorder.payload(hardwareOrder.OrderID)
	if !ok || len(job.Items) != 1 || job.Items[0].IsHardware == nil || !*job.Items[0].IsHardware {
		t.Fatalf("SAP payload lost explicit hardware override: %#v", job.Items)
	}

	forcedDigital := false
	digitalOrder := shopPayload("shop-override-digital", "ORD-OVERRIDE-DIGITAL", "NUKI-SL3")
	digitalOrder.Items[0].IsHardware = &forcedDigital
	if _, err := service.shopService.Process(context.Background(), digitalOrder); err != nil {
		t.Fatal(err)
	}
	if _, err := service.paymentService.Process(context.Background(), paymentPayload("pay-override-digital", digitalOrder.OrderID)); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(context.Background(), `SELECT i.is_hardware FROM order_items i JOIN orders o ON o.id = i.order_id WHERE o.order_id = $1`, digitalOrder.OrderID).Scan(&storedHardware); err != nil {
		t.Fatal(err)
	}
	if storedHardware {
		t.Fatal("explicit non-hardware override was not persisted")
	}
	waitFor(t, 2*time.Second, func() bool { return recorder.count(digitalOrder.OrderID) == 1 })
	job, ok = recorder.payload(digitalOrder.OrderID)
	if !ok || len(job.Items) != 1 || job.Items[0].IsHardware == nil || *job.Items[0].IsHardware {
		t.Fatalf("SAP payload lost explicit non-hardware override: %#v", job.Items)
	}
}

func integrationConfig(sapURL string) config.Config {
	return config.Config{
		SAPAPIURL:                sapURL,
		HardwareSyncDelaySeconds: 1,
		SAPTimeoutMS:             200,
		SAPMaxAttempts:           3,
		SAPRecoveryWindowSeconds: 900,
	}
}

type integrationServices struct {
	orders         *orders.Service
	shopService    *shop.Service
	paymentService *payment.Service
}

func integrationService(pool *pgxpool.Pool, cfg config.Config) *integrationServices {
	transaction := db.NewTransactionRunner(pool)
	return &integrationServices{
		orders:         orders.NewService(repositories.NewPoolOrderRepository(pool)),
		shopService:    shop.NewService(transaction, cfg),
		paymentService: payment.NewService(transaction, cfg),
	}
}

func shopPayload(eventID, orderID, sku string) shop.Webhook {
	return shop.Webhook{
		EventID:       eventID,
		OrderID:       orderID,
		CustomerEmail: "integration@example.com",
		Items:         []contracts.OrderItem{{SKU: sku, Quantity: 1, Price: 39}},
	}
}

func paymentPayload(eventID, orderID string) payment.Webhook {
	return paymentPayloadWithStatus(eventID, orderID, "COMPLETED")
}

func paymentPayloadWithStatus(eventID, orderID, status string) payment.Webhook {
	return payment.Webhook{
		EventID:          eventID,
		ReferenceOrderID: orderID,
		PaymentStatus:    contracts.PaymentStatus(status),
		Timestamp:        time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}

func stopWorker(t *testing.T, worker *ordersync.Worker) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := worker.Stop(ctx); err != nil {
		t.Errorf("stop worker: %v", err)
	}
}
