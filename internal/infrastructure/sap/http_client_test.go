package sap

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	stdsync "sync"
	"sync/atomic"
	"testing"
	"time"

	"order-sync/internal/config"
	"order-sync/internal/contracts"
	gosync "order-sync/internal/sync"
)

func TestSyncOrderSendsOrderAndIdempotencyKey(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("got method %s, want POST", r.Method)
		}
		if got := r.Header.Get("idempotency-key"); got != "ORD-1" {
			t.Errorf("got idempotency key %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","sap_internal_id":"SAP-1"}`))
	}))
	defer server.Close()

	client := NewClient(config.Config{SAPAPIURL: server.URL, SAPTimeoutMS: 500})
	isHardware := false
	order := gosync.OrderDetails{OrderID: "ORD-1", CustomerEmail: "hello@example.com", PaidAt: "2026-08-19T09:30:00Z", Items: []contracts.OrderItem{{SKU: "NUKI-SL3", Quantity: 1, Price: 169, IsHardware: &isHardware}}}
	sapID, err := client.SyncOrder(context.Background(), order.OrderID, order)
	if err != nil {
		t.Fatal(err)
	}
	if sapID != "SAP-1" {
		t.Fatalf("unexpected result sapID=%q payload=%#v", sapID, received)
	}
	if got, ok := received["order_id"].(string); !ok || got != order.OrderID {
		t.Fatalf("unexpected order_id payload=%#v", received)
	}
	if got, ok := received["customer_email"].(string); !ok || got != order.CustomerEmail {
		t.Fatalf("unexpected customer_email payload=%#v", received)
	}
	if got, ok := received["paid_at"].(string); !ok || got != order.PaidAt {
		t.Fatalf("unexpected paid_at payload=%#v", received)
	}
	if _, ok := received["items"]; !ok {
		t.Fatalf("items missing from payload=%#v", received)
	}
	for _, field := range []string{"id", "status", "attempts", "due_at", "waiting_since"} {
		if _, ok := received[field]; ok {
			t.Fatalf("job field %q leaked into payload=%#v", field, received)
		}
	}
}

func TestSyncOrderCoalescesConcurrentCallsWithSameIdempotencyKey(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var requests atomic.Int32
	var startedOnce stdsync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		startedOnce.Do(func() { close(started) })
		<-release
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","sap_internal_id":"SAP-1"}`))
	}))
	defer server.Close()

	client := NewClient(config.Config{SAPAPIURL: server.URL, SAPTimeoutMS: 500})
	order := gosync.OrderDetails{OrderID: "ORD-CONCURRENT"}
	results := make(chan string, 2)
	errorsCh := make(chan error, 2)
	go func() {
		id, err := client.SyncOrder(context.Background(), "KEY-1", order)
		results <- id
		errorsCh <- err
	}()
	<-started
	go func() {
		id, err := client.SyncOrder(context.Background(), "KEY-1", order)
		results <- id
		errorsCh <- err
	}()
	close(release)

	for range 2 {
		if err := <-errorsCh; err != nil {
			t.Fatal(err)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("HTTP requests = %d, want 1", got)
	}
	for range 2 {
		if got := <-results; got != "SAP-1" {
			t.Fatalf("SAP ID = %q, want SAP-1", got)
		}
	}
}

func TestSyncOrderRejectsNonSuccessBusinessStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"failed","sap_internal_id":"SAP-1"}`))
	}))
	defer server.Close()

	client := NewClient(config.Config{SAPAPIURL: server.URL, SAPTimeoutMS: 500})
	_, err := client.SyncOrder(context.Background(), "ORD-BUSINESS-ERROR", gosync.OrderDetails{OrderID: "ORD-BUSINESS-ERROR"})
	if err == nil {
		t.Fatal("expected non-success SAP business status to fail")
	}
	if !strings.Contains(err.Error(), `status was "failed"`) {
		t.Fatalf("error = %v, want business status detail", err)
	}
	var classified interface{ Retryable() bool }
	if !errors.As(err, &classified) {
		t.Fatalf("business status error has no retryability classification: %v", err)
	}
	if classified.Retryable() {
		t.Fatalf("business status rejection was retryable; want non-retryable DEAD policy")
	}
}

func TestSyncOrderRejectsSAPError(t *testing.T) {
	for _, test := range []struct {
		status    int
		retryable bool
	}{
		{http.StatusBadRequest, false},
		{http.StatusUnauthorized, false},
		{http.StatusNotFound, false},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusGatewayTimeout, true},
	} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "SAP error", test.status)
			}))
			defer server.Close()

			client := NewClient(config.Config{SAPAPIURL: server.URL, SAPTimeoutMS: 500})
			_, err := client.SyncOrder(context.Background(), "ORD-2", gosync.OrderDetails{OrderID: "ORD-2"})
			if err == nil {
				t.Fatalf("expected HTTP %d to fail", test.status)
			}
			var classified interface{ Retryable() bool }
			if !errors.As(err, &classified) {
				t.Fatalf("HTTP %d error has no retryability classification: %v", test.status, err)
			}
			if got := classified.Retryable(); got != test.retryable {
				t.Fatalf("HTTP %d retryable = %v, want %v", test.status, got, test.retryable)
			}
		})
	}
}

func TestSyncOrderParsesRetryAfter(t *testing.T) {
	future := time.Now().Add(5 * time.Second).UTC().Format(http.TimeFormat)
	for _, test := range []struct {
		name   string
		header string
		want   time.Duration
		valid  bool
	}{
		{name: "numeric", header: "7", want: 7 * time.Second, valid: true},
		{name: "http-date", header: future, want: 5 * time.Second, valid: true},
		{name: "invalid", header: "later", valid: false},
		{name: "absent", valid: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if test.header != "" {
					w.Header().Set("Retry-After", test.header)
				}
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer server.Close()

			client := NewClient(config.Config{SAPAPIURL: server.URL, SAPTimeoutMS: 500})
			_, err := client.SyncOrder(context.Background(), "ORD-429", gosync.OrderDetails{OrderID: "ORD-429"})
			if err == nil {
				t.Fatal("expected HTTP 429 to fail")
			}
			var retryAfter interface{ RetryAfter() (time.Duration, bool) }
			if !errors.As(err, &retryAfter) {
				t.Fatalf("error has no Retry-After metadata: %v", err)
			}
			got, gotValid := retryAfter.RetryAfter()
			if gotValid != test.valid {
				t.Fatalf("Retry-After valid = %v, want %v", gotValid, test.valid)
			}
			if test.valid && test.name == "numeric" && got != test.want {
				t.Fatalf("Retry-After = %s, want %s", got, test.want)
			}
			if test.valid && test.name == "http-date" && (got < 4*time.Second || got > 5*time.Second) {
				t.Fatalf("HTTP-date Retry-After = %s, want about %s", got, test.want)
			}
		})
	}
}

func TestSyncOrderRetriesConnectionFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	serverURL := server.URL
	server.Close()

	client := NewClient(config.Config{SAPAPIURL: serverURL, SAPTimeoutMS: 500})
	_, err := client.SyncOrder(context.Background(), "ORD-4", gosync.OrderDetails{OrderID: "ORD-4"})
	if err == nil {
		t.Fatal("expected connection failure")
	}
	var classified interface{ Retryable() bool }
	if !errors.As(err, &classified) || !classified.Retryable() {
		t.Fatalf("connection failure was not classified as retryable: %v", err)
	}
}

func TestSyncOrderTimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()

	client := NewClient(config.Config{SAPAPIURL: server.URL, SAPTimeoutMS: 20})
	started := time.Now()
	_, err := client.SyncOrder(context.Background(), "ORD-3", gosync.OrderDetails{OrderID: "ORD-3"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("timeout took too long: %s", elapsed)
	}
	var classified interface{ Retryable() bool }
	if !errors.As(err, &classified) {
		t.Fatalf("timeout has no retryability classification: %v", err)
	}
	if !classified.Retryable() {
		t.Fatalf("timeout was not classified as retryable: %v", err)
	}
}
