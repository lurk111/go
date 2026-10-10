CREATE TABLE IF NOT EXISTS monitor_cursors (
	key VARCHAR(160) NOT NULL,
	high_watermark TIMESTAMP WITH TIME ZONE,
	reconcile_after VARCHAR(64),
	reconcile_at TIMESTAMP WITH TIME ZONE,
	last_success_at TIMESTAMP WITH TIME ZONE,
	failures INTEGER NOT NULL,
	PRIMARY KEY (key)
);
CREATE TABLE IF NOT EXISTS monitor_events (
	event_id VARCHAR(36) NOT NULL,
	logical_key VARCHAR(64) NOT NULL,
	task_key VARCHAR(64) NOT NULL,
	task_id VARCHAR(64) NOT NULL,
	episode INTEGER NOT NULL,
	rule_code VARCHAR(64) NOT NULL,
	rule_version VARCHAR(16) NOT NULL,
	status VARCHAR(32) NOT NULL,
	condition_active BOOLEAN NOT NULL,
	severity VARCHAR(16) NOT NULL,
	diagnosis_status VARCHAR(32) NOT NULL,
	evidence JSON NOT NULL,
	opened_at TIMESTAMP WITH TIME ZONE NOT NULL,
	last_seen_at TIMESTAMP WITH TIME ZONE NOT NULL,
	closed_at TIMESTAMP WITH TIME ZONE,
	close_reason TEXT,
	revision INTEGER NOT NULL,
	PRIMARY KEY (event_id),
	UNIQUE (logical_key, episode)
);
CREATE INDEX IF NOT EXISTS ix_monitor_events_logical_key ON monitor_events (logical_key);
CREATE INDEX IF NOT EXISTS ix_monitor_events_task_key ON monitor_events (task_key);
CREATE UNIQUE INDEX IF NOT EXISTS one_live_condition ON monitor_events (logical_key) WHERE condition_active;
CREATE TABLE IF NOT EXISTS task_snapshots (
	key VARCHAR(64) NOT NULL,
	source VARCHAR(80) NOT NULL,
	environment VARCHAR(40) NOT NULL,
	task_id VARCHAR(64) NOT NULL,
	status VARCHAR(32) NOT NULL,
	stage VARCHAR(64) NOT NULL,
	source_updated_at TIMESTAMP WITH TIME ZONE,
	observed_at TIMESTAMP WITH TIME ZONE NOT NULL,
	next_check_at TIMESTAMP WITH TIME ZONE NOT NULL,
	first_seen_at TIMESTAMP WITH TIME ZONE NOT NULL,
	evidence JSON NOT NULL,
	rule_states JSON NOT NULL,
	PRIMARY KEY (key),
	UNIQUE (source, environment, task_id)
);
CREATE INDEX IF NOT EXISTS ix_task_snapshots_next_check_at ON task_snapshots (next_check_at);
CREATE TABLE IF NOT EXISTS delivery_outbox (
	id VARCHAR(36) NOT NULL,
	event_id VARCHAR(36) NOT NULL,
	revision INTEGER NOT NULL,
	destination VARCHAR(32) NOT NULL,
	kind VARCHAR(32) NOT NULL,
	payload JSON NOT NULL,
	status VARCHAR(32) NOT NULL,
	next_retry_at TIMESTAMP WITH TIME ZONE NOT NULL,
	attempts INTEGER NOT NULL,
	error VARCHAR(100),
	confirmed_at TIMESTAMP WITH TIME ZONE,
	PRIMARY KEY (id),
	UNIQUE (event_id, revision, destination),
	FOREIGN KEY(event_id) REFERENCES monitor_events (event_id)
);
CREATE INDEX IF NOT EXISTS ix_delivery_outbox_next_retry_at ON delivery_outbox (next_retry_at);
CREATE INDEX IF NOT EXISTS ix_delivery_outbox_status ON delivery_outbox (status);
CREATE TABLE IF NOT EXISTS event_audit (
	id VARCHAR(36) NOT NULL,
	event_id VARCHAR(36) NOT NULL,
	action VARCHAR(64) NOT NULL,
	actor VARCHAR(64) NOT NULL,
	at TIMESTAMP WITH TIME ZONE NOT NULL,
	details JSON NOT NULL,
	PRIMARY KEY (id),
	FOREIGN KEY(event_id) REFERENCES monitor_events (event_id)
);
CREATE INDEX IF NOT EXISTS ix_event_audit_event_id ON event_audit (event_id);
ALTER TABLE monitor_cursors ADD COLUMN IF NOT EXISTS last_failure_at timestamptz;
CREATE TABLE IF NOT EXISTS diagnosis_jobs (
event_id varchar(36) PRIMARY KEY REFERENCES monitor_events(event_id), status varchar(32) NOT NULL,
queued_at timestamptz NOT NULL, deadline_at timestamptz NOT NULL, started_at timestamptz, finished_at timestamptz,
claim_token varchar(36), tool_calls integer NOT NULL, evidence json NOT NULL, report json, report_hash varchar(64), error varchar(64));
