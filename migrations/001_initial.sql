-- Orders store the latest business state received from the shop and payment provider.
CREATE TABLE IF NOT EXISTS orders (
  id BIGSERIAL PRIMARY KEY,
  order_id TEXT NOT NULL UNIQUE,
  customer_email TEXT NOT NULL,
  payment_status TEXT NOT NULL DEFAULT 'PENDING' CHECK (payment_status IN ('PENDING', 'PAID', 'CANCELLED')),
  sap_id TEXT,
  paid_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS order_items (
  id BIGSERIAL PRIMARY KEY,
  order_id BIGINT NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
  sku TEXT NOT NULL,
  quantity INTEGER NOT NULL CHECK (quantity > 0),
  price NUMERIC(12, 2) NOT NULL CHECK (price >= 0),
  is_hardware BOOLEAN
);

-- SKU categories are operational catalog configuration. Unknown SKUs are treated as non-hardware.
CREATE TABLE IF NOT EXISTS sku_classifications (
  sku TEXT PRIMARY KEY,
  category TEXT NOT NULL CHECK (category IN ('HARDWARE', 'DIGITAL')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO sku_classifications (sku, category)
VALUES
  ('NUKI-SL3', 'HARDWARE'),
  ('NUKI-BRIDGE', 'HARDWARE'),
  ('NUKI-SMART-HOSTING', 'DIGITAL')
ON CONFLICT (sku) DO NOTHING;

CREATE TABLE IF NOT EXISTS webhook_events (
  event_id TEXT NOT NULL,
  event_type TEXT NOT NULL CHECK (event_type IN ('SHOP', 'PAYMENT')),
  payload JSONB NOT NULL,
  processed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (event_type, event_id)
);

-- Payments keep the latest provider state
-- order_id is nullable because a payment may arrive before its shop order.
CREATE TABLE IF NOT EXISTS payments (
  id BIGSERIAL PRIMARY KEY,
  reference_order_id TEXT NOT NULL UNIQUE,
  order_id BIGINT UNIQUE REFERENCES orders(id) ON DELETE SET NULL,
  status TEXT NOT NULL CHECK (status IN ('PENDING', 'COMPLETED', 'FAILED', 'CANCELLED')),
  paid_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_webhook_events_shop_order_id
  ON webhook_events ((payload->>'order_id')) WHERE event_type = 'SHOP';
CREATE INDEX IF NOT EXISTS idx_webhook_events_payment_reference_order_id
  ON webhook_events ((payload->>'reference_order_id')) WHERE event_type = 'PAYMENT';

CREATE TABLE IF NOT EXISTS sync_jobs (
  id BIGSERIAL PRIMARY KEY,
  order_id BIGINT NOT NULL UNIQUE REFERENCES orders(id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'PROCESSING', 'SYNCED', 'WAITING', 'DEAD', 'CANCELLED')),
  due_at TIMESTAMPTZ NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  locked_at TIMESTAMPTZ,
  waiting_since TIMESTAMPTZ,
  last_error TEXT,
  sap_internal_id TEXT,
  synced_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_sync_jobs_due ON sync_jobs (due_at) WHERE status IN ('PENDING', 'PROCESSING', 'WAITING');

CREATE OR REPLACE FUNCTION notify_sync_job_change() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  PERFORM pg_notify('sync_jobs', NEW.id::text);
  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS sync_jobs_wakeup ON sync_jobs;
CREATE TRIGGER sync_jobs_wakeup
AFTER INSERT OR UPDATE OF status, due_at ON sync_jobs
FOR EACH ROW EXECUTE FUNCTION notify_sync_job_change();
