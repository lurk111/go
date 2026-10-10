ALTER TABLE monitor_events ADD COLUMN IF NOT EXISTS check_id varchar(100) NOT NULL DEFAULT '';
ALTER TABLE monitor_events ADD COLUMN IF NOT EXISTS entity_key text NOT NULL DEFAULT '';
ALTER TABLE monitor_events ADD COLUMN IF NOT EXISTS title text NOT NULL DEFAULT '';
ALTER TABLE monitor_events ADD COLUMN IF NOT EXISTS definition json NOT NULL DEFAULT '{}';
ALTER TABLE monitor_events ALTER COLUMN task_id TYPE varchar(1024);
ALTER TABLE task_snapshots ALTER COLUMN task_id TYPE varchar(1024);
CREATE INDEX IF NOT EXISTS ix_monitor_events_check_id ON monitor_events(check_id);
CREATE TABLE IF NOT EXISTS monitor_config_versions (
  version varchar(64) PRIMARY KEY,
  document json NOT NULL,
  created_at timestamptz NOT NULL,
  actor varchar(64) NOT NULL,
  active boolean NOT NULL DEFAULT false
);
CREATE UNIQUE INDEX IF NOT EXISTS one_active_monitor_config ON monitor_config_versions(active) WHERE active;
