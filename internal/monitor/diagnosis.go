package monitor

import (
	"bidmon/internal/evidence"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

func Redact(s string) string { return evidence.Redact(s) }

type bounded struct {
	bytes.Buffer
	Limit int
}

func (b *bounded) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.Limit {
		return 0, errors.New("output_limit")
	}
	return b.Buffer.Write(p)
}
func CollectSSH(ctx context.Context, c Config, event Event, operation string) M {
	now := time.Now()
	r := evidence.Request{Event: event.EventID, Task: event.TaskID, Operation: operation, Service: "lab-worker", From: now.Unix() - 1799, To: now.Unix() + 1, Expires: now.Unix() + 30, Limit: 200}
	fail := func(reason string) M { return M(evidence.Result(r, nil, reason, false)) }
	key, e := os.ReadFile(c.BindingKey)
	if e != nil {
		return fail("binding_unavailable")
	}
	raw, e := evidence.Sign(r, key)
	if e != nil {
		return fail("binding_unavailable")
	}
	private, e := os.ReadFile(c.KeyFile)
	if e != nil {
		return fail("ssh_key_unavailable")
	}
	signer, e := ssh.ParsePrivateKey(private)
	if e != nil {
		return fail("ssh_key_unavailable")
	}
	check, e := knownhosts.New(c.KnownHosts)
	if e != nil {
		return fail("host_key_unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, e := (&net.Dialer{}).DialContext(ctx, "tcp", c.SSHHost)
	if e != nil {
		return fail("ssh_unavailable")
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	sc, ch, req, e := ssh.NewClientConn(conn, c.SSHHost, &ssh.ClientConfig{User: "diag", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: check, Timeout: 3 * time.Second})
	if e != nil {
		return fail("ssh_or_host_key_unavailable")
	}
	client := ssh.NewClient(sc, ch, req)
	defer client.Close()
	session, e := client.NewSession()
	if e != nil {
		return fail("ssh_unavailable")
	}
	defer session.Close()
	out := &bounded{Limit: evidence.MaxBytes}
	stderr := &bounded{Limit: 4096}
	session.Stdout = out
	session.Stderr = stderr
	session.Stdin = bytes.NewReader(raw)
	if e = session.Run("presales-diag"); e != nil {
		return fail("helper_denied_or_unavailable")
	}
	var result M
	if json.Unmarshal(out.Bytes(), &result) != nil {
		return fail("invalid_evidence")
	}
	if str(result, "event_id") != event.EventID || str(result, "task_id") != event.TaskID || str(result, "operation") != operation || str(result, "source") != "ssh-lab" || len(str(result, "evidence_id")) != 35 {
		return fail("evidence_identity_mismatch")
	}
	if _, e = hex.DecodeString(strings.TrimPrefix(str(result, "evidence_id"), "ev-")); e != nil {
		return fail("invalid_evidence")
	}
	if delta := time.Since(parsed(result, "collected_at")); delta > time.Minute || delta < -time.Minute {
		return fail("evidence_clock_skew")
	}
	if _, ok := result["truncated"].(bool); !ok {
		return fail("invalid_evidence")
	}
	result["content"] = evidence.Sanitize(result["content"])
	return result
}
func RuleReport(event Event, records Records, status string) M {
	facts := []any{}
	causes := []any{}
	missing := []any{}
	category, certainty, summary := "insufficient_evidence", "insufficient_evidence", "现有证据不足以确认异常原因"
	codes := map[string]string{}
	extractID, workerID := "", ""
	for _, r := range records {
		if str(r, "unavailable_reason") != "" {
			missing = append(missing, str(r, "operation")+":"+str(r, "unavailable_reason"))
			continue
		}
		op, id := str(r, "operation"), str(r, "evidence_id")
		content := obj(r, "content")
		if op == "task_snapshot" {
			facts = append(facts, M{"text": "观察到任务状态：" + str(content, "status"), "evidence_ids": []string{id}})
		}
		if op == "worker_status" && content["heartbeat_fresh"] == false {
			workerID = id
		}
		rows, _ := content["records"].([]any)
		for _, v := range rows {
			row, ok := v.(map[string]any)
			if !ok {
				continue
			}
			code, _ := row["error_code"].(string)
			if op == "model_error" && row["provider"] == "synthetic-provider" && (code == "insufficient_quota" || code == "rate_limit_exceeded" || code == "request_timeout") {
				codes[code] = id
			}
			if op == "extraction_summary" && code == "extractor_failed" {
				extractID = id
			}
		}
	}
	causeID := ""
	if len(codes) == 1 {
		for code, id := range codes {
			category = map[string]string{"insufficient_quota": "model_quota_exhausted", "rate_limit_exceeded": "model_rate_limited", "request_timeout": "model_timeout"}[code]
			summary = map[string]string{"insufficient_quota": "模型供应商明确返回额度不足", "rate_limit_exceeded": "模型供应商明确返回限流", "request_timeout": "模型调用记录明确返回请求超时"}[code]
			causeID = id
		}
		certainty = "confirmed_by_error"
	} else if len(codes) > 1 {
		summary = "观察到多类模型错误，不能确认单一原因"
	} else if extractID != "" {
		category = "extraction_failed"
		certainty = "confirmed_by_error"
		summary = "提取器返回明确错误，不能据此认定原文为空"
		causeID = extractID
	} else if workerID != "" && (event.RuleCode == "QUEUE_DELAY" || event.RuleCode == "EXECUTION_STALE") {
		category = "worker_unavailable_suspect"
		certainty = "supported_hypothesis"
		summary = "Worker 心跳过期，可能不可用；尚未直接观察进程状态"
		causeID = workerID
	}
	if causeID != "" {
		causes = append(causes, M{"text": summary, "evidence_ids": []string{causeID}})
	}
	if status != "completed" {
		category = "diagnosis_unavailable"
		certainty = "insufficient_evidence"
		summary = "诊断未完成，请依据已有证据人工复核"
		causes = []any{}
	}
	return M{"event_id": event.EventID, "task_id": event.TaskID, "diagnosis_mode": "rule_based", "diagnosis_status": status, "business_recovery": "not_confirmed", "category": category, "certainty": certainty, "summary": summary, "facts": facts, "causes": causes, "missing_evidence": missing, "recommended_actions": RecommendedActions(event, category), "generated_at": stamp(time.Now()), "rule_version": "v1", "report_version": 1}
}
func ValidateReport(report M, e Event, records Records) error {
	if str(report, "event_id") != e.EventID || str(report, "task_id") != e.TaskID || (str(report, "diagnosis_mode") != "rule_based" && str(report, "diagnosis_mode") != "model_assisted") || str(report, "business_recovery") != "not_confirmed" {
		return errors.New("invalid_report_identity")
	}
	if e.CheckID != "" && (str(report, "check_id") != e.CheckID || str(report, "entity_key") != e.EntityKey || str(report, "rule_version") != e.RuleVersion) {
		return errors.New("invalid_generic_report_identity")
	}
	categories := map[string]bool{"model_quota_exhausted": true, "model_rate_limited": true, "model_timeout": true, "extraction_failed": true, "worker_unavailable_suspect": true, "insufficient_evidence": true, "diagnosis_unavailable": true}
	if e.CheckID != "" {
		for _, rule := range eventCheck(e).DiagnosticRules {
			categories[rule.Category] = true
		}
	}
	statuses := map[string]bool{"completed": true, "failed": true, "timed_out": true}
	certainties := map[string]bool{"confirmed_by_error": true, "supported_hypothesis": true, "insufficient_evidence": true}
	if !categories[str(report, "category")] || !statuses[str(report, "diagnosis_status")] || !certainties[str(report, "certainty")] || len([]rune(str(report, "summary"))) > 1000 || str(report, "summary") == "" || parsed(report, "generated_at").IsZero() {
		return errors.New("invalid_report_shape")
	}
	valid := map[string]bool{}
	seen := map[string]bool{}
	for _, r := range records {
		if str(r, "event_id") != e.EventID || str(r, "task_id") != e.TaskID {
			return errors.New("invalid_evidence_identity")
		}
		if e.CheckID != "" {
			if str(r, "check_id") != e.CheckID || str(r, "resource_id") != e.EntityKey {
				return errors.New("invalid_generic_evidence_identity")
			}
			op := str(r, "operation")
			request := evidence.ProfileRequest{Version: 2, EventID: e.EventID, CheckID: e.CheckID, ResourceID: e.EntityKey, Operation: op}
			if op == "logs" || op == "service_status" {
				request.ProfileID = eventCheck(e).EvidenceProfile
				request.TargetID, request.ServiceID = str(r, "target_id"), str(r, "service_id")
				if str(r, "unavailable_reason") == "" && (request.ProfileID == "" || request.TargetID == "" || request.ServiceID == "") {
					return errors.New("invalid_generic_evidence_scope")
				}
			} else if op != "snapshot" && op != "details" {
				return errors.New("invalid_generic_evidence_operation")
			}
			// Collection-time clock checks happen in the SSH client. Reports may be
			// archived after the diagnosis deadline, so validate timestamp syntax
			// and identity without rejecting legitimately older saved evidence.
			if err := evidence.ValidateProfileResult(evidence.M(r), request, parsed(r, "collected_at")); err != nil {
				return errors.New("invalid_generic_evidence_shape")
			}
			id := str(r, "evidence_id")
			if seen[id] {
				return errors.New("duplicate_evidence_id")
			}
			seen[id] = true
		}
		if str(r, "unavailable_reason") == "" {
			valid[str(r, "evidence_id")] = true
		}
	}
	if e.CheckID != "" {
		base := GeneralRuleReport(e, records, str(report, "diagnosis_status"))
		for _, field := range []string{"category", "certainty", "summary"} {
			if report[field] != base[field] {
				return errors.New("generic_rule_authority_changed")
			}
		}
		for _, field := range []string{"facts", "causes", "recommended_actions", "missing_evidence"} {
			actual, _ := json.Marshal(report[field])
			expected, _ := json.Marshal(base[field])
			if !bytes.Equal(actual, expected) {
				return errors.New("generic_rule_authority_changed")
			}
		}
		if str(report, "diagnosis_mode") == "model_assisted" && str(report, "model_assessment_type") != "hypothesis" {
			return errors.New("invalid_model_assessment_type")
		}
	}
	raw, _ := json.Marshal(report)
	var normalized M
	_ = json.Unmarshal(raw, &normalized)
	findingFields := []string{"facts", "causes"}
	listFields := []string{"recommended_actions", "missing_evidence"}
	if str(report, "diagnosis_mode") == "model_assisted" {
		if str(report, "model_status") != "completed" || str(report, "model_name") == "" {
			return errors.New("invalid_model_report")
		}
		normalized["model_findings"] = []any{normalized["model_analysis"]}
		findingFields = append(findingFields, "model_findings")
		listFields = append(listFields, "model_recommended_actions")
	}
	for _, field := range findingFields {
		items, ok := normalized[field].([]any)
		if !ok || len(items) > 12 {
			return errors.New("invalid_findings")
		}
		if field == "causes" && str(report, "certainty") != "insufficient_evidence" && len(items) == 0 {
			return errors.New("unsupported_conclusion")
		}
		for _, item := range items {
			m, ok := item.(map[string]any)
			if !ok {
				return errors.New("invalid_finding")
			}
			text, ok := m["text"].(string)
			if !ok || strings.TrimSpace(text) == "" || len([]rune(text)) > 1000 {
				return errors.New("invalid_finding_text")
			}
			ids, ok := m["evidence_ids"].([]any)
			if !ok || len(ids) == 0 {
				return errors.New("missing_reference")
			}
			for _, id := range ids {
				s, ok := id.(string)
				if !ok || !valid[s] {
					return errors.New("unknown_evidence_reference")
				}
			}
		}
	}
	for _, field := range listFields {
		items, ok := normalized[field].([]any)
		if !ok || len(items) > 12 {
			return errors.New("invalid_report_list")
		}
		if field == "model_recommended_actions" && len(items) == 0 {
			return errors.New("invalid_model_actions")
		}
		for _, v := range items {
			value, ok := v.(string)
			if !ok || len([]rune(value)) > 1000 {
				return errors.New("invalid_report_text")
			}
		}
	}
	return nil
}

type Diagnosis struct {
	Cfg     Config
	DB      *gorm.DB
	Collect func(context.Context, Config, Event, string) M
}

func (d *Diagnosis) Prepare(ctx context.Context) error {
	return d.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var events []Event
		if e := scoped(tx, d.Cfg).Where("status='open' AND diagnosis_status='not_requested'").Order("opened_at").Limit(100).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Find(&events).Error; e != nil {
			return e
		}
		for _, event := range events {
			now := time.Now().UTC()
			job := Job{EventID: event.EventID, Status: "queued", QueuedAt: now, DeadlineAt: now.Add(d.Cfg.DiagTimeout), Evidence: Records{}}
			if e := tx.Create(&job).Error; e != nil {
				return e
			}
			if e := tx.Model(&Event{}).Where("event_id=?", event.EventID).Update("diagnosis_status", "queued").Error; e != nil {
				return e
			}
		}
		return nil
	})
}
func (d *Diagnosis) Finish(ctx context.Context, id, token, status string, records Records, prepared ...M) error {
	return d.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var event Event
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&event, "event_id=?", id).Error; e != nil {
			return e
		}
		var job Job
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&job, "event_id=?", id).Error; e != nil {
			return e
		}
		if job.ClaimToken == nil || *job.ClaimToken != token || job.ReportHash != nil {
			return nil
		}
		now := time.Now().UTC()
		if now.After(job.DeadlineAt) {
			status = "timed_out"
		}
		report := ReportForEvent(event, records, status)
		if status == "completed" && len(prepared) > 0 {
			report = prepared[0]
		}
		if e := ValidateReport(report, event, records); e != nil {
			return e
		}
		raw, _ := json.Marshal(report)
		h := sha256.Sum256(raw)
		hash := hex.EncodeToString(h[:])
		job.ReportHash = &hash
		job.Report = report
		job.Evidence = records
		job.FinishedAt = &now
		if job.Status != "canceled" {
			job.Status = status
			event.DiagnosisStatus = status
		}
		if e := tx.Save(&job).Error; e != nil {
			return e
		}
		if event.Status == "open" && job.Status != "canceled" {
			event.Revision++
			if e := enqueue(tx, event, "diagnosis", report); e != nil {
				return e
			}
		}
		if e := tx.Save(&event).Error; e != nil {
			return e
		}
		return audit(tx, id, "diagnosis_"+job.Status, M{"report_hash": hash, "business_recovery": "not_confirmed"})
	})
}
func (d *Diagnosis) RunOne(ctx context.Context) error {
	// Original deadline is retained across restart; do not rerun abandoned in-flight jobs.
	var expired []Job
	if e := d.DB.Where("event_id IN (?) AND status IN ? AND deadline_at<=?", eventScope(d.DB, d.Cfg), []string{"queued", "running"}, time.Now()).Limit(20).Find(&expired).Error; e != nil {
		return e
	}
	for _, job := range expired {
		token := "expired"
		if job.ClaimToken != nil {
			token = *job.ClaimToken
		} else {
			if e := d.DB.Model(&Job{}).Where("event_id=? AND claim_token IS NULL", job.EventID).Update("claim_token", token).Error; e != nil {
				return e
			}
		}
		if e := d.Finish(ctx, job.EventID, token, "timed_out", job.Evidence); e != nil {
			return e
		}
	}
	var job Job
	token := uuid.NewString()
	err := d.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Where("event_id IN (?) AND status='queued' AND deadline_at>?", eventScope(tx, d.Cfg), time.Now()).Order("queued_at").Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).First(&job).Error; e != nil {
			return e
		}
		now := time.Now().UTC()
		job.Status = "running"
		job.ToolCalls = 1
		job.StartedAt = &now
		job.ClaimToken = &token
		return tx.Save(&job).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var event Event
	if err = d.DB.First(&event, "event_id=?", job.EventID).Error; err != nil {
		return err
	}
	records := Records{M(evidence.Result(evidence.Request{Event: event.EventID, Task: event.TaskID, Operation: "task_snapshot"}, map[string]any(event.Evidence), "", false))}
	records[0]["source"] = "monitor-db"
	if event.CheckID != "" {
		records = Records{genericSnapshot(event)}
	}
	work, cancel := context.WithDeadline(ctx, job.DeadlineAt)
	defer cancel()
	collect := d.Collect
	if collect == nil {
		collect = CollectSSH
	}
	ops := []string{"task_logs", "worker_status", "extraction_summary", "model_error"}
	if event.CheckID != "" {
		collect = collectGeneric
		ops = []string{"logs"}
		if eventCheck(event).DetailQuery != "" {
			ops = append(ops, "details")
		}
	}
	status := "completed"
	for _, op := range ops {
		var current Job
		if err = d.DB.First(&current, "event_id=?", job.EventID).Error; err != nil {
			return err
		}
		if current.Status == "canceled" {
			break
		}
		if work.Err() != nil {
			status = "timed_out"
			break
		}
		if current.ToolCalls >= 8 {
			status = "failed"
			break
		}
		if err = d.DB.Model(&Job{}).Where("event_id=? AND claim_token=?", job.EventID, token).Updates(map[string]any{"tool_calls": gorm.Expr("tool_calls+1"), "evidence": records}).Error; err != nil {
			return err
		}
		records = append(records, collect(work, d.Cfg, event, op))
		if err = d.DB.Model(&Job{}).Where("event_id=? AND claim_token=?", job.EventID, token).Update("evidence", records).Error; err != nil {
			return err
		}
	}
	report := ReportForEvent(event, records, status)
	var current Job
	if err = d.DB.First(&current, "event_id=?", job.EventID).Error; err != nil {
		return err
	}
	modelAllowed := event.CheckID == "" || (eventCheck(event).AllowModel && eventCheck(event).DiagnosisMode == "hybrid")
	if d.Cfg.Model != nil && modelAllowed && status == "completed" && current.Status != "canceled" && work.Err() == nil {
		result, modelErr := ModelReport(work, *d.Cfg.Model, event, records, report)
		if modelErr != nil {
			report["model_status"] = "fallback"
			report["model_error"] = modelErr.Error()
		} else {
			report = result
		}
	}
	return d.Finish(ctx, job.EventID, token, status, records, report)
}

var _ io.Writer = (*bounded)(nil)
