CREATE TABLE async_tasks (
 task_id VARCHAR(64) PRIMARY KEY, biz_type VARCHAR(32) NOT NULL, user_id VARCHAR(64) NOT NULL,
 status VARCHAR(20) NOT NULL DEFAULT 'pending', stage VARCHAR(64) NOT NULL DEFAULT '',
 progress TEXT, error_message TEXT, worker_id VARCHAR(64),
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
 started_at DATETIME(6), finished_at DATETIME(6),
 INDEX scan_idx (biz_type,updated_at,task_id), INDEX active_idx (status,task_id)
);
CREATE TABLE bidding_results (
 id INT PRIMARY KEY AUTO_INCREMENT, task_id VARCHAR(64) NOT NULL, user_id VARCHAR(64) NOT NULL,
 status VARCHAR(20) DEFAULT 'completed', file_count INT DEFAULT 0, result_json LONGTEXT,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), INDEX task_idx (task_id,user_id)
);
CREATE TABLE lab_jobs (
 id INT PRIMARY KEY AUTO_INCREMENT, task_id VARCHAR(64) NOT NULL, action VARCHAR(20) NOT NULL,
 due_at DATETIME(6) NOT NULL
);
