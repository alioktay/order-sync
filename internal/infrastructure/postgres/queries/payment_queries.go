package queries

const (
	UpsertPayment = `
INSERT INTO payments (reference_order_id, order_id, status, paid_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (reference_order_id) DO UPDATE
SET order_id = COALESCE(payments.order_id, EXCLUDED.order_id),
    status = CASE WHEN payments.status = 'COMPLETED' THEN payments.status ELSE EXCLUDED.status END,
    paid_at = CASE
        WHEN payments.status = 'COMPLETED' THEN payments.paid_at
        WHEN EXCLUDED.status = 'COMPLETED' THEN EXCLUDED.paid_at
        ELSE payments.paid_at
    END,
    updated_at = NOW()
WHERE payments.status NOT IN ('COMPLETED', 'CANCELLED')
RETURNING id, reference_order_id, order_id, status, paid_at, created_at, updated_at`
	FindPayment      = `SELECT id, reference_order_id, order_id, status, paid_at, created_at, updated_at FROM payments WHERE reference_order_id = $1 FOR UPDATE`
	LinkPaymentOrder = `UPDATE payments SET order_id = $2, updated_at = NOW() WHERE reference_order_id = $1 AND order_id IS NULL`
	CancelPayment    = `
INSERT INTO payments (reference_order_id, order_id, status)
VALUES ($1, $2, 'CANCELLED')
ON CONFLICT (reference_order_id) DO UPDATE
SET order_id = COALESCE(payments.order_id, EXCLUDED.order_id), status = 'CANCELLED', updated_at = NOW()
WHERE payments.status NOT IN ('COMPLETED', 'CANCELLED')
RETURNING id, reference_order_id, order_id, status, paid_at, created_at, updated_at`
)
