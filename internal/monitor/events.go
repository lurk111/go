package monitor

import (
	"errors"
	"fmt"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

func audit(tx *gorm.DB, id, action string, details M) error {
	actor := "monitor"
	if action == "manually_closed" {
		actor = "lab-operator"
	}
	return tx.Create(&Audit{ID: uuid.NewString(), EventID: id, Action: action, Actor: actor, At: time.Now().UTC(), Details: details}).Error
}
func enqueue(tx *gorm.DB, e Event, kind string, report M) error {
	recovery := "not_confirmed"
	if kind == "resolved" {
		recovery = "confirmed"
	}
	summary := map[string]string{"initial": "发现任务规则异常", "resolved": "已依据业务观察确认解除", "closed": "已人工关闭，不表示业务恢复", "diagnosis": "诊断报告已更新"}[kind]
	if e.CheckID != "" && kind == "initial" {
		summary = e.Title
	}
	p := M{"event_id": e.EventID, "revision": e.Revision, "task_id": e.TaskID, "rule_code": e.RuleCode, "episode": e.Episode, "kind": kind, "status": e.Status, "channel": "local_sink", "diagnosis_mode": "not_requested", "business_recovery": recovery, "observed_at": stamp(e.LastSeenAt), "summary": summary}
	if e.CheckID != "" {
		p["check_id"], p["entity_key"], p["title"] = e.CheckID, e.EntityKey, e.Title
	}
	if len(report) > 0 {
		for _, k := range []string{"diagnosis_mode", "summary", "diagnosis_status", "category", "certainty"} {
			p[k] = report[k]
		}
		p["report_path"] = "/internal/events/" + e.EventID + "/report"
	}
	if e.CheckID != "" {
		channels, _ := e.Definition["channels"].([]any)
		for _, value := range channels {
			channel, ok := value.(map[string]any)
			if !ok {
				if m, yes := value.(M); yes {
					channel = map[string]any(m)
				} else {
					continue
				}
			}
			id, _ := channel["id"].(string)
			if id == "" {
				continue
			}
			payload := M{}
			for k, v := range p {
				payload[k] = v
			}
			payload["channel"] = id
			payload["destination_config"] = channel
			payload["idempotency_key"] = e.EventID + ":" + fmt.Sprint(e.Revision) + ":" + id
			if err := tx.Create(&Outbox{ID: uuid.NewString(), EventID: e.EventID, Revision: e.Revision, Destination: id, Kind: kind, Payload: payload, Status: "pending", NextRetryAt: time.Now().UTC()}).Error; err != nil {
				return err
			}
		}
		return nil
	}
	return tx.Create(&Outbox{ID: uuid.NewString(), EventID: e.EventID, Revision: e.Revision, Destination: "local_sink", Kind: kind, Payload: p, Status: "pending", NextRetryAt: time.Now().UTC()}).Error
}
func apply(tx *gorm.DB, s Snapshot, rule string, signal *bool, counts M, needed int, now time.Time) error {
	return applyConfigured(tx, s, rule, signal, counts, needed, needed, now, M{})
}
func applyConfigured(tx *gorm.DB, s Snapshot, rule string, signal *bool, counts M, triggerNeeded, recoveryNeeded int, now time.Time, definition M) error {
	logical := identity(s.Key, rule)
	var e Event
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("logical_key=? AND condition_active", logical).First(&e).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	found := err == nil
	if signal != nil && *signal && found {
		e.LastSeenAt = now
		e.Evidence = s.Evidence
	}
	if !found && signal != nil && *signal && int(num(counts, "bad")) >= triggerNeeded {
		var last int
		if err = tx.Model(&Event{}).Select("COALESCE(MAX(episode),0)").Where("logical_key=?", logical).Scan(&last).Error; err != nil {
			return err
		}
		e = Event{EventID: uuid.NewString(), LogicalKey: logical, TaskKey: s.Key, TaskID: s.TaskID, Episode: last + 1, RuleCode: rule, RuleVersion: "v1", Status: "open", ConditionActive: true, Severity: "warning", DiagnosisStatus: "not_requested", Evidence: s.Evidence, OpenedAt: now, LastSeenAt: now, Revision: 1}
		if len(definition) > 0 {
			e.CheckID = str(definition, "check_id")
			e.EntityKey = s.TaskID
			e.Title = str(definition, "title")
			e.Definition = definition
			e.Severity = str(definition, "severity")
			e.RuleVersion = str(definition, "rule_version")
		}
		if err = tx.Create(&e).Error; err != nil {
			return err
		}
		if err = audit(tx, e.EventID, "opened", M{"implementation": "go", "rule_version": e.RuleVersion}); err != nil {
			return err
		}
		return enqueue(tx, e, "initial", nil)
	}
	if found && signal != nil && !*signal && int(num(counts, "good")) >= recoveryNeeded {
		e.ConditionActive = false
		if err = audit(tx, e.EventID, "condition_cleared", nil); err != nil {
			return err
		}
		if e.Status == "open" {
			e.Status = "resolved"
			e.ClosedAt = &now
			e.LastSeenAt = now
			e.Revision++
			if err = enqueue(tx, e, "resolved", nil); err != nil {
				return err
			}
		}
	}
	if found {
		return tx.Save(&e).Error
	}
	return nil
}
func CloseEvent(db *gorm.DB, c Config, id, reason string) (Event, error) {
	var e Event
	reason = strings.TrimSpace(reason)
	if reason == "" || len([]rune(reason)) > 1000 {
		return e, errors.New("reason_required")
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := scoped(tx, c).Clauses(clause.Locking{Strength: "UPDATE"}).First(&e, "event_id=?", id).Error; err != nil {
			return err
		}
		if e.Status != "open" {
			return nil
		}
		now := time.Now().UTC()
		reason = Redact(reason)
		e.Status = "closed_unresolved"
		e.CloseReason = &reason
		e.ClosedAt = &now
		e.Revision++
		if err := tx.Model(&Job{}).Where("event_id=? AND status IN ?", id, []string{"queued", "running"}).Updates(map[string]any{"status": "canceled", "finished_at": now}).Error; err != nil {
			return err
		}
		if e.DiagnosisStatus == "queued" || e.DiagnosisStatus == "running" {
			e.DiagnosisStatus = "canceled"
		}
		if err := tx.Model(&Outbox{}).Where("event_id=? AND status IN ?", id, []string{"pending", "retry", "sending"}).Update("status", "canceled").Error; err != nil {
			return err
		}
		if err := tx.Save(&e).Error; err != nil {
			return err
		}
		if err := audit(tx, id, "manually_closed", M{"reason": reason, "actor": "administrator"}); err != nil {
			return err
		}
		return enqueue(tx, e, "closed", nil)
	})
	return e, err
}
