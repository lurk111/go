package monitor

import (
	"bidmon/internal/checks"
	"bidmon/internal/evidence"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func generalDiagnosisFixture() (Event, Records) {
	spec := checks.CheckSpec{ID: "order-callback", EvidenceProfile: "order-logs", DiagnosisMode: "hybrid", AllowModel: true, DiagnosticRules: []checks.DiagnosticRuleSpec{{ID: "callback-timeout", Pattern: "callback_timeout", Category: "callback_timeout", Summary: "回调日志提示请求超时", Actions: []string{"核对目标服务响应耗时和请求超时配置。"}}}}
	raw, _ := json.Marshal(spec)
	var definition M
	_ = json.Unmarshal(raw, &definition)
	event := Event{EventID: uuid.NewString(), TaskID: "order-one", EntityKey: "order-one", CheckID: spec.ID, RuleCode: "callback-failed", RuleVersion: "v2", Title: "订单回调连续失败 token=title-private", Evidence: M{"fields": M{"status": "failed", "password": "snapshot-private"}}, Definition: M{"check": definition, "runbook_text": "核对 callback 服务 token=runbook-private"}}
	snapshot := genericSnapshot(event)
	r := evidence.ProfileRequest{Version: 2, EventID: event.EventID, CheckID: event.CheckID, ResourceID: event.EntityKey, ProfileID: spec.EvidenceProfile, TargetID: "app-a", ServiceID: "callback-worker", Operation: "logs"}
	log := M(evidence.ProfileResult(r, M{"records": []any{M{"message": "callback_timeout token=log-private"}}}, "", false, time.Now()))
	log["task_id"] = event.TaskID
	return event, Records{snapshot, log}
}

func cloneReport(report M) M {
	raw, _ := json.Marshal(report)
	var copy M
	_ = json.Unmarshal(raw, &copy)
	return copy
}

func TestGeneralRuleReportIdentityAndRedaction(t *testing.T) {
	event, records := generalDiagnosisFixture()
	report := GeneralRuleReport(event, records, "completed")
	if report["category"] != "callback_timeout" || report["certainty"] != "supported_hypothesis" || report["business_recovery"] != "not_confirmed" {
		t.Fatal(report)
	}
	if err := ValidateReport(report, event, records); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(M{"report": report, "evidence": records})
	for _, secret := range []string{"title-private", "snapshot-private", "log-private", "runbook-private"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("generic report/evidence leaked %s", secret)
		}
	}
	for _, field := range []string{"check_id", "entity_key", "rule_version"} {
		copy := cloneReport(report)
		copy[field] = "other"
		if err := ValidateReport(copy, event, records); err == nil {
			t.Fatalf("report %s mismatch accepted", field)
		}
	}
	for _, field := range []string{"check_id", "resource_id", "profile_id", "source", "operation", "evidence_id"} {
		copied := Records{records[0], cloneReport(records[1])}
		copied[1][field] = "other"
		if err := ValidateReport(report, event, copied); err == nil {
			t.Fatalf("evidence %s mismatch accepted", field)
		}
	}
	duplicate := Records{records[0], records[1], records[1]}
	if err := ValidateReport(report, event, duplicate); err == nil {
		t.Fatal("duplicate evidence IDs accepted")
	}
	for _, field := range []string{"category", "certainty", "summary", "facts", "causes", "recommended_actions", "missing_evidence"} {
		copy := cloneReport(report)
		if field == "category" {
			copy[field] = "model_timeout" // An allowed category still cannot replace the rule.
		} else if field == "certainty" {
			copy[field] = "confirmed_by_error"
		} else {
			copy[field] = "forged conclusion"
		}
		if err := ValidateReport(copy, event, records); err == nil {
			t.Fatalf("generic rule field %s could be overwritten", field)
		}
	}
}

func TestGeneralRuleReportConflictingAndMissingEvidence(t *testing.T) {
	event, records := generalDiagnosisFixture()
	spec := eventCheck(event)
	spec.DiagnosticRules = append(spec.DiagnosticRules, checks.DiagnosticRuleSpec{ID: "dependency", Pattern: "callback_timeout", Category: "dependency_unavailable", Summary: "依赖可能不可用", Actions: []string{"查看依赖服务状态。"}})
	raw, _ := json.Marshal(spec)
	var definition M
	_ = json.Unmarshal(raw, &definition)
	event.Definition["check"] = definition
	report := GeneralRuleReport(event, records, "completed")
	if report["category"] != "insufficient_evidence" || report["certainty"] != "insufficient_evidence" || len(report["causes"].([]any)) != 2 {
		t.Fatal("conflicting categories reduced to one cause", report)
	}
	if err := ValidateReport(report, event, records); err != nil {
		t.Fatal(err)
	}
	missing := cloneReport(records[1])
	missing["content"], missing["unavailable_reason"] = nil, "ssh_unavailable"
	records = Records{records[0], missing}
	report = GeneralRuleReport(event, records, "completed")
	if report["category"] != "insufficient_evidence" || len(report["missing_evidence"].([]string)) != 1 {
		t.Fatal(report)
	}
	if err := ValidateReport(report, event, records); err != nil {
		t.Fatal(err)
	}
	// A deadline may archive old evidence; collection-time host clock checks are
	// separate from validation of saved diagnostic reports.
	records[0]["collected_at"] = stamp(time.Now().Add(-10 * time.Minute))
	report = GeneralRuleReport(event, records, "timed_out")
	if err := ValidateReport(report, event, records); err != nil || report["category"] != "diagnosis_unavailable" {
		t.Fatal(report, err)
	}
}

func TestGeneralModelHypothesesPreserveRules(t *testing.T) {
	event, records := generalDiagnosisFixture()
	base := GeneralRuleReport(event, records, "completed")
	for _, tc := range []struct {
		name, answer, want string
	}{
		{"hypothesis", `{"analysis":"可能为目标服务响应过慢，当前证据不足以确认是否恢复。 token=test-model-key","evidence_ids":["E2"],"recommended_actions":["人工核对依赖响应耗时，token=test-model-key"]}`, ""},
		{"forged_reference", `{"analysis":"原因假设","evidence_ids":["unknown"],"recommended_actions":["检查日志"]}`, "model_reference_invalid"},
		{"rewrite_category", `{"analysis":"已确认","evidence_ids":["E2"],"recommended_actions":["检查日志"],"category":"database_failed"}`, "model_report_invalid"},
		{"rewrite_recovery", `{"analysis":"已恢复","evidence_ids":["E2"],"recommended_actions":["检查日志"],"business_recovery":"confirmed"}`, "model_report_invalid"},
		{"blank_analysis", `{"analysis":"   ","evidence_ids":["E2"],"recommended_actions":["检查日志"]}`, "model_report_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var invalidInput atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload M
				_ = json.NewDecoder(r.Body).Decode(&payload)
				input, prompt := str(payload, "input"), str(payload, "instructions")
				if strings.Contains(input, "private") || !strings.Contains(prompt, "假设") || strings.Contains(prompt, "售前任务") || payload["store"] != false {
					invalidInput.Store(true)
				}
				_ = json.NewEncoder(w).Encode(M{"status": "completed", "output": []any{M{"type": "message", "content": []any{M{"type": "output_text", "text": tc.answer}}}}})
			}))
			defer server.Close()
			result, err := ModelReport(context.Background(), ModelConfig{Model: "test-model", BaseURL: server.URL, APIKey: "test-model-key"}, event, records, base)
			if tc.want != "" {
				if err == nil || err.Error() != tc.want {
					t.Fatalf("expected %s, got %v", tc.want, err)
				}
				return
			}
			if err != nil || invalidInput.Load() {
				t.Fatal(err, "input boundary invalid", invalidInput.Load())
			}
			if result["model_assessment_type"] != "hypothesis" || result["category"] != base["category"] || result["certainty"] != base["certainty"] || result["business_recovery"] != "not_confirmed" {
				t.Fatal("model changed rule authority", result)
			}
			if err := ValidateReport(result, event, records); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(result)
			if strings.Contains(string(raw), "test-model-key") {
				t.Fatal("model key echoed into output")
			}
			result["model_assessment_type"] = "confirmed"
			if err := ValidateReport(result, event, records); err == nil {
				t.Fatal("model hypothesis upgraded to confirmed")
			}
		})
	}
	var called atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called.Store(true) }))
	defer server.Close()
	wrongRecords := Records{records[0], cloneReport(records[1])}
	wrongRecords[1]["resource_id"] = "other-order"
	if _, err := ModelReport(context.Background(), ModelConfig{BaseURL: server.URL, APIKey: "test-key"}, event, wrongRecords, base); err == nil || called.Load() {
		t.Fatal("cross-business evidence reached model provider", err)
	}
}
