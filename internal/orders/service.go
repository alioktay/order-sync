package orders

import (
	"context"

	"order-sync/internal/config"
	"order-sync/internal/contracts"
)

type Service struct {
	orders contracts.OrderRepository
}

func NewService(orders contracts.OrderRepository) *Service {
	return &Service{orders: orders}
}

func (s *Service) GetOrderStatus(ctx context.Context, orderID string) (*OrderStatus, error) {
	return s.orders.FindStatus(ctx, orderID)
}

func MarkPaidAndSchedule(ctx context.Context, repo contracts.OrderRepository, classifier contracts.SKUClassifier, cfg config.Config, id int64, items []contracts.OrderItem, paidAt string) error {
	hasHardware := false
	skus := itemSKUs(items)
	if len(skus) > 0 {
		classifiedHardware, err := classifier.HasHardware(ctx, skus)
		if err != nil {
			return err
		}
		hasHardware = classifiedHardware
	}
	for _, item := range items {
		if item.IsHardware != nil && *item.IsHardware {
			hasHardware = true
			break
		}
	}
	if err := repo.MarkPaid(ctx, id, paidAt); err != nil {
		return err
	}
	delay := 0
	if hasHardware {
		delay = cfg.HardwareSyncDelaySeconds
	}
	return repo.ScheduleSync(ctx, id, delay)
}

func itemSKUs(items []contracts.OrderItem) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		if item.IsHardware == nil {
			result = append(result, item.SKU)
		}
	}
	return result
}
