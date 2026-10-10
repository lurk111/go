package monitor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestModelAdapter(t *testing.T) {
	e := Event{EventID: "event", TaskID: "task", RuleCode: "TASK_FAILED"}
	records := Records{{"event_id": "event", "task_id": "task", "evidence_id": "ev1", "operation": "task_snapshot", "content": M{"status": "failed", "api_key": "never-send-this"}}}
	for _, tc := range []struct {
		name   string
		status int
		output string
		want   string
	}{
		{"valid", 200, `{"analysis":"观察到失败，缺少根因证据。","evidence_ids":["ev1"],"recommended_actions":["检查同一任务的错误日志。"]}`, ""},
		{"forged_reference", 200, `{"analysis":"错误","evidence_ids":["fake"],"recommended_actions":["检查日志"]}`, "model_reference_invalid"},
		{"empty_actions", 200, `{"analysis":"错误","evidence_ids":["ev1"],"recommended_actions":[]}`, "model_report_invalid"},
		{"extra_authority", 200, `{"analysis":"已恢复","evidence_ids":["ev1"],"recommended_actions":["检查"],"business_recovery":"confirmed"}`, "model_report_invalid"},
		{"invalid_json", 200, `not json`, "model_report_invalid"},
		{"authentication", 401, `secret-upstream-body`, "model_http_401"},
		{"rate_limit", 429, `secret-upstream-body`, "model_http_429"},
		{"upstream", 503, `secret-upstream-body`, "model_http_503"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Error("request contract")
				}
				var payload M
				if json.NewDecoder(r.Body).Decode(&payload) != nil {
					t.Error("invalid request")
				}
				if strings.Contains(str(payload, "input"), "never-send-this") || payload["store"] != false {
					t.Error("input privacy")
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(M{"status": "completed", "output": []any{M{"type": "message", "content": []any{M{"type": "output_text", "text": tc.output}}}}})
			}))
			defer srv.Close()
			base := RuleReport(e, records, "completed")
			got, err := ModelReport(context.Background(), ModelConfig{Model: "test", BaseURL: srv.URL + "/v1", APIKey: "test-key"}, e, records, base)
			if tc.want != "" {
				if err == nil || err.Error() != tc.want {
					t.Fatalf("expected %s, got %v", tc.want, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got["category"] != base["category"] || got["certainty"] != base["certainty"] || got["business_recovery"] != "not_confirmed" || got["diagnosis_mode"] != "model_assisted" {
				t.Fatal("authority changed")
			}
			if err = ValidateReport(got, e, records); err != nil {
				t.Fatal(err)
			}
			got["model_analysis"] = M{"text": "fabricated", "evidence_ids": []string{"unknown"}}
			if ValidateReport(got, e, records) == nil {
				t.Fatal("persisted model citation unchecked")
			}
		})
	}
}

func TestModelCancellationAndRedirect(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		reached := false
		destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if redirect {
				http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
			} else {
				_, _ = io.Copy(io.Discard, r.Body)
				select {
				case <-r.Context().Done():
				case <-time.After(time.Second):
				}
			}
		}))
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err := ModelReport(ctx, ModelConfig{BaseURL: srv.URL, APIKey: "test-key"}, Event{}, nil, M{})
		cancel()
		srv.Close()
		destination.Close()
		if err == nil || reached {
			t.Fatal("redirect or cancellation not enforced")
		}
	}
}

func TestActionRunbooks(t *testing.T) {
	seen := map[string]bool{}
	for _, category := range []string{"model_quota_exhausted", "model_rate_limited", "model_timeout", "extraction_failed", "worker_unavailable_suspect"} {
		actions := RecommendedActions(Event{}, category)
		if len(actions) < 2 || seen[actions[0]] {
			t.Fatal("missing specific advice")
		}
		seen[actions[0]] = true
	}
	if !strings.Contains(RecommendedActions(Event{RuleCode: "RESULT_EMPTY_SUSPECT"}, "insufficient_evidence")[0], "原文件") {
		t.Fatal("missing manual review")
	}
}
