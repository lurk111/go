CREATE TABLE IF NOT EXISTS callback_orders (
  order_id VARCHAR(64) PRIMARY KEY,
  callback_state VARCHAR(32) NOT NULL,
  retry_count INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS callback_orders_updated ON callback_orders (updated_at, order_id);
