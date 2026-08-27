package contracts

import (
	"context"
	"time"
)

type OrderRepository interface {
	Create(context.Context, string, string) (int64, error)
	Find(context.Context, string) (*StoredOrder, error)
	FindID(context.Context, string) (int64, error)
	FindStatus(context.Context, string) (*OrderStatus, error)
	ListItems(context.Context, int64) ([]OrderItem, error)
	MarkPaid(context.Context, int64, string) error
	AddItems(context.Context, int64, []OrderItem) error
	ScheduleSync(context.Context, int64, int) error
	Cancel(context.Context, int64) error
}

type PaymentRepository interface {
	Upsert(context.Context, string, PaymentStatus, *int64, *time.Time) (PaymentState, error)
	Find(context.Context, string) (*PaymentState, error)
	LinkOrder(context.Context, string, int64) error
	Cancel(context.Context, string, *int64) (PaymentState, error)
}

type SKUClassifier interface {
	HasHardware(context.Context, []string) (bool, error)
}

type EventRepository interface {
	Record(context.Context, string, EventType, any) (bool, error)
	FindPayload(context.Context, EventType, string) ([]byte, error)
	MarkProcessed(context.Context, EventType, string) error
	MarkPaymentEventsProcessed(context.Context, string) error
}

type TransactionRepositories struct {
	Orders        OrderRepository
	SKUClassifier SKUClassifier
	Events        EventRepository
	Payments      PaymentRepository
}

type TransactionRunner interface {
	Run(context.Context, func(TransactionRepositories) (WebhookResult, error)) (WebhookResult, error)
}
