package orders

import (
	"context"
	"order-sync/internal/contracts"
)

type OrderRepository = contracts.OrderRepository
type API interface {
	GetOrderStatus(context.Context, string) (*contracts.OrderStatus, error)
}
