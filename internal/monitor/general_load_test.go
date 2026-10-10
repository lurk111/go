package monitor

import (
	"bidmon/internal/checks"
	"testing"
	"time"
)

func TestGeneralIntegrationSnapshotLoad200(t *testing.T) {
	f, id, doc := generalFixture(t)
	spec := callbackCheck("callback-load", id)
	spec.Query = "SELECT order_id,callback_state,retry_count,updated_at FROM callback_orders WHERE order_id LIKE '" + id + "%' ORDER BY order_id"
	spec.Rules[0].Confirmations = 2
	spec.Rules[0].RecoveryConfirmations = 2
	doc.Checks = []checks.CheckSpec{spec}
	runtime := startGeneral(t, f, doc)
	now := time.Now().UTC().Truncate(time.Microsecond)
	tx, err := f.writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		object := id + "-" + time.Duration(i).String()
		if _, err = tx.Exec("INSERT INTO callback_orders(order_id,callback_state,retry_count,created_at,updated_at) VALUES(?,?,?,?,?)", object, "failed", 3, now, now); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	begin := time.Now()
	scanGeneral(t, runtime, 2)
	var active int64
	if err = f.db.Model(&Event{}).Where("check_id=? AND condition_active", spec.ID).Count(&active).Error; err != nil || active != 200 {
		t.Fatal("missing or duplicate load incidents", active, err)
	}
	triggerMS := time.Since(begin).Milliseconds()
	if _, err = f.writer.Exec("UPDATE callback_orders SET callback_state='delivered',updated_at=UTC_TIMESTAMP(6) WHERE order_id LIKE ?", id+"%"); err != nil {
		t.Fatal(err)
	}
	begin = time.Now()
	scanGeneral(t, runtime, 2)
	var recovered int64
	if err = f.db.Model(&Event{}).Where("check_id=? AND status='resolved' AND NOT condition_active", spec.ID).Count(&recovered).Error; err != nil || recovered != 200 {
		t.Fatal("load recovery lost", recovered, err)
	}
	t.Logf("load_conditions entities=200 checks=1 confirmations=2 recovery_confirmations=2 observations=800 trigger_total_ms=%d recovery_total_ms=%d notifications=disabled mysql_readonly=true", triggerMS, time.Since(begin).Milliseconds())
}
