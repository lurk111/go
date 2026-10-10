package monitor

import (
	"bidmon/internal/checks"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Every test uses the existing isolated PostgreSQL schema and unique MySQL
// business identifiers. Business reads run through the SELECT-only source DSN.
func generalFixture(t *testing.T) (*fixture, string, *checks.Document) {
	t.Helper()
	f := fixtureFor(t)
	if _, err := f.writer.Exec(`CREATE TABLE IF NOT EXISTS callback_orders (
  order_id VARCHAR(64) PRIMARY KEY,
  callback_state VARCHAR(32) NOT NULL,
  retry_count INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  INDEX callback_orders_updated (updated_at, order_id)
)`); err != nil {
		t.Fatal(err)
	}
	id := "general-" + uuid.NewString()
	t.Setenv("GENERAL_TEST_MYSQL_DSN", f.cfg.MySQL)
	doc := &checks.Document{Version: 1, Sources: []checks.SourceSpec{{ID: "callback-source", Driver: "mysql", DSN: checks.SecretRef{Env: "GENERAL_TEST_MYSQL_DSN"}}}}
	t.Cleanup(func() {
		if _, err := f.writer.Exec("DELETE FROM callback_orders WHERE order_id LIKE ?", id+"%"); err != nil {
			t.Errorf("test callback rows cleanup failed: %v", err)
		}
	})
	return f, id, doc
}

func callbackCheck(checkID, objectID string) checks.CheckSpec {
	return checks.CheckSpec{
		ID: checkID, Source: "callback-source", Mode: "entity", Environment: "integration", Interval: "1s",
		Query:    "SELECT order_id,callback_state,retry_count,updated_at FROM callback_orders WHERE order_id='" + objectID + "'",
		Identity: []string{"id"},
		Fields: map[string]checks.FieldSpec{
			"id":      {Column: "order_id", Type: "string", Required: true},
			"status":  {Column: "callback_state", Type: "string", Required: true},
			"retries": {Column: "retry_count", Type: "int", Required: true},
			"updated": {Column: "updated_at", Type: "timestamp", Required: true},
		},
		Rules: []checks.RuleSpec{{ID: "callback-failed", Version: "v1", Trigger: `fields.status == "failed" && fields.retries >= 3`, Recover: `fields.status == "delivered"`, Confirmations: 5, RecoveryConfirmations: 4, Severity: "critical", Summary: "订单回调连续失败"}},
	}
}

func startGeneral(t *testing.T, f *fixture, doc *checks.Document) *GeneralRuntime {
	t.Helper()
	f.cfg.Document, f.cfg.Model = doc, nil
	runtime, err := NewGeneralRuntime(f.ctx, f.db, f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.cfg.Runtime = runtime
	runtime.Cfg = f.cfg
	t.Cleanup(runtime.Close)
	return runtime
}

func putCallback(t *testing.T, f *fixture, id, status string, retries int) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := f.writer.Exec(`INSERT INTO callback_orders(order_id,callback_state,retry_count,created_at,updated_at) VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE callback_state=VALUES(callback_state),retry_count=VALUES(retry_count),updated_at=VALUES(updated_at)`, id, status, retries, now, now); err != nil {
		t.Fatal(err)
	}
}

func scanGeneral(t *testing.T, runtime *GeneralRuntime, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		// Advance scheduling eligibility without sleeping. Each Scan still performs
		// a real read-only SQL query and persists a distinct observation.
		runtime.scanMu.Lock()
		for _, p := range runtime.plans {
			p.Next = time.Time{}
		}
		runtime.scanMu.Unlock()
		if err := runtime.Scan(context.Background()); err != nil {
			t.Fatal(err)
		}
		for _, health := range runtime.Health() {
			if health.Error != "" {
				t.Fatalf("check %s failed: %s", health.ID, health.Error)
			}
		}
	}
}

func generalEvents(t *testing.T, f *fixture, checkID, objectID string) []Event {
	t.Helper()
	rows := []Event{}
	if err := f.db.Where("check_id=? AND entity_key=?", checkID, objectID).Order("episode").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestGeneralIntegrationConfirmationRecoveryUnknownAndMissing(t *testing.T) {
	f, id, doc := generalFixture(t)
	doc.Checks = []checks.CheckSpec{callbackCheck("callback-lifecycle", id)}
	runtime := startGeneral(t, f, doc)
	putCallback(t, f, id, "failed", 3)
	scanGeneral(t, runtime, 4)
	if rows := generalEvents(t, f, doc.Checks[0].ID, id); len(rows) != 0 {
		t.Fatal("event opened before five confirmations", rows)
	}
	scanGeneral(t, runtime, 1)
	rows := generalEvents(t, f, doc.Checks[0].ID, id)
	if len(rows) != 1 || rows[0].Severity != "critical" || !rows[0].ConditionActive || rows[0].Status != "open" {
		t.Fatal("confirmed callback event missing", rows)
	}
	putCallback(t, f, id, "delivered", 3)
	scanGeneral(t, runtime, 3)
	if got := generalEvents(t, f, doc.Checks[0].ID, id)[0]; got.Status != "open" {
		t.Fatal("recovered before four observations", got)
	}
	if _, err := f.writer.Exec("DELETE FROM callback_orders WHERE order_id=?", id); err != nil {
		t.Fatal(err)
	}
	scanGeneral(t, runtime, 1)
	if got := generalEvents(t, f, doc.Checks[0].ID, id)[0]; got.Status != "open" || !got.ConditionActive {
		t.Fatal("disappearing business record incorrectly recovered", got)
	}
	putCallback(t, f, id, "processing", 3)
	scanGeneral(t, runtime, 4)
	if got := generalEvents(t, f, doc.Checks[0].ID, id)[0]; got.Status != "open" || !got.ConditionActive {
		t.Fatal("unknown decision incorrectly recovered", got)
	}
	putCallback(t, f, id, "delivered", 3)
	scanGeneral(t, runtime, 3)
	if got := generalEvents(t, f, doc.Checks[0].ID, id)[0]; got.Status != "open" {
		t.Fatal("confirmation sequence crossed a missing/unknown observation", got)
	}
	scanGeneral(t, runtime, 1)
	if got := generalEvents(t, f, doc.Checks[0].ID, id)[0]; got.Status != "resolved" || got.ConditionActive {
		t.Fatal("explicit callback recovery not confirmed", got)
	}
}

func TestGeneralIntegrationCheckIsolationAndAggregate(t *testing.T) {
	f, id, doc := generalFixture(t)
	one, two := callbackCheck("callback-one", id), callbackCheck("callback-two", id)
	aggregate := checks.CheckSpec{ID: "callback-backlog", Source: "callback-source", Environment: "integration", Mode: "aggregate", Interval: "1s", Query: "SELECT COUNT(*) AS failed_count FROM callback_orders WHERE callback_state='failed' AND order_id LIKE '" + id + "%'", Fields: map[string]checks.FieldSpec{"count": {Column: "failed_count", Type: "int", Required: true}}, Rules: []checks.RuleSpec{{ID: "backlog", Trigger: "fields.count >= 2", Recover: "fields.count == 0", Confirmations: 5, RecoveryConfirmations: 4}}}
	doc.Checks = []checks.CheckSpec{one, two, aggregate}
	runtime := startGeneral(t, f, doc)
	putCallback(t, f, id, "failed", 3)
	putCallback(t, f, id+"-other", "failed", 3)
	scanGeneral(t, runtime, 5)
	a, b, c := generalEvents(t, f, one.ID, id), generalEvents(t, f, two.ID, id), generalEvents(t, f, aggregate.ID, "aggregate")
	if len(a) != 1 || len(b) != 1 || len(c) != 1 || a[0].TaskKey == b[0].TaskKey || a[0].LogicalKey == b[0].LogicalKey || a[0].EventID == b[0].EventID {
		t.Fatalf("same entity across checks mixed: a=%v b=%v aggregate=%v", a, b, c)
	}
	putCallback(t, f, id, "delivered", 3)
	putCallback(t, f, id+"-other", "delivered", 3)
	scanGeneral(t, runtime, 4)
	for _, value := range []struct{ check, entity string }{{one.ID, id}, {two.ID, id}, {aggregate.ID, "aggregate"}} {
		if got := generalEvents(t, f, value.check, value.entity)[0]; got.Status != "resolved" {
			t.Fatal("entity/aggregate recovery failed", got)
		}
	}
}

func TestGeneralIntegrationRuleChangeResetsCandidate(t *testing.T) {
	f, id, doc := generalFixture(t)
	doc.Checks = []checks.CheckSpec{callbackCheck("callback-config", id)}
	runtime := startGeneral(t, f, doc)
	putCallback(t, f, id, "failed", 3)
	scanGeneral(t, runtime, 3)
	updated, oldVersion := runtime.Current()
	updated.Checks[0].Rules[0].Trigger = `fields.status == "failed" && fields.retries > 2`
	if err := runtime.Install(f.ctx, updated, "integration-operator"); err != nil {
		t.Fatal(err)
	}
	_, newVersion := runtime.Current()
	if newVersion == oldVersion {
		t.Fatal("configuration update did not record a new version")
	}
	scanGeneral(t, runtime, 4)
	if got := generalEvents(t, f, doc.Checks[0].ID, id); len(got) != 0 {
		t.Fatal("candidate confirmation reused across changed rule", got)
	}
	scanGeneral(t, runtime, 1)
	rows := generalEvents(t, f, doc.Checks[0].ID, id)
	if len(rows) != 1 || str(rows[0].Definition, "config_version") != newVersion {
		t.Fatal("new rule/configuration not frozen in incident", rows)
	}
	var activeVersions int64
	if err := f.db.Model(&ConfigVersion{}).Where("active", true).Count(&activeVersions).Error; err != nil || activeVersions != 1 {
		t.Fatal("active configuration uniqueness lost", activeVersions, err)
	}
}

func TestGeneralIntegrationQueryFailureBreaksConfirmation(t *testing.T) {
	f, id, doc := generalFixture(t)
	doc.Checks = []checks.CheckSpec{callbackCheck("callback-source-gap", id)}
	runtime := startGeneral(t, f, doc)
	putCallback(t, f, id, "failed", 3)
	scanGeneral(t, runtime, 4)
	broken, _ := runtime.Current()
	broken.Checks[0].Query = "SELECT order_id,callback_state,retry_count,updated_at FROM callback_orders_missing_for_test"
	if err := runtime.Install(f.ctx, broken, "integration-query-failure"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Scan(f.ctx); err != nil {
		t.Fatal(err)
	}
	health := runtime.Health()
	if len(health) != 1 || health[0].LastFailure == nil || health[0].Error == "" {
		t.Fatal("query failure not visible in check health", health)
	}
	if err := runtime.Install(f.ctx, doc, "integration-restore-query"); err != nil {
		t.Fatal(err)
	}
	scanGeneral(t, runtime, 4)
	if rows := generalEvents(t, f, doc.Checks[0].ID, id); len(rows) != 0 {
		t.Fatal("candidate observations bridged an unavailable source", rows)
	}
	scanGeneral(t, runtime, 1)
	if rows := generalEvents(t, f, doc.Checks[0].ID, id); len(rows) != 1 {
		t.Fatal(rows)
	}
	if err := runtime.Install(f.ctx, broken, "integration-query-failure-again"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Scan(f.ctx); err != nil {
		t.Fatal(err)
	}
	if got := generalEvents(t, f, doc.Checks[0].ID, id)[0]; got.Status != "open" || !got.ConditionActive {
		t.Fatal("source query failure incorrectly recovered event", got)
	}
}

func TestGeneralIntegrationManualCloseRestartAndRecurrence(t *testing.T) {
	f, id, doc := generalFixture(t)
	doc.Checks = []checks.CheckSpec{callbackCheck("callback-manual", id)}
	runtime := startGeneral(t, f, doc)
	putCallback(t, f, id, "failed", 3)
	scanGeneral(t, runtime, 5)
	rows := generalEvents(t, f, doc.Checks[0].ID, id)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	closed, err := CloseEvent(f.db, f.cfg, rows[0].EventID, "实验故意制造的失败，无需继续处理")
	if err != nil || closed.Status != "closed_unresolved" {
		t.Fatal(closed, err)
	}
	scanGeneral(t, runtime, 6)
	runtime.Close()
	restarted, err := NewGeneralRuntime(f.ctx, f.db, f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.cfg.Runtime, restarted.Cfg.Runtime = restarted, restarted
	t.Cleanup(restarted.Close)
	scanGeneral(t, restarted, 2)
	rows = generalEvents(t, f, doc.Checks[0].ID, id)
	if len(rows) != 1 || rows[0].Status != "closed_unresolved" || !rows[0].ConditionActive {
		t.Fatal("closed persistent failure reopened after restart", rows)
	}
	putCallback(t, f, id, "delivered", 3)
	scanGeneral(t, restarted, 4)
	rows = generalEvents(t, f, doc.Checks[0].ID, id)
	if rows[0].Status != "closed_unresolved" || rows[0].ConditionActive || rows[0].CloseReason == nil {
		t.Fatal("business recovery rewrote manual closure", rows)
	}
	putCallback(t, f, id, "failed", 3)
	scanGeneral(t, restarted, 5)
	rows = generalEvents(t, f, doc.Checks[0].ID, id)
	if len(rows) != 2 || rows[1].Episode != 2 || rows[1].Status != "open" {
		t.Fatal("recovered then recurring failure did not create a new episode", rows)
	}
}

func TestGeneralIntegrationPreviewDoesNotCreateIncidents(t *testing.T) {
	f, id, doc := generalFixture(t)
	doc.Checks = []checks.CheckSpec{callbackCheck("callback-preview", id)}
	runtime := startGeneral(t, f, doc)
	putCallback(t, f, id, "failed", 3)
	preview, before := runtime.Current()
	preview.Checks[0].Rules[0].Summary = "只用于预览的配置"
	for i := 0; i < 6; i++ {
		rows, err := runtime.Preview(f.ctx, preview, doc.Checks[0].ID)
		if err != nil || len(rows) != 1 || rows[0].CheckID != doc.Checks[0].ID || rows[0].EntityKey != id || rows[0].Quality != checks.QualityValid {
			t.Fatal(rows, err)
		}
	}
	for _, model := range []any{&Event{}, &Snapshot{}, &Job{}, &Outbox{}} {
		var count int64
		if err := f.db.Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("preview wrote monitoring state", fmt.Sprintf("%T", model), count, err)
		}
	}
	_, after := runtime.Current()
	if after != before {
		t.Fatal("preview activated configuration")
	}
}

func TestGeneralIntegrationNotificationChannelIsolation(t *testing.T) {
	f, id, doc := generalFixture(t)
	var mu sync.Mutex
	goodKeys, badCalls, invalidPayload := []string{}, 0, false
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload M
		err := json.NewDecoder(r.Body).Decode(&payload)
		key := r.Header.Get("Idempotency-Key")
		mu.Lock()
		invalidPayload = invalidPayload || err != nil || key == "" || payload["destination_config"] != nil || payload["check_id"] != "callback-routes"
		goodKeys = append(goodKeys, key)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		badCalls++
		mu.Unlock()
		w.Header().Set("Retry-After", "300")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	doc.Channels = []checks.ChannelSpec{{ID: "slow-route", Type: "webhook", URL: bad.URL}, {ID: "working-route", Type: "webhook", URL: good.URL}}
	check := callbackCheck("callback-routes", id)
	check.NotificationRoutes = []string{"slow-route", "working-route"}
	doc.Checks = []checks.CheckSpec{check}
	runtime := startGeneral(t, f, doc)
	putCallback(t, f, id, "failed", 3)
	scanGeneral(t, runtime, 5)
	dispatcher := Dispatcher{Cfg: f.cfg, DB: f.db}
	for i := 0; i < 2; i++ {
		if err := dispatcher.Deliver(f.ctx); err != nil {
			t.Fatal(err)
		}
	}
	putCallback(t, f, id, "delivered", 3)
	scanGeneral(t, runtime, 4)
	for i := 0; i < 3; i++ {
		if err := dispatcher.Deliver(f.ctx); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	keys, calls, invalid := append([]string{}, goodKeys...), badCalls, invalidPayload
	mu.Unlock()
	if len(keys) != 2 || keys[0] == keys[1] || calls != 1 || invalid {
		t.Fatalf("routes blocked or delivered invalid data: good=%v bad=%d invalid=%v", keys, calls, invalid)
	}
	var delivered, waiting int64
	if err := f.db.Model(&Outbox{}).Where("destination=? AND status='delivered'", "working-route").Count(&delivered).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&Outbox{}).Where("destination=? AND status IN ?", "slow-route", []string{"pending", "retry"}).Count(&waiting).Error; err != nil {
		t.Fatal(err)
	}
	if delivered != 2 || waiting != 2 || !strings.HasSuffix(keys[0], ":working-route") || !strings.HasSuffix(keys[1], ":working-route") {
		t.Fatal("outbox destinations or idempotency scope mixed", delivered, waiting, keys)
	}
}
