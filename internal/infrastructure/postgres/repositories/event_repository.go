package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"order-sync/internal/contracts"
	"order-sync/internal/infrastructure/postgres/queries"

	"github.com/jackc/pgx/v5"
)

type WebhookEventRepository struct{ db DBTX }

func NewWebhookEventRepository(db DBTX) *WebhookEventRepository {
	return &WebhookEventRepository{db: db}
}

var _ contracts.EventRepository = (*WebhookEventRepository)(nil)

func (r *WebhookEventRepository) Record(ctx context.Context, id string, eventType contracts.EventType, payload any) (bool, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("marshal webhook event %q: %w", id, err)
	}
	var result string
	err = r.db.QueryRow(ctx, queries.InsertEvent, id, eventType, data).Scan(&result)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("insert webhook event %q: %w", id, err)
	}
	return true, nil
}

func (r *WebhookEventRepository) FindPayload(ctx context.Context, eventType contracts.EventType, id string) ([]byte, error) {
	var payload []byte
	err := r.db.QueryRow(ctx, queries.FindEventPayload, eventType, id).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find webhook event payload %q: %w", id, err)
	}
	return payload, nil
}
func (r *WebhookEventRepository) MarkProcessed(ctx context.Context, eventType contracts.EventType, id string) error {
	_, err := r.db.Exec(ctx, queries.MarkEventProcessed, eventType, id)
	if err != nil {
		return fmt.Errorf("mark webhook event %q processed: %w", id, err)
	}
	return nil
}
func (r *WebhookEventRepository) MarkPaymentEventsProcessed(ctx context.Context, orderID string) error {
	_, err := r.db.Exec(ctx, queries.MarkPaymentEventsProcessed, orderID)
	if err != nil {
		return fmt.Errorf("mark payment events for order %q processed: %w", orderID, err)
	}
	return nil
}
