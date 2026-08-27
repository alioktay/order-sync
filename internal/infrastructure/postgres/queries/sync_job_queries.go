package queries

const (
	ClaimJob   = `SELECT j.id, j.status, j.attempts, o.order_id, o.customer_email, o.paid_at, j.due_at, j.waiting_since FROM sync_jobs j JOIN orders o ON o.id = j.order_id WHERE o.payment_status <> 'CANCELLED' AND ((j.status IN ('PENDING', 'WAITING') AND j.due_at <= NOW()) OR (j.status = 'PROCESSING' AND j.locked_at <= NOW() - INTERVAL '2 minutes')) ORDER BY CASE WHEN j.status IN ('PENDING', 'WAITING') THEN j.due_at ELSE j.locked_at + INTERVAL '2 minutes' END FOR UPDATE OF j SKIP LOCKED LIMIT 1`
	NextWake   = `SELECT MIN(wake_at) FROM (SELECT j.due_at AS wake_at FROM sync_jobs j JOIN orders o ON o.id = j.order_id WHERE o.payment_status <> 'CANCELLED' AND j.status IN ('PENDING', 'WAITING') UNION ALL SELECT j.locked_at + INTERVAL '2 minutes' AS wake_at FROM sync_jobs j JOIN orders o ON o.id = j.order_id WHERE o.payment_status <> 'CANCELLED' AND j.status = 'PROCESSING' AND j.locked_at IS NOT NULL) AS job_wakes`
	LockJob    = `UPDATE sync_jobs SET status = 'PROCESSING', locked_at = NOW(), attempts = attempts + 1, updated_at = NOW() WHERE id = $1`
	JobItems   = `SELECT sku, quantity, price::float8 AS price, is_hardware FROM order_items WHERE order_id = (SELECT order_id FROM sync_jobs WHERE id = $1)`
	MarkSynced = `WITH updated_job AS (UPDATE sync_jobs SET status = 'SYNCED', sap_internal_id = $2, synced_at = NOW(), locked_at = NULL, waiting_since = NULL, last_error = NULL, updated_at = NOW() WHERE id = $1 RETURNING order_id) UPDATE orders SET sap_id = $2, updated_at = NOW() WHERE id = (SELECT order_id FROM updated_job)`
	MarkFailed = `UPDATE sync_jobs SET status = $2, due_at = $3, locked_at = NULL, waiting_since = CASE WHEN $2 = 'WAITING' AND waiting_since IS NULL THEN NOW() WHEN $2 = 'WAITING' THEN waiting_since END, last_error = $4, updated_at = NOW() WHERE id = $1`
)
