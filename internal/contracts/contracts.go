package contracts

import (
	"errors"
	"time"
)

var (
	ErrPaymentFinalized       = errors.New("payment is finalized")
	ErrOrderPayloadConflict   = errors.New("order payload conflicts with existing order")
	ErrPaymentPayloadConflict = errors.New("payment payload conflicts with existing payment")
	ErrDigitalCancellation    = errors.New("digital-only orders cannot be cancelled")
	ErrOrderCannotCancel      = errors.New("order cannot be cancelled")
)

type EventType string

const (
	EventTypeShop    EventType = "SHOP"
	EventTypePayment EventType = "PAYMENT"
)

type PaymentStatus string

const (
	PaymentStatusPending   PaymentStatus = "PENDING"
	PaymentStatusCompleted PaymentStatus = "COMPLETED"
	PaymentStatusFailed    PaymentStatus = "FAILED"
	PaymentStatusCancelled PaymentStatus = "CANCELLED"
)

type SyncStatus string

const (
	SyncStatusPending    SyncStatus = "PENDING"
	SyncStatusProcessing SyncStatus = "PROCESSING"
	SyncStatusSynced     SyncStatus = "SYNCED"
	SyncStatusWaiting    SyncStatus = "WAITING"
	SyncStatusDead       SyncStatus = "DEAD"
	SyncStatusCancelled  SyncStatus = "CANCELLED"
)

type OrderState string

const (
	OrderStatePending   OrderState = "PENDING"
	OrderStatePaid      OrderState = "PAID"
	OrderStateCancelled OrderState = "CANCELLED"
)

type OrderItem struct {
	SKU        string  `json:"sku"`
	Quantity   int     `json:"quantity"`
	Price      float64 `json:"price"`
	IsHardware *bool   `json:"isHardware,omitempty"`
}

// StoredOrder is the immutable shop payload persisted for an order.
type StoredOrder struct {
	ID            int64
	CustomerEmail string
	Items         []OrderItem
}

type WebhookResult struct {
	Duplicate     bool          `json:"-"`
	Message       string        `json:"message"`
	EventID       string        `json:"event_id,omitempty"`
	OrderID       string        `json:"order_id,omitempty"`
	PaymentStatus PaymentStatus `json:"payment_status,omitempty"`
	OrderStatus   OrderState    `json:"order_payment_status,omitempty"`
	SyncStatus    SyncStatus    `json:"sync_status,omitempty"`
}

type ValidationIssue struct {
	Code    string `json:"code"`
	Path    []any  `json:"path"`
	Message string `json:"message"`
}

type OrderStatus struct {
	OrderID       string        `json:"order_id"`
	CustomerEmail string        `json:"customer_email"`
	Status        OrderState    `json:"status"`
	PaymentStatus PaymentStatus `json:"payment_status"`
	PaidAt        *string       `json:"paid_at"`
	SyncStatus    SyncStatus    `json:"sync_status"`
	DueAt         *string       `json:"due_at"`
	Attempts      *int          `json:"attempts"`
	LastError     *string       `json:"last_error"`
	SAPInternalID *string       `json:"sap_internal_id"`
}

type PaymentState struct {
	ID               int64
	ReferenceOrderID string
	OrderID          *int64
	Status           PaymentStatus
	PaidAt           *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}
