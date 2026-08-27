package sync

import (
	"context"
	"order-sync/internal/contracts"
	"time"
)

type Job struct {
	ID           int64                `json:"-"`
	Status       contracts.SyncStatus `json:"-"`
	Attempts     int                  `json:"-"`
	DueAt        *time.Time           `json:"-"`
	WaitingSince *time.Time           `json:"-"`
	OrderDetails
}

type OrderDetails struct {
	OrderID       string                `json:"order_id"`
	CustomerEmail string                `json:"customer_email"`
	PaidAt        string                `json:"paid_at"`
	Items         []contracts.OrderItem `json:"items"`
}

type JobRepository interface {
	ClaimDue(context.Context) (*Job, error)
	NextWake(context.Context) (*time.Time, error)
	Watch(context.Context) (<-chan struct{}, <-chan error, error)
	MarkSynced(context.Context, int64, string) error
	MarkFailed(context.Context, int64, contracts.SyncStatus, time.Time, string) error
}
type SapClient interface {
	SyncOrder(context.Context, string, OrderDetails) (string, error)
}
