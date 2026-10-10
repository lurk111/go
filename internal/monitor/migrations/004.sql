ALTER TABLE delivery_outbox ALTER COLUMN destination TYPE varchar(64);
ALTER TABLE task_snapshots ALTER COLUMN environment TYPE varchar(64);
