package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"order-sync/internal/config"
	"order-sync/internal/contracts"
	"order-sync/internal/orders"
)

type API interface {
	Process(context.Context, Webhook) (contracts.WebhookResult, error)
}

const (
	MessagePaymentRecorded   = "Payment status recorded; order remains unpaid"
	MessageAwaitingShopOrder = "Payment stored and awaiting shop order"
	MessageOrderMarkedPaid   = "Order marked paid and SAP sync scheduled"
	MessageOrderCancelled    = "Order cancelled"
)

type Service struct {
	transaction contracts.TransactionRunner
	config      config.Config
}

func NewService(transaction contracts.TransactionRunner, cfg config.Config) *Service {
	return &Service{transaction: transaction, config: cfg}
}

func (s *Service) Process(ctx context.Context, payload Webhook) (contracts.WebhookResult, error) {
	payload = NormalizePaymentWebhook(payload)
	return s.transaction.Run(ctx, func(repos contracts.TransactionRepositories) (contracts.WebhookResult, error) {
		return processPayment(ctx, repos, payload, s.config)
	})
}

func processPayment(ctx context.Context, repos contracts.TransactionRepositories, payload Webhook, cfg config.Config) (contracts.WebhookResult, error) {
	newEvent, err := repos.Events.Record(ctx, payload.EventID, contracts.EventTypePayment, payload)
	if err != nil {
		return contracts.WebhookResult{}, fmt.Errorf("record payment event: %w", err)
	}
	if !newEvent {
		storedPayload, err := repos.Events.FindPayload(ctx, contracts.EventTypePayment, payload.EventID)
		if err != nil {
			return contracts.WebhookResult{}, fmt.Errorf("load payment event %q: %w", payload.EventID, err)
		}
		var stored Webhook
		if len(storedPayload) == 0 || json.Unmarshal(storedPayload, &stored) != nil || !samePaymentPayload(NormalizePaymentWebhook(stored), payload) {
			return contracts.WebhookResult{}, contracts.ErrPaymentPayloadConflict
		}
		existing, err := repos.Payments.Find(ctx, payload.ReferenceOrderID)
		if err != nil {
			return contracts.WebhookResult{}, fmt.Errorf("find payment %q: %w", payload.ReferenceOrderID, err)
		}
		if existing == nil {
			return contracts.WebhookResult{}, fmt.Errorf("load payment %q for duplicate event: payment not found", payload.ReferenceOrderID)
		}
		result, err := paymentResult(ctx, repos, payload.EventID, payload.ReferenceOrderID, "Payment event already processed", existing.OrderID)
		if err != nil {
			return contracts.WebhookResult{}, err
		}
		result.Duplicate = true
		result.PaymentStatus = existing.Status
		return result, nil
	}
	existing, err := repos.Payments.Find(ctx, payload.ReferenceOrderID)
	if err != nil {
		return contracts.WebhookResult{}, fmt.Errorf("find payment %q: %w", payload.ReferenceOrderID, err)
	}
	if existing != nil && isTerminal(existing.Status) {
		if existing.Status != payload.PaymentStatus {
			return contracts.WebhookResult{}, contracts.ErrPaymentFinalized
		}
		if err := repos.Events.MarkProcessed(ctx, contracts.EventTypePayment, payload.EventID); err != nil {
			return contracts.WebhookResult{}, fmt.Errorf("mark payment event %q processed: %w", payload.EventID, err)
		}
		result, err := paymentResult(ctx, repos, payload.EventID, payload.ReferenceOrderID, "Payment already finalized", existing.OrderID)
		if err != nil {
			return contracts.WebhookResult{}, err
		}
		result.Duplicate = true
		result.PaymentStatus = existing.Status
		return result, nil
	}

	timestamp, err := time.Parse(time.RFC3339, payload.Timestamp)
	if err != nil {
		return contracts.WebhookResult{}, fmt.Errorf("parse payment timestamp: %w", err)
	}
	orderID, err := findOrderID(ctx, repos.Orders, payload.ReferenceOrderID)
	if err != nil {
		return contracts.WebhookResult{}, err
	}
	if payload.PaymentStatus == contracts.PaymentStatusCancelled {
		return cancelPayment(ctx, repos, payload, orderID)
	}
	var paidAt *time.Time
	if payload.PaymentStatus == contracts.PaymentStatusCompleted {
		paidAt = &timestamp
	}
	state, err := repos.Payments.Upsert(ctx, payload.ReferenceOrderID, payload.PaymentStatus, orderID, paidAt)
	if err != nil {
		return contracts.WebhookResult{}, fmt.Errorf("upsert payment %q: %w", payload.ReferenceOrderID, err)
	}

	if payload.PaymentStatus != contracts.PaymentStatusCompleted {
		if err := repos.Events.MarkProcessed(ctx, contracts.EventTypePayment, payload.EventID); err != nil {
			return contracts.WebhookResult{}, fmt.Errorf("mark payment event %q processed: %w", payload.EventID, err)
		}
		return paymentResult(ctx, repos, payload.EventID, payload.ReferenceOrderID, MessagePaymentRecorded, orderID)
	}
	if orderID == nil {
		if err := repos.Events.MarkProcessed(ctx, contracts.EventTypePayment, payload.EventID); err != nil {
			return contracts.WebhookResult{}, fmt.Errorf("mark payment event %q processed: %w", payload.EventID, err)
		}
		return contracts.WebhookResult{
			Message:       MessageAwaitingShopOrder,
			EventID:       payload.EventID,
			OrderID:       payload.ReferenceOrderID,
			PaymentStatus: state.Status,
		}, nil
	}
	result, err := completePayment(ctx, repos, payload, state, *orderID, cfg)
	if err != nil {
		return contracts.WebhookResult{}, err
	}
	return paymentResult(ctx, repos, payload.EventID, payload.ReferenceOrderID, result.Message, orderID)
}

func samePaymentPayload(left, right Webhook) bool {
	return left.ReferenceOrderID == right.ReferenceOrderID && left.PaymentStatus == right.PaymentStatus && left.Timestamp == right.Timestamp
}

func isTerminal(status contracts.PaymentStatus) bool {
	return status == contracts.PaymentStatusCompleted || status == contracts.PaymentStatusCancelled
}

func cancelPayment(ctx context.Context, repos contracts.TransactionRepositories, payload Webhook, orderID *int64) (contracts.WebhookResult, error) {
	if orderID != nil {
		items, err := repos.Orders.ListItems(ctx, *orderID)
		if err != nil {
			return contracts.WebhookResult{}, fmt.Errorf("load items for order %d: %w", *orderID, err)
		}
		hardware, err := repos.SKUClassifier.HasHardware(ctx, paymentItemSKUs(items))
		if err != nil {
			return contracts.WebhookResult{}, fmt.Errorf("classify order %d: %w", *orderID, err)
		}
		for _, item := range items {
			if item.IsHardware != nil && *item.IsHardware {
				hardware = true
			}
		}
		if !hardware {
			return contracts.WebhookResult{}, contracts.ErrDigitalCancellation
		}
	}
	if _, err := repos.Payments.Cancel(ctx, payload.ReferenceOrderID, orderID); err != nil {
		return contracts.WebhookResult{}, err
	}
	if orderID == nil {
		if err := repos.Events.MarkProcessed(ctx, contracts.EventTypePayment, payload.EventID); err != nil {
			return contracts.WebhookResult{}, fmt.Errorf("mark payment event %q processed: %w", payload.EventID, err)
		}
		return contracts.WebhookResult{
			Message:       MessagePaymentRecorded,
			EventID:       payload.EventID,
			OrderID:       payload.ReferenceOrderID,
			PaymentStatus: contracts.PaymentStatusCancelled,
		}, nil
	}
	if err := repos.Orders.Cancel(ctx, *orderID); err != nil {
		return contracts.WebhookResult{}, err
	}
	if err := repos.Events.MarkProcessed(ctx, contracts.EventTypePayment, payload.EventID); err != nil {
		return contracts.WebhookResult{}, fmt.Errorf("mark payment event %q processed: %w", payload.EventID, err)
	}
	return paymentResult(ctx, repos, payload.EventID, payload.ReferenceOrderID, MessageOrderCancelled, orderID)
}

func paymentResult(ctx context.Context, repos contracts.TransactionRepositories, eventID, orderID, message string, internalOrderID *int64) (contracts.WebhookResult, error) {
	result := contracts.WebhookResult{Message: message, EventID: eventID, OrderID: orderID}
	if internalOrderID == nil {
		return result, nil
	}
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

func paymentItemSKUs(items []contracts.OrderItem) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		if item.IsHardware == nil {
			result = append(result, item.SKU)
		}
	}
	return result
}

func findOrderID(ctx context.Context, repository contracts.OrderRepository, referenceOrderID string) (*int64, error) {
	id, err := repository.FindID(ctx, referenceOrderID)
	if err != nil {
		return nil, fmt.Errorf("find order %q: %w", referenceOrderID, err)
	}
	if id == 0 {
		return nil, nil
	}
	return &id, nil
}

func completePayment(ctx context.Context, repos contracts.TransactionRepositories, payload Webhook, state contracts.PaymentState, orderID int64, cfg config.Config) (contracts.WebhookResult, error) {
	items, err := repos.Orders.ListItems(ctx, orderID)
	if err != nil {
		return contracts.WebhookResult{}, fmt.Errorf("load items for order %d: %w", orderID, err)
	}
	if state.PaidAt == nil {
		return contracts.WebhookResult{}, fmt.Errorf("completed payment %s has no paid_at", payload.ReferenceOrderID)
	}
	if err := orders.MarkPaidAndSchedule(ctx, repos.Orders, repos.SKUClassifier, cfg, orderID, items, state.PaidAt.Format(time.RFC3339Nano)); err != nil {
		return contracts.WebhookResult{}, fmt.Errorf("mark order %d paid: %w", orderID, err)
	}
	if err := repos.Events.MarkProcessed(ctx, contracts.EventTypePayment, payload.EventID); err != nil {
		return contracts.WebhookResult{}, fmt.Errorf("mark payment event %q processed: %w", payload.EventID, err)
	}
	return contracts.WebhookResult{Message: MessageOrderMarkedPaid}, nil
}
