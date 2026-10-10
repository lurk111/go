#!/bin/bash
set -eu
# Generated passwords are hexadecimal; refuse arbitrary SQL metacharacters.
[[ "$MONITOR_MYSQL_PASSWORD" =~ ^([a-f0-9]{48}|[a-f0-9]{64})$ ]] || exit 1
mysql --protocol=socket -uroot -p"$MYSQL_ROOT_PASSWORD" "$MYSQL_DATABASE" <<SQL
CREATE USER 'monitor_reader'@'%' IDENTIFIED BY '${MONITOR_MYSQL_PASSWORD}';
GRANT SELECT (task_id,user_id,biz_type,status,stage,worker_id,created_at,updated_at,started_at,finished_at) ON presales_lab.async_tasks TO 'monitor_reader'@'%';
GRANT SELECT (id,task_id,user_id,status,result_json) ON presales_lab.bidding_results TO 'monitor_reader'@'%';
GRANT SELECT ON presales_lab.callback_orders TO 'monitor_reader'@'%';
SQL
