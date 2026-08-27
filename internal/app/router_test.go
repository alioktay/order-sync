package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"order-sync/internal/config"
	"order-sync/internal/contracts"
	"order-sync/internal/payment"
	"order-sync/internal/shop"
	"strings"
	"testing"
)

type fakeAPI struct {
	shopResult    contracts.WebhookResult
	shopErr       error
	paymentResult contracts.WebhookResult
	paymentErr    error
	status        *contracts.OrderStatus
	statusErr     error
}

func (f fakeAPI) Process(context.Context, shop.Webhook) (contracts.WebhookResult, error) {
	return f.shopResult, f.shopErr
}
func (f fakeAPI) GetOrderStatus(context.Context, string) (*contracts.OrderStatus, error) {
	return f.status, f.statusErr
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testRouter(api fakeAPI, health HealthCheck, secret string) *httptest.Server {
	return httptest.NewServer(NewRouter(RouterParams{
		Config:  config.Config{WebhookSecret: secret},
		Shop:    api,
		Payment: paymentAPI{api},
		Orders:  orderAPI{api},
		Logger:  testLogger(),
		Health:  health,
	}))
}

type paymentAPI struct{ fakeAPI }

func (f paymentAPI) Process(_ context.Context, _ payment.Webhook) (contracts.WebhookResult, error) {
	return f.paymentResult, f.paymentErr
}

type orderAPI struct{ fakeAPI }

func (f orderAPI) GetOrderStatus(_ context.Context, _ string) (*contracts.OrderStatus, error) {
	return f.status, f.statusErr
}

func request(t *testing.T, client *http.Client, method, url, body, secret string) *http.Response {
	return requestWithContentType(t, client, method, url, body, secret, "application/json")
}

func requestWithContentType(t *testing.T, client *http.Client, method, url, body, secret, contentType string) *http.Response {
	t.Helper()
	if contentType == "" {
		contentType = "application/json"
	}
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)

	if secret != "" {
		req.Header.Set("X-Webhook-Secret", secret)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestRouterAuthenticationAndValidation(t *testing.T) {
	server := testRouter(fakeAPI{}, func(context.Context) error { return nil }, "secret")
	defer server.Close()

	response := request(t, server.Client(), http.MethodPost, server.URL+"/api/webhooks/shop", `{}`, "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status: got %d", response.StatusCode)
	}
	response = request(t, server.Client(), http.MethodPost, server.URL+"/api/webhooks/payment", `{}`, "secret")
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("validation status: got %d", response.StatusCode)
	}
}

func TestRouterHealth(t *testing.T) {
	tests := []struct {
		name   string
		health HealthCheck
		status int
	}{
		{name: "healthy", health: func(context.Context) error { return nil }, status: http.StatusOK},
		{name: "unhealthy", health: func(context.Context) error { return errors.New("database unavailable") }, status: http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := testRouter(fakeAPI{}, test.health, "")
			defer server.Close()
			response := request(t, server.Client(), http.MethodGet, server.URL+"/health", "", "")
			if response.StatusCode != test.status {
				t.Fatalf("status: got %d, want %d", response.StatusCode, test.status)
			}
		})
	}
}

func TestRouterRequestID(t *testing.T) {
	server := testRouter(fakeAPI{}, func(context.Context) error { return nil }, "")
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Request-ID", "request-from-client")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if got := response.Header.Get("X-Request-ID"); got != "request-from-client" {
		t.Fatalf("request ID: got %q", got)
	}
}

func TestShopWebhookResponses(t *testing.T) {
	valid := `{"event_id":"evt-1","order_id":"order-1","customer_email":"buyer@example.com","items":[{"sku":"digital","quantity":1,"price":9.99}]}`
	tests := []struct {
		name        string
		api         fakeAPI
		body        string
		contentType string
		status      int
	}{
		{name: "created", api: fakeAPI{shopResult: contracts.WebhookResult{Message: "Order stored"}}, body: valid, status: http.StatusCreated},
		{name: "duplicate", api: fakeAPI{shopResult: contracts.WebhookResult{Duplicate: true, Message: "Shop event already processed", EventID: "evt-1", OrderID: "order-1", PaymentStatus: contracts.PaymentStatusPending, OrderStatus: contracts.OrderStatePending, SyncStatus: contracts.SyncStatusPending}}, body: valid, status: http.StatusOK},
		{name: "service error", api: fakeAPI{shopErr: errors.New("write failed")}, body: valid, status: http.StatusInternalServerError},
		{name: "payload conflict", api: fakeAPI{shopErr: fmt.Errorf("run transaction work: %w", contracts.ErrOrderPayloadConflict)}, body: valid, status: http.StatusConflict},
		{name: "invalid JSON", body: `{`, status: http.StatusBadRequest},
		{name: "unknown field", body: `{"unknown":true}`, status: http.StatusBadRequest},
		{name: "multiple values", body: valid + `{}`, status: http.StatusBadRequest},
		{name: "oversized body", body: valid + strings.Repeat(" ", maxWebhookBodySize), status: http.StatusBadRequest},
		{name: "wrong content type", body: valid, contentType: "text/plain", status: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := testRouter(test.api, func(context.Context) error { return nil }, "")
			defer server.Close()
			response := requestWithContentType(t, server.Client(), http.MethodPost, server.URL+"/api/webhooks/shop", test.body, "", test.contentType)
			if response.StatusCode != test.status {
				t.Fatalf("status: got %d, want %d", response.StatusCode, test.status)
			}
			if test.name == "duplicate" {
				var body map[string]any
				if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if _, ok := body["duplicate"]; ok {
					t.Fatal("duplicate response exposed duplicate field")
				}
				if body["message"] != "Shop event already processed" || body["event_id"] != "evt-1" || body["order_id"] != "order-1" || body["payment_status"] != "PENDING" || body["order_payment_status"] != "PENDING" || body["sync_status"] != "PENDING" {
					t.Fatalf("duplicate response body = %#v", body)
				}
			}
			if test.name == "payload conflict" {
				var body map[string]any
				if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body["error"] != contracts.ErrOrderPayloadConflict.Error() {
					t.Fatalf("payload conflict body = %#v", body)
				}
			}
		})
	}
}

func TestPaymentWebhookResponses(t *testing.T) {
	valid := `{"event_id":"evt-2","reference_order_id":"order-1","payment_status":"completed","timestamp":"2026-08-26T10:00:00Z"}`
	tests := []struct {
		name        string
		api         fakeAPI
		body        string
		contentType string
		status      int
	}{
		{name: "awaiting order", api: fakeAPI{paymentResult: contracts.WebhookResult{Message: "Payment stored and awaiting shop order"}}, body: valid, status: http.StatusAccepted},
		{name: "processed", api: fakeAPI{paymentResult: contracts.WebhookResult{Message: "Payment processed"}}, body: valid, status: http.StatusOK},
		{name: "duplicate", api: fakeAPI{paymentResult: contracts.WebhookResult{Duplicate: true, Message: "Payment event already processed", EventID: "evt-2", OrderID: "order-1", PaymentStatus: contracts.PaymentStatusCompleted, OrderStatus: contracts.OrderStatePaid, SyncStatus: contracts.SyncStatusSynced}}, body: valid, status: http.StatusOK},
		{name: "service error", api: fakeAPI{paymentErr: errors.New("write failed")}, body: valid, status: http.StatusInternalServerError},
		{name: "finalized payment", api: fakeAPI{paymentErr: contracts.ErrPaymentFinalized}, body: valid, status: http.StatusConflict},
		{name: "payload conflict", api: fakeAPI{paymentErr: fmt.Errorf("run transaction work: %w", contracts.ErrPaymentPayloadConflict)}, body: valid, status: http.StatusConflict},
		{name: "malformed JSON", body: `{`, status: http.StatusBadRequest},
		{name: "unknown field", body: `{"unknown":true}`, status: http.StatusBadRequest},
		{name: "multiple values", body: valid + `{}`, status: http.StatusBadRequest},
		{name: "oversized body", body: valid + strings.Repeat(" ", maxWebhookBodySize), status: http.StatusBadRequest},
		{name: "wrong content type", body: valid, contentType: "text/plain", status: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := testRouter(test.api, func(context.Context) error { return nil }, "")
			defer server.Close()
			response := requestWithContentType(t, server.Client(), http.MethodPost, server.URL+"/api/webhooks/payment", test.body, "", test.contentType)
			if response.StatusCode != test.status {
				t.Fatalf("status: got %d, want %d", response.StatusCode, test.status)
			}
			if test.name == "duplicate" {
				var body map[string]any
				if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if _, ok := body["duplicate"]; ok {
					t.Fatal("duplicate response exposed duplicate field")
				}
				if body["message"] != "Payment event already processed" || body["event_id"] != "evt-2" || body["order_id"] != "order-1" || body["payment_status"] != "COMPLETED" || body["order_payment_status"] != "PAID" || body["sync_status"] != "SYNCED" {
					t.Fatalf("duplicate response body = %#v", body)
				}
			}
			if test.name == "payload conflict" {
				var body map[string]any
				if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body["error"] != contracts.ErrPaymentPayloadConflict.Error() {
					t.Fatalf("payload conflict body = %#v", body)
				}
			}
		})
	}
}

func TestOrderStatusResponses(t *testing.T) {
	tests := []struct {
		name   string
		api    fakeAPI
		status int
	}{
		{name: "found", api: fakeAPI{status: &contracts.OrderStatus{OrderID: "order-1"}}, status: http.StatusOK},
		{name: "not found", api: fakeAPI{}, status: http.StatusNotFound},
		{name: "service error", api: fakeAPI{statusErr: errors.New("read failed")}, status: http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := testRouter(test.api, func(context.Context) error { return nil }, "")
			defer server.Close()
			response := request(t, server.Client(), http.MethodGet, server.URL+"/api/orders/order-1", "", "")
			if response.StatusCode != test.status {
				t.Fatalf("status: got %d, want %d", response.StatusCode, test.status)
			}
		})
	}
}
