# API contract

Base URL: `http://localhost:3000`. Webhook requests use JSON. If `WEBHOOK_SECRET` is non-empty, both webhook routes require the exact `X-Webhook-Secret` header; otherwise authentication is disabled. The service preserves a supplied `X-Request-ID` or generates one and returns it in the response header.

## Endpoints

| Method | Path | Success | Purpose |
|---|---|---|---|
| GET | `/health` | 200 | PostgreSQL connectivity check |
| POST | `/api/webhooks/shop` | 200 or 201 | Process shop order; replayed events return 200 |
| POST | `/api/webhooks/payment` | 200 or 202 | Process payment state |
| GET | `/api/orders/{orderId}` | 200 | Read current order status |

### Shop webhook

```json
{"event_id":"evt_1","order_id":"ORD-1","customer_email":"alioktay@gmail.com","items":[{"sku":"NUKI-SL3","quantity":1,"price":169.0,"isHardware":true}]}
```

Required fields are `event_id`, `order_id`, `customer_email`, and at least one item. Email must be an exact parseable address. Each item requires a non-empty `sku`, positive integer `quantity`, and non-negative `price`; `isHardware` is optional. Normalization trims the event/order/email/SKU string fields.

New orders return 201. The business identity is `order_id`: a new event ID with the same customer email and items is an accepted replay and returns 200 without creating items or sync jobs. Repeating the same shop event ID with that same payload also returns 200 with `Shop event already processed`. Both accepted replay responses include the current payment and synchronization status fields. Customer email, SKU, quantity, price, and `isHardware` override are immutable; any difference for an existing order, including a repeated event ID with changed order data, returns 409. Event IDs are retained as type-scoped deduplication metadata, so the same ID can be used once by each webhook type. Decode/validation errors return 400 with `{"error":"Invalid request","issues":[{"code":...,"path":...,"message":...}]}`.

### Payment webhook

```json
{"event_id":"evt_pay_1","reference_order_id":"ORD-1","payment_status":"COMPLETED","timestamp":"2026-08-19T09:30:00Z"}
```

Required fields are `event_id`, `reference_order_id`, `payment_status`, and an RFC3339 `timestamp`. Allowed payment statuses are `PENDING`, `COMPLETED`, `FAILED`, and `CANCELLED`. The business identity is `reference_order_id`; event ID and timestamp changes alone do not conflict. Reusing an event ID requires the same normalized payload; any changed payment field returns 409. `PENDING` and `FAILED` may be updated until a payment becomes `COMPLETED` or `CANCELLED` when sent with a new event ID. Repeating that same terminal status is a no-op; any different status after terminalization returns 409. An event for an order not yet received is accepted with 202 and is reconciled later. Normal processing and accepted replays return 200; repeated event IDs return the current applicable payment, order, and synchronization status fields.

### Responses

`WebhookResult` contains `message`, optional `event_id`, `order_id`, `payment_status`, `order_payment_status`, and `sync_status`. The read endpoint returns `order_id`, `customer_email`, `status`, `payment_status`, `paid_at`, `sync_status`, `due_at`, `attempts`, `last_error`, and `sap_internal_id`. `/health` returns `{"status":"ok"}`; failure returns `{"error":"Internal server error"}` with 500. Unknown orders return `{"error":"Order not found"}` with 404. Database or unexpected processing errors return the generic 500 response. Missing/incorrect webhook secrets return `{"error":"Unauthorized"}` with 401.

## SAP contract

The client POSTs an order-details payload to `SAP_API_URL` containing the order ID, customer email, paid time, and item array (`sku`, `quantity`, `price`, `isHardware`). Synchronization metadata such as the job ID, status, attempts, due time, and waiting time is not sent. It sends the order identity in the `idempotency-key` header. The response must be valid JSON with `status: "success"` and a non-empty `sap_internal_id`. HTTP 429/500/502/503/504, timeouts, DNS, network errors, and unexpected EOF are retryable. Other HTTP statuses, invalid JSON, missing `sap_internal_id`, and explicit cancellation are non-retryable. See [domain retry semantics](domain.md#retry-semantics) for backoff and recovery-window behavior.
