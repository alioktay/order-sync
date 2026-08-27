package queries

const (
	InsertEvent                = `INSERT INTO webhook_events (event_id, event_type, payload) VALUES ($1, $2, $3::jsonb) ON CONFLICT (event_type, event_id) DO NOTHING RETURNING event_id`
	FindEventPayload           = `SELECT payload FROM webhook_events WHERE event_type = $1 AND event_id = $2`
	MarkEventProcessed         = `UPDATE webhook_events SET processed_at = NOW() WHERE event_type = $1 AND event_id = $2`
	MarkPaymentEventsProcessed = `UPDATE webhook_events SET processed_at = NOW() WHERE event_type = 'PAYMENT' AND payload->>'reference_order_id' = $1 AND processed_at IS NULL`
)
