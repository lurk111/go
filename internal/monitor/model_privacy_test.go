package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelInputRedactsBareCredentialAndPreservesNumbers(t *testing.T) {
	for _, key := range []string{"bare-relay-credential", "bare-quote\"credential\\suffix"} {
		t.Run(key, func(t *testing.T) {
			event := Event{EventID: "event", TaskID: "task", RuleCode: "TASK_FAILED"}
			var records Records
			raw, _ := json.Marshal(Records{{"event_id": "event", "task_id": "task", "evidence_id": "ev1", "operation": "task_snapshot", "content": M{"message": "Log captured: " + key, "count": int64(9007199254740993)}}})
			if err := records.Scan(raw); err != nil {
				t.Fatal(err)
			}
			if obj(records[0], "content")["count"] != json.Number("9007199254740993") {
				t.Fatal("persisted integer changed")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body M
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Fatal("request invalid")
				}
				input := str(body, "input")
				encoded, _ := json.Marshal(key)
				if strings.Contains(input, key) || strings.Contains(input, string(encoded[1:len(encoded)-1])) {
					t.Error("bare credential in model evidence")
				}
				if !strings.Contains(input, "9007199254740993") {
					t.Error("model input lost integer precision")
				}
				answer := `{"analysis":"仅有失败快照，根因需人工核对。","evidence_ids":["E1"],"recommended_actions":["核对关联日志与业务状态。"]}`
				_ = json.NewEncoder(w).Encode(M{"status": "completed", "output": []any{M{"type": "message", "content": []any{M{"type": "output_text", "text": answer}}}}})
			}))
			defer server.Close()
			if _, err := ModelReport(context.Background(), ModelConfig{BaseURL: server.URL, APIKey: key, Model: "test"}, event, records, RuleReport(event, records, "completed")); err != nil {
				t.Fatal(err)
			}
		})
	}
}
