package monitor

import (
	"context"
	"github.com/google/uuid"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestIntegrationUpgradePreservesLegacyIncident(t *testing.T) {
	if os.Getenv("RUN_GO_INTEGRATION") != "1" {
		t.Skip("isolated PostgreSQL required")
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	base, err := OpenStore(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	schema := "upgrade_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err = base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = base.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		pool, _ := base.DB()
		_ = pool.Close()
	})
	u, _ := url.Parse(cfg.Database)
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	db, err := OpenStore(u.String())
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	old, _ := migrations.ReadFile("migrations/001.sql")
	if err = db.Exec(string(old)).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("CREATE TABLE bid_mon_schema(version integer primary key,applied_at timestamptz NOT NULL); INSERT INTO bid_mon_schema VALUES(1,NOW())").Error; err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	reason := "实验故意制造的失败，无需继续处理"
	event := Event{EventID: uuid.NewString(), LogicalKey: identity("legacy", "rule"), TaskKey: identity("legacy", "task"), TaskID: "legacy-task", Episode: 3, RuleCode: "TASK_FAILED", RuleVersion: "v1", Status: "closed_unresolved", ConditionActive: true, Severity: "warning", DiagnosisStatus: "not_requested", Evidence: M{"preserved": true}, OpenedAt: at, LastSeenAt: at, ClosedAt: &at, CloseReason: &reason, Revision: 4}
	// The old table has no phase-two fields. Insert only its original columns.
	if err = db.Select("event_id", "logical_key", "task_key", "task_id", "episode", "rule_code", "rule_version", "status", "condition_active", "severity", "diagnosis_status", "evidence", "opened_at", "last_seen_at", "closed_at", "close_reason", "revision").Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	if err = Migrate(db.WithContext(context.Background())); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(db); err != nil {
		t.Fatal("repeat migration", err)
	}
	var actual Event
	if err = db.First(&actual, "event_id=?", event.EventID).Error; err != nil {
		t.Fatal(err)
	}
	if actual.LogicalKey != event.LogicalKey || actual.Episode != 3 || actual.Status != "closed_unresolved" || !actual.ConditionActive || actual.CloseReason == nil || *actual.CloseReason != reason || actual.Revision != 4 || actual.Evidence["preserved"] != true {
		t.Fatal("upgrade rewrote legacy incident")
	}
	var count int64
	if err = db.Table("bid_mon_schema").Count(&count).Error; err != nil || count != 4 {
		t.Fatal("ordered migrations incomplete", count, err)
	}
}
