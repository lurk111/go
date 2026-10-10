-- MySQL demo schema. Business installations can use their existing schema and
-- map query aliases instead. Supply a separate SELECT-only monitoring account.
CREATE TABLE IF NOT EXISTS callback_orders (
  order_id VARCHAR(64) PRIMARY KEY,
  callback_state VARCHAR(32) NOT NULL,
  retry_count INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  INDEX callback_orders_updated (updated_at, order_id)
);
