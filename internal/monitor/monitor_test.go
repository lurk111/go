package monitor

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{Source: "test", Environment: "lab", Poll: time.Second, Queue: 3 * time.Second, Stale: 3 * time.Second, Grace: 2 * time.Second, Page: 2, Attempts: 3, DiagTimeout: 120 * time.Second, DiagConcurrency: 2}
}
func resultFixture(empty bool) string {
	d := M{}
	for _, p := range lists {
		a := []any{}
		if !empty && p == "qualification_review" {
			a = append(a, M{"item": "synthetic"})
		}
		switch p {
		case "business_requirements.items":
			d["business_requirements"] = M{"items": a}
		case "technical_requirements.items":
			d["technical_requirements"] = M{"items": a}
		default:
			d[p] = a
		}
	}
	instructions := M{}
	for _, k := range instructionFields {
		instructions[k] = M{"value": nil}
	}
	d["bidder_instructions"] = instructions
	raw, _ := json.Marshal(d)
	return string(raw)
}
func TestResultSummary(t *testing.T) {
	for _, tc := range []struct{ name, raw, state string }{{"all_empty", resultFixture(true), "empty"}, {"partial_content", resultFixture(false), "present"}, {"invalid_json", "{broken", "invalid"}, {"missing_fields", "{}", "invalid"}, {"null_root", "null", "invalid"}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := str(Summarize(tc.raw), "state"); got != tc.state {
				t.Fatalf("got %s", got)
			}
		})
	}
	for _, v := range []any{false, 0} {
		var d M
		_ = json.Unmarshal([]byte(resultFixture(true)), &d)
		obj(d, "bidder_instructions")["budget"] = M{"value": v}
		raw, _ := json.Marshal(d)
		if str(Summarize(string(raw)), "state") != "present" {
			t.Fatal("zero and false are content")
		}
	}
}
func TestRules(t *testing.T) {
	now := time.Now().UTC()
	c := testConfig()
	for _, tc := range []struct {
		name, status, rule string
		age                time.Duration
		state              string
		want               *bool
	}{{"failed_immediate", "failed", "TASK_FAILED", 0, "", bit(true)}, {"normal_not_failed", "completed", "TASK_FAILED", 0, "present", bit(false)}, {"cancel_not_recovery", "canceled", "TASK_FAILED", 0, "", nil}, {"queue_within", "pending", "QUEUE_DELAY", time.Second, "", bit(false)}, {"queue_over", "pending", "QUEUE_DELAY", 5 * time.Second, "", bit(true)}, {"stale", "running", "EXECUTION_STALE", 5 * time.Second, "", bit(true)}, {"missing_after_grace", "completed", "RESULT_MISSING", 5 * time.Second, "missing", bit(true)}, {"missing_in_grace", "completed", "RESULT_MISSING", time.Second, "missing", nil}, {"empty_candidate", "completed", "RESULT_EMPTY_SUSPECT", 5 * time.Second, "empty", bit(true)}, {"invalid_not_empty", "completed", "RESULT_EMPTY_SUSPECT", 5 * time.Second, "invalid", nil}, {"duplicate_structure", "completed", "RESULT_STRUCTURE_INVALID", 5 * time.Second, "ambiguous", bit(true)}, {"unknown_status", "mystery", "SOURCE_STATE_UNKNOWN", 0, "", bit(true)}} {
		t.Run(tc.name, func(t *testing.T) {
			r := Evaluate(Task{Status: tc.status, Created: now.Add(-tc.age), Updated: now.Add(-tc.age)}, M{"state": tc.state}, now, c, nil)[tc.rule]
			if (r == nil) != (tc.want == nil) || r != nil && *r != *tc.want {
				t.Fatal("wrong signal")
			}
		})
	}
	r := Evaluate(Task{Status: "running", Stage: "extract", Updated: now}, nil, now, c, M{"status": "running", "stage": "extract"})
	if r["EXECUTION_STALE"] != nil {
		t.Fatal("timestamp alone is not progress")
	}
}
func TestConfirmationGap(t *testing.T) {
	state := advance(nil, bit(true))
	state = advance(state, nil)
	state = advance(state, bit(true))
	if num(state, "bad") != 1 {
		t.Fatal("gap bridged")
	}
	state = advance(state, bit(false))
	if num(state, "bad") != 0 || num(state, "good") != 1 {
		t.Fatal("wrong recovery")
	}
}
func TestPythonIdentity(t *testing.T) {
	if identity("presales-lab", "lab", "sample-task") != "47e04173857115c8669d9eb7eb8f308e3317c4bf03477e5e4603e34d09490ecd" {
		t.Fatal("identity changed", identity("presales-lab", "lab", "sample-task"))
	}
}
func TestRuleDiagnosticAndValidation(t *testing.T) {
	e := Event{EventID: "event", TaskID: "task", RuleCode: "TASK_FAILED"}
	for _, tc := range []struct{ code, category string }{{"insufficient_quota", "model_quota_exhausted"}, {"rate_limit_exceeded", "model_rate_limited"}, {"request_timeout", "model_timeout"}, {"429", "insufficient_evidence"}} {
		t.Run(tc.code, func(t *testing.T) {
			r := Records{{"evidence_id": "ev1", "event_id": "event", "task_id": "task", "operation": "model_error", "content": M{"records": []any{map[string]any{"provider": "synthetic-provider", "error_code": tc.code}}}}}
			report := RuleReport(e, r, "completed")
			if str(report, "category") != tc.category {
				t.Fatal(report)
			}
			if err := ValidateReport(report, e, r); err != nil {
				t.Fatal(err)
			}
			report["facts"] = []any{M{"text": "fake", "evidence_ids": []string{"fake-id"}}}
			if ValidateReport(report, e, r) == nil {
				t.Fatal("accepted fabricated evidence")
			}
			report = RuleReport(e, r, "completed")
			report["business_recovery"] = "confirmed"
			if ValidateReport(report, e, r) == nil {
				t.Fatal("accepted fake recovery")
			}
		})
	}
}
func TestSSHUnavailableIsEvidence(t *testing.T) {
	c := testConfig()
	c.BindingKey = "/no-such-key"
	r := CollectSSH(context.Background(), c, Event{EventID: "e", TaskID: "t"}, "task_logs")
	if str(r, "unavailable_reason") == "" {
		t.Fatal("missing unavailable state")
	}
}
