package shop

import (
	"context"
	"fmt"
	"time"

	"order-sync/internal/config"
	"order-sync/internal/contracts"
	"order-sync/internal/orders"
)

type API interface {
	Process(context.Context, Webhook) (contracts.WebhookResult, error)
}

type Service struct {
	transaction contracts.TransactionRunner
	config      config.Config
}

func NewService(transaction contracts.TransactionRunner, cfg config.Config) *Service {
	return &Service{transaction: transaction, config: cfg}
}

func (s *Service) Process(ctx context.Context, payload Webhook) (contracts.WebhookResult, error) {
	payload = NormalizeShopWebhook(payload)
	return s.transaction.Run(ctx, func(repos contracts.TransactionRepositories) (contracts.WebhookResult, error) {
		return processShop(ctx, repos, payload, s.config)
	})
}

func processShop(ctx context.Context, repos contracts.TransactionRepositories, payload Webhook, cfg config.Config) (contracts.WebhookResult, error) {
	newEvent, err := repos.Events.Record(ctx, payload.EventID, contracts.EventTypeShop, payload)
	if err != nil {
		return contracts.WebhookResult{}, fmt.Errorf("record shop event: %w", err)
	}
	if !newEvent {
		existing, err := repos.Orders.Find(ctx, payload.OrderID)
		if err != nil {
			return contracts.WebhookResult{}, fmt.Errorf("load existing order %q: %w", payload.OrderID, err)
		}
		if existing == nil || !sameOrderPayload(existing, payload) {
			return contracts.WebhookResult{}, contracts.ErrOrderPayloadConflict
		}
		result, err := shopResult(ctx, repos, payload.EventID, payload.OrderID, "Shop event already processed")
		if err != nil {
			return contracts.WebhookResult{}, err
		}
		result.Duplicate = true
		return result, nil
	}
	orderID, err := repos.Orders.Create(ctx, payload.OrderID, payload.CustomerEmail)
	if err != nil {
		return contracts.WebhookResult{}, fmt.Errorf("create order %q: %w", payload.OrderID, err)
	}
	if orderID == 0 {
		existing, err := repos.Orders.Find(ctx, payload.OrderID)
		if err != nil {
			return contracts.WebhookResult{}, fmt.Errorf("load existing order %q: %w", payload.OrderID, err)
		}
		if existing == nil || !sameOrderPayload(existing, payload) {
			return contracts.WebhookResult{}, contracts.ErrOrderPayloadConflict
		}
		if err := repos.Events.MarkProcessed(ctx, contracts.EventTypeShop, payload.EventID); err != nil {
			return contracts.WebhookResult{}, fmt.Errorf("mark shop event %q processed: %w", payload.EventID, err)
		}
		result, err := shopResult(ctx, repos, payload.EventID, payload.OrderID, "Shop order already stored")
		if err != nil {
			return contracts.WebhookResult{}, err
		}
		result.Duplicate = true
		return result, nil
	}
	if err := repos.Orders.AddItems(ctx, orderID, payload.Items); err != nil {
		return contracts.WebhookResult{}, fmt.Errorf("store items for order %d: %w", orderID, err)
	}
	if err := reconcilePayment(ctx, repos, payload, orderID, cfg); err != nil {
		return contracts.WebhookResult{}, err
	}
	if err := repos.Events.MarkProcessed(ctx, contracts.EventTypeShop, payload.EventID); err != nil {
		return contracts.WebhookResult{}, fmt.Errorf("mark shop event %q processed: %w", payload.EventID, err)
	}
	return shopResult(ctx, repos, payload.EventID, payload.OrderID, "Order stored")
}

func sameOrderPayload(existing *contracts.StoredOrder, payload Webhook) bool {
	if existing.CustomerEmail != payload.CustomerEmail || len(existing.Items) != len(payload.Items) {
		return false
	}
	matched := make([]bool, len(payload.Items))
	for _, item := range existing.Items {
		found := false
		for i, incoming := range payload.Items {
			if !matched[i] && item.SKU == incoming.SKU && item.Quantity == incoming.Quantity && item.Price == incoming.Price && sameHardwareOverride(item.IsHardware, incoming.IsHardware) {
				matched[i] = true
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func sameHardwareOverride(left, right *bool) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func shopResult(ctx context.Context, repos contracts.TransactionRepositories, eventID, orderID, message string) (contracts.WebhookResult, error) {
	result := contracts.WebhookResult{Message: message, EventID: eventID, OrderID: orderID}
	status, err := repos.Orders.FindStatus(ctx, orderID)
	if err != nil {
		return contracts.WebhookResult{}, fmt.Errorf("load order status for %q: %w", orderID, err)
	}
	if status != nil {
		result.PaymentStatus = status.PaymentStatus
		result.OrderStatus = status.Status
		result.SyncStatus = status.SyncStatus
	}
	return result, nil
}

func reconcilePayment(ctx context.Context, repos contracts.TransactionRepositories, payload Webhook, orderID int64, cfg config.Config) error {
	paymentState, err := repos.Payments.Find(ctx, payload.OrderID)
	if err != nil {
		return fmt.Errorf("find payment for order %q: %w", payload.OrderID, err)
	}
	if paymentState == nil {
		return nil
	}
	if paymentState.OrderID == nil {
		if err := repos.Payments.LinkOrder(ctx, payload.OrderID, orderID); err != nil {
			return fmt.Errorf("link payment to order %d: %w", orderID, err)
		}
	}
	if paymentState.Status == contracts.PaymentStatusCancelled {
		if err := repos.Orders.Cancel(ctx, orderID); err != nil {
			return err
		}
		if err := repos.Events.MarkPaymentEventsProcessed(ctx, payload.OrderID); err != nil {
			return fmt.Errorf("mark payment events for order %q processed: %w", payload.OrderID, err)
		}
		return nil
	}
	if paymentState.Status != contracts.PaymentStatusCompleted {
		return nil
	}
	if paymentState.PaidAt == nil {
		return fmt.Errorf("completed payment %s has no paid_at", payload.OrderID)
	}
	if err := orders.MarkPaidAndSchedule(ctx, repos.Orders, repos.SKUClassifier, cfg, orderID, payload.Items, paymentState.PaidAt.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("mark order %d paid: %w", orderID, err)
	}
	if err := repos.Events.MarkPaymentEventsProcessed(ctx, payload.OrderID); err != nil {
		return fmt.Errorf("mark payment events for order %q processed: %w", payload.OrderID, err)
	}
	return nil
}
