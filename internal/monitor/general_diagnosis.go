package monitor

import (
	"bidmon/internal/checks"
	"bidmon/internal/evidence"
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"time"
)

func reportText(value string, limit int) string {
	value = Redact(strings.TrimSpace(value))
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func eventCheck(event Event) checks.CheckSpec {
	raw, _ := json.Marshal(event.Definition["check"])
	var spec checks.CheckSpec
	_ = json.Unmarshal(raw, &spec)
	return spec
}
func ReportForEvent(event Event, records Records, status string) M {
	if event.CheckID == "" {
		return RuleReport(event, records, status)
	}
	return GeneralRuleReport(event, records, status)
}
func GeneralRuleReport(event Event, records Records, status string) M {
	facts, causes := []any{}, []any{}
	missing := []string{}
	summary, category, certainty := "业务规则异常已记录；现有证据不足以确认原因", "insufficient_evidence", "insufficient_evidence"
	actions := []string{"检查对应业务记录及触发字段，核对异常是否持续存在。", "对照异常时间窗口查看相关服务日志及依赖响应；证据不足时补充日志再判断。"}
	spec := eventCheck(event)
	matchedCategories := map[string]bool{}
	matchedActions := []string{}
	seenActions := map[string]bool{}
	for _, record := range records {
		id := str(record, "evidence_id")
		if reason := str(record, "unavailable_reason"); reason != "" {
			missing = append(missing, reportText(str(record, "operation")+": "+reason, 1000))
			continue
		}
		if str(record, "operation") == "snapshot" {
			if len(facts) < 12 {
				facts = append(facts, M{"text": reportText("数据库观察触发检查 "+event.CheckID+" 的规则 "+event.RuleCode+"："+event.Title, 1000), "evidence_ids": []string{id}})
			}
			continue
		}
		content, _ := json.Marshal(record["content"])
		for _, rule := range spec.DiagnosticRules {
			pattern, err := regexp.Compile(rule.Pattern)
			if err != nil || !pattern.Match(content) {
				continue
			}
			if len(causes) >= 12 {
				break
			}
			category, certainty, summary = rule.Category, "supported_hypothesis", reportText(rule.Summary, 1000)
			if summary == "" {
				summary = "日志匹配诊断规则 " + rule.ID
			}
			matchedCategories[rule.Category] = true
			causes = append(causes, M{"text": reportText("日志匹配诊断规则："+summary, 1000), "evidence_ids": []string{id}})
			for _, action := range rule.Actions {
				action = reportText(action, 1000)
				if action != "" && !seenActions[action] && len(matchedActions) < 12 {
					matchedActions = append(matchedActions, action)
					seenActions[action] = true
				}
			}
		}
	}
	if len(matchedActions) > 0 {
		actions = matchedActions
	}
	if len(matchedCategories) > 1 {
		category, certainty, summary = "insufficient_evidence", "insufficient_evidence", "日志同时匹配多个原因类别，现有证据不能确认单一原因；请核对各项假设与缺失证据"
	}
	if status != "completed" {
		category, certainty, summary = "diagnosis_unavailable", "insufficient_evidence", "取证未在预算内完成；业务异常仍保留"
		causes = []any{}
	}
	for i, a := range actions {
		actions[i] = reportText(a, 1000)
	}
	return M{"event_id": event.EventID, "task_id": event.TaskID, "check_id": event.CheckID, "entity_key": event.EntityKey, "diagnosis_mode": "rule_based", "diagnosis_status": status, "business_recovery": "not_confirmed", "category": category, "certainty": certainty, "summary": summary, "facts": facts, "causes": causes, "missing_evidence": missing, "recommended_actions": actions, "generated_at": stamp(time.Now()), "rule_version": event.RuleVersion, "report_version": 2, "runbook": reportText(str(event.Definition, "runbook_text"), 16000)}
}
func genericSnapshot(event Event) M {
	r := evidence.ProfileRequest{Version: 2, EventID: event.EventID, CheckID: event.CheckID, ResourceID: event.EntityKey, Operation: "snapshot"}
	result := M(evidence.Snapshot(r, map[string]any(event.Evidence), time.Now()))
	result["task_id"] = event.TaskID
	return result
}
func collectGeneric(ctx context.Context, cfg Config, event Event, operation string) M {
	if operation == "details" {
		return collectGenericDetails(ctx, cfg, event)
	}
	spec := eventCheck(event)
	r := evidence.ProfileRequest{Version: 2, EventID: event.EventID, CheckID: event.CheckID, ResourceID: event.EntityKey, ProfileID: spec.EvidenceProfile, Operation: operation}
	fail := func(reason string) M {
		out := M(evidence.ProfileResult(r, nil, reason, false, time.Now()))
		out["task_id"] = event.TaskID
		return out
	}
	if spec.EvidenceProfile == "" {
		return fail("logs_not_configured")
	}
	path := str(event.Definition, "evidence_config_file")
	if path == "" {
		path = os.Getenv("MON_EVIDENCE_CONFIG_FILE")
	}
	c, err := LoadEvidenceConfig(path)
	if err != nil {
		return fail("evidence_config_unavailable")
	}
	target, profile, err := c.Resolve(event.CheckID, spec.EvidenceProfile)
	if err != nil {
		return fail("evidence_profile_denied")
	}
	r.TargetID, r.ServiceID = profile.TargetID, profile.ServiceID
	result := CollectProfile(ctx, target, r)
	result["task_id"] = event.TaskID
	return result
}
