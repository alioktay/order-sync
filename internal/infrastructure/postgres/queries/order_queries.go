package queries

const (
	InsertOrder  = `INSERT INTO orders (order_id, customer_email) VALUES ($1, $2) ON CONFLICT (order_id) DO NOTHING RETURNING id`
	InsertItem   = `INSERT INTO order_items (order_id, sku, quantity, price, is_hardware) VALUES ($1, $2, $3, $4, $5)`
	FindOrderID  = `SELECT id FROM orders WHERE order_id = $1 FOR UPDATE`
	FindOrder    = `SELECT id, customer_email FROM orders WHERE order_id = $1 FOR UPDATE`
	FindStatus   = `SELECT o.order_id, o.customer_email, o.payment_status, COALESCE(p.status, 'NOT_RECEIVED') AS payment_status, o.paid_at, COALESCE(j.status, 'NOT_SCHEDULED') AS sync_status, j.due_at, j.attempts, j.last_error, o.sap_id FROM orders o LEFT JOIN payments p ON p.order_id = o.id LEFT JOIN sync_jobs j ON j.order_id = o.id WHERE o.order_id = $1`
	FindItems    = `SELECT sku, quantity, price::float8 AS price, is_hardware FROM order_items WHERE order_id = $1`
	MarkPaid     = `UPDATE orders SET payment_status = 'PAID', paid_at = $2, updated_at = NOW() WHERE id = $1`
	ScheduleSync = `INSERT INTO sync_jobs (order_id, due_at) VALUES ($1, NOW() + ($2 * INTERVAL '1 second')) ON CONFLICT (order_id) DO NOTHING`
	CancelOrder  = `
WITH locked_order AS (SELECT id FROM orders WHERE id = $1 FOR UPDATE),
locked_job AS (SELECT id, status FROM sync_jobs WHERE order_id = $1 FOR UPDATE)
SELECT id FROM locked_order
WHERE NOT EXISTS (SELECT 1 FROM locked_job WHERE status NOT IN ('PENDING', 'WAITING', 'DEAD'))`
	MarkCancelledOrder = `UPDATE orders SET payment_status = 'CANCELLED', updated_at = NOW() WHERE id = $1`
	CancelJob          = `UPDATE sync_jobs SET status = 'CANCELLED', locked_at = NULL, due_at = NOW(), updated_at = NOW() WHERE order_id = $1 AND status IN ('PENDING', 'WAITING', 'DEAD')`
)
