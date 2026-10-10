package monitor

import (
	"bidmon/internal/checks"
	"bidmon/internal/evidence"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestGeneralIntegrationFrozenDetailEvidence(t *testing.T) {
	f, id, doc := generalFixture(t)
	spec := callbackCheck("callback-detail", id)
	spec.DetailQuery = "SELECT order_id,callback_state,retry_count,'token=fixture-secret' AS error_message FROM callback_orders WHERE order_id=:id"
	doc.Checks = []checks.CheckSpec{spec}
	runtime := startGeneral(t, f, doc)
	putCallback(t, f, id, "failed", 3)
	scanGeneral(t, runtime, 5)
	event := generalEvents(t, f, spec.ID, id)[0]
	result := collectGenericDetails(f.ctx, f.cfg, event)
	if result["unavailable_reason"] != nil {
		t.Fatal(result["unavailable_reason"])
	}
	req := evidence.ProfileRequest{Version: 2, EventID: event.EventID, CheckID: spec.ID, ResourceID: id, Operation: "details"}
	if err := evidence.ValidateProfileResult(evidence.M(result), req, time.Now()); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result["content"])
	if strings.Contains(string(raw),"fixture-secret"){t.Fatal("detail evidence skipped credential redaction")}
	var rows []M
	if json.Unmarshal(raw, &rows) != nil || len(rows) != 1 || rows[0]["order_id"] != id {
		t.Fatal("detail scope mixed")
	}
	records := Records{genericSnapshot(event), result}
	if err := ValidateReport(GeneralRuleReport(event, records, "completed"), event, records); err != nil {
		t.Fatal(err)
	}
	newer, _ := runtime.Current()
	newer.Checks[0].DetailQuery = "SELECT order_id FROM callback_orders WHERE order_id='unrelated'"
	if err := runtime.Install(f.ctx, newer, "test"); err != nil {
		t.Fatal(err)
	}
	if collectGenericDetails(f.ctx, f.cfg, event)["unavailable_reason"] != nil {
		t.Fatal("detail query was changed by current configuration")
	}
}
