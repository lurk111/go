package monitor

import (
	"bidmon/internal/checks"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func generalHTTP(t *testing.T, server *Server, method, path string, payload any, authenticated bool) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if value, ok := payload.([]byte); ok {
		raw = value
	} else if payload != nil {
		var err error
		raw, err = json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	if authenticated {
		r.Header.Set("Authorization", "Bearer "+server.Cfg.Token)
	}
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	return w
}

func TestGeneralIntegrationHTTPInvalidConfigurationNeverActivates(t *testing.T) {
	f, id, doc := generalFixture(t)
	doc.Checks = []checks.CheckSpec{callbackCheck("http-config", id)}
	runtime := startGeneral(t, f, doc)
	server := &Server{Cfg: f.cfg, DB: f.db}
	_, before := runtime.Current()
	var beforeCount int64
	if err := f.db.Model(&ConfigVersion{}).Count(&beforeCount).Error; err != nil {
		t.Fatal(err)
	}
	invalid, _ := runtime.Current()
	invalid.Checks[0].Query = "DELETE FROM callback_orders"
	for _, payload := range []any{invalid, []byte(`{"version":1,"sources":[],"checks":[],"unexpected_field":true}`)} {
		for _, method := range []string{"PUT", "POST"} {
			path := "/internal/config"
			if method == "POST" {
				path += "/validate"
			}
			if response := generalHTTP(t, server, method, path, payload, true); response.Code != http.StatusBadRequest {
				t.Fatal("invalid configuration accepted", response.Code, response.Body.String())
			}
		}
	}
	valid, _ := runtime.Current()
	valid.Checks[0].Rules[0].Summary = "仅校验，不激活"
	if response := generalHTTP(t, server, "POST", "/internal/config/validate", valid, true); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	if response := generalHTTP(t, server, "PUT", "/internal/config", valid, false); response.Code != http.StatusUnauthorized {
		t.Fatal("unauthenticated update accepted", response.Code)
	}
	_, after := runtime.Current()
	var afterCount int64
	if err := f.db.Model(&ConfigVersion{}).Count(&afterCount).Error; err != nil || before != after || beforeCount != afterCount {
		t.Fatal("validation changed active/history configuration", before, after, beforeCount, afterCount, err)
	}
}

func TestGeneralIntegrationHTTPSourceAndMappingChangesResetCounts(t *testing.T) {
	for _, change := range []string{"source", "mapping"} {
		t.Run(change, func(t *testing.T) {
			f, id, doc := generalFixture(t)
			doc.Checks = []checks.CheckSpec{callbackCheck("http-semantic-change", id)}
			runtime := startGeneral(t, f, doc)
			server := &Server{Cfg: f.cfg, DB: f.db}
			putCallback(t, f, id, "failed", 3)
			scanGeneral(t, runtime, 4)
			updated, oldVersion := runtime.Current()
			if change == "source" {
				t.Setenv("GENERAL_TEST_ALTERNATE_DSN", f.cfg.MySQL)
				updated.Sources[0].DSN = checks.SecretRef{Env: "GENERAL_TEST_ALTERNATE_DSN"}
			} else {
				updated.Checks[0].Query = "SELECT order_id,callback_state,retry_count AS retries_alias,updated_at FROM callback_orders WHERE order_id='" + id + "'"
				field := updated.Checks[0].Fields["retries"]
				field.Column = "retries_alias"
				updated.Checks[0].Fields["retries"] = field
			}
			response := generalHTTP(t, server, "PUT", "/internal/config", updated, true)
			if response.Code != http.StatusOK {
				t.Fatal(response.Code, response.Body.String())
			}
			_, newVersion := runtime.Current()
			if newVersion == oldVersion {
				t.Fatal("semantic change did not create a new configuration version")
			}
			scanGeneral(t, runtime, 4)
			if rows := generalEvents(t, f, updated.Checks[0].ID, id); len(rows) != 0 {
				t.Fatal("candidate reused observations from another source/mapping", rows)
			}
			scanGeneral(t, runtime, 1)
			if rows := generalEvents(t, f, updated.Checks[0].ID, id); len(rows) != 1 {
				t.Fatal(rows)
			}
		})
	}
}

func TestGeneralIntegrationHTTPEncryptedSecretsAndRestart(t *testing.T) {
	f, id, doc := generalFixture(t)
	doc.Checks = []checks.CheckSpec{callbackCheck("http-stored-secret", id)}
	runtime := startGeneral(t, f, doc)
	server := &Server{Cfg: f.cfg, DB: f.db}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "test-encryption-key")
	if err := os.WriteFile(keyFile, key, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MON_SECRET_KEY_FILE", keyFile)
	value := "synthetic-only-password-" + uuid.NewString()
	response := generalHTTP(t, server, "PUT", "/internal/secrets/test-credential", M{"value": value}, true)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), value) {
		t.Fatal("secret write returned plaintext", response.Code)
	}
	var stored StoredSecret
	if err := f.db.First(&stored, "id=?", "test-credential").Error; err != nil || len(stored.Ciphertext) < 32 || bytes.Contains(stored.Ciphertext, []byte(value)) {
		t.Fatal("credential not encrypted at rest", err)
	}
	firstCiphertext := append([]byte{}, stored.Ciphertext...)
	if response := generalHTTP(t, server, "PUT", "/internal/secrets/test-credential", M{"value": value}, true); response.Code != http.StatusOK {
		t.Fatal(response.Code)
	}
	if err := f.db.First(&stored, "id=?", "test-credential").Error; err != nil || bytes.Equal(firstCiphertext, stored.Ciphertext) {
		t.Fatal("credential rotation reused encryption nonce", err)
	}
	response = generalHTTP(t, server, "GET", "/internal/secrets", nil, true)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), value) || strings.Contains(strings.ToLower(response.Body.String()), "ciphertext") || strings.Contains(response.Body.String(), base64.StdEncoding.EncodeToString(stored.Ciphertext)) {
		t.Fatal("secret list returned secret material", response.Code)
	}
	response = generalHTTP(t, server, "PUT", "/internal/secrets/callback-dsn", M{"value": f.cfg.MySQL}, true)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), f.cfg.MySQL) {
		t.Fatal("DSN secret write failed or disclosed value", response.Code)
	}
	updated, _ := runtime.Current()
	updated.Sources[0].DSN = checks.SecretRef{File: "stored:callback-dsn"}
	if response := generalHTTP(t, server, "PUT", "/internal/config", updated, true); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	for _, path := range []string{"/internal/config", "/internal/config/history", "/internal/secrets"} {
		response := generalHTTP(t, server, "GET", path, nil, true)
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), value) || strings.Contains(response.Body.String(), f.cfg.MySQL) || strings.Contains(strings.ToLower(response.Body.String()), "ciphertext") {
			t.Fatal("configuration/read endpoint leaked secrets", path, response.Code)
		}
	}
	restarted, err := NewGeneralRuntime(f.ctx, f.db, f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Close)
	if got, err := restarted.resolveSecret(checks.SecretRef{File: "stored:test-credential"}); err != nil || got != value {
		t.Fatal("restart could not decrypt persisted credential", err)
	}
	putCallback(t, f, id, "failed", 3)
	current, _ := restarted.Current()
	if rows, err := restarted.Preview(f.ctx, current, current.Checks[0].ID); err != nil || len(rows) != 1 {
		t.Fatal("stored DSN did not work after restart", err)
	}
	wrongKey := make([]byte, 32)
	if _, err := rand.Read(wrongKey); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, wrongKey, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := restarted.resolveSecret(checks.SecretRef{File: "stored:test-credential"}); err == nil || got != "" {
		t.Fatal("wrong key decrypted persisted credential")
	}
	if err := os.WriteFile(keyFile, key, 0600); err != nil {
		t.Fatal(err)
	}
	// Ciphertext is bound to its identifier as AES-GCM associated data.
	if err := f.db.Create(&StoredSecret{ID: "copied-credential", Ciphertext: stored.Ciphertext, UpdatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	if got, err := restarted.resolveSecret(checks.SecretRef{File: "stored:copied-credential"}); err == nil || got != "" {
		t.Fatal("credential ciphertext could be moved to another ID")
	}
}

func monitoringFingerprint(t *testing.T, db *gorm.DB) []byte {
	t.Helper()
	state := map[string][]map[string]any{}
	for _, table := range []string{"monitor_events", "task_snapshots", "diagnosis_jobs", "delivery_outbox", "event_audit", "monitor_config_versions", "monitor_secrets"} {
		rows := []map[string]any{}
		if err := db.Table(table).Order("1").Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		state[table] = rows
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestGeneralIntegrationRetentionPreviewNeverDeletes(t *testing.T) {
	f, _, doc := generalFixture(t)
	startGeneral(t, f, doc)
	now := time.Now().UTC().Truncate(time.Microsecond)
	old := now.AddDate(0, 0, -90)
	ids := []string{"eligible", "active-manual", "pending-delivery", "pending-diagnosis", "recent", "idle"}
	for _, id := range ids {
		at := old
		if id == "recent" {
			at = now
		}
		key := identity(f.cfg.Source, f.cfg.Environment, id)
		snapshot := Snapshot{Key: key, Source: f.cfg.Source, Environment: f.cfg.Environment, TaskID: id, Status: "observed", ObservedAt: at, FirstSeenAt: at, NextCheckAt: at, Evidence: M{"preserved": id}, RuleStates: M{}}
		if err := f.db.Create(&snapshot).Error; err != nil {
			t.Fatal(err)
		}
		if id == "idle" {
			continue
		}
		event := Event{EventID: uuid.NewString(), LogicalKey: identity(key, "retention-test"), TaskKey: key, TaskID: id, Episode: 1, RuleCode: "retention-test", RuleVersion: "v1", Status: "resolved", Severity: "warning", DiagnosisStatus: "completed", Evidence: M{"preserved": id}, OpenedAt: old, LastSeenAt: at, ClosedAt: &at, Revision: 1}
		if id == "active-manual" {
			event.Status, event.ConditionActive = "closed_unresolved", true
		}
		if err := f.db.Create(&event).Error; err != nil {
			t.Fatal(err)
		}
		if id == "eligible" || id == "pending-delivery" {
			status := "delivered"
			var confirmed *time.Time = &old
			if id == "pending-delivery" {
				status, confirmed = "needs_attention", nil
			}
			outbox := Outbox{ID: uuid.NewString(), EventID: event.EventID, Revision: 1, Destination: "test-route", Kind: "closed", Payload: M{"preserved": id}, Status: status, NextRetryAt: now, ConfirmedAt: confirmed}
			if err := f.db.Create(&outbox).Error; err != nil {
				t.Fatal(err)
			}
		}
		if id == "pending-diagnosis" {
			job := Job{EventID: event.EventID, Status: "queued", QueuedAt: now, DeadlineAt: now.Add(time.Minute), Evidence: Records{}}
			if err := f.db.Create(&job).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	before := monitoringFingerprint(t, f.db)
	policy := checks.RetentionSpec{ClosedEvidenceDays: 30, SnapshotDays: 30, DeliveredDays: 30}
	for i := 0; i < 3; i++ {
		result, err := PreviewRetention(f.ctx, f.db, policy, now)
		if err != nil || result["preview_only"] != true || result["automatic_cleanup"] != false || result["closed_event_evidence"] != int64(1) || result["inactive_snapshots"] != int64(4) || result["confirmed_deliveries"] != int64(1) {
			t.Fatal("retention preview counted protected data incorrectly", result, err)
		}
	}
	response := generalHTTP(t, &Server{Cfg: f.cfg, DB: f.db}, "POST", "/internal/retention/preview", policy, true)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	if _, err := PreviewRetention(f.ctx, f.db, checks.RetentionSpec{ClosedEvidenceDays: -1}, now); err == nil {
		t.Fatal("negative retention days accepted")
	}
	if after := monitoringFingerprint(t, f.db); !bytes.Equal(before, after) {
		t.Fatal("retention preview changed persisted monitoring/configuration data")
	}
}

func TestPreviousFieldsRestoreNumericAndTimestampContracts(t *testing.T) {
	spec := checks.CheckSpec{ID: "previous-types", Mode: "entity", Identity: []string{"id"}, Fields: map[string]checks.FieldSpec{"id": {Column: "id", Type: "string", Required: true}, "retries": {Column: "retries", Type: "int", Required: true}, "updated": {Column: "updated", Type: "timestamp", Required: true}, "payload": {Column: "payload", Type: "json", Required: true}}, Rules: []checks.RuleSpec{{ID: "changed", Trigger: "fields.retries > previous.retries && fields.updated > previous.updated && previous.payload.enabled == true", Recover: "fields.retries <= previous.retries"}}}
	var saved M
	if err := saved.Scan(`{"id":"one","retries":9007199254740993,"updated":"2026-01-01T00:00:00Z","payload":{"enabled":true}}`); err != nil {
		t.Fatal(err)
	}
	previous := previousFields(spec, saved)
	if previous["retries"] != int64(9007199254740993) {
		t.Fatal("JSON roundtrip changed large integer", previous["retries"])
	}
	stamp, ok := previous["updated"].(time.Time)
	if !ok {
		t.Fatal("saved timestamp type not restored", previous)
	}
	rules, err := checks.CompileRules(spec)
	if err != nil {
		t.Fatal(err)
	}
	observation := checks.Observation{CheckID: spec.ID, EntityKey: "one", Quality: checks.QualityValid, Fields: map[string]any{"retries": int64(9007199254740994), "updated": stamp.Add(time.Second)}}
	if result := rules[0].Evaluate(observation, map[string]any(previous), time.Now()); result.Decision != checks.Trigger {
		t.Fatal("CEL previous contract not equivalent to current fields", result)
	}
	if got := previousFields(spec, M{"id": "one", "retries": json.Number("3"), "updated": "invalid-timestamp", "payload": map[string]any{}}); len(got) != 0 {
		t.Fatal("invalid previous timestamp was treated as typed evidence", got)
	}
}
