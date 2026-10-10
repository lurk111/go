CREATE TABLE IF NOT EXISTS monitor_secrets (
  id varchar(64) PRIMARY KEY,
  ciphertext bytea NOT NULL,
  updated_at timestamptz NOT NULL
);
