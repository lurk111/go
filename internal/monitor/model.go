package monitor

import (
	"bytes"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

type M map[string]any

func (m M) Value() (driver.Value, error) {
	if m == nil {
		return []byte(`{}`), nil
	}
	return json.Marshal(m)
}
func (m *M) Scan(v any) error {
	if v == nil {
		*m = M{}
		return nil
	}
	switch x := v.(type) {
	case []byte:
		d := json.NewDecoder(bytes.NewReader(x))
		d.UseNumber()
		return d.Decode(m)
	case string:
		d := json.NewDecoder(bytes.NewBufferString(x))
		d.UseNumber()
		return d.Decode(m)
	}
	return fmt.Errorf("invalid json")
}

type Records []M

func (r Records) Value() (driver.Value, error) {
	if r == nil {
		return []byte(`[]`), nil
	}
	return json.Marshal(r)
}
func (r *Records) Scan(v any) error {
	if v == nil {
		*r = Records{}
		return nil
	}
	switch x := v.(type) {
	case []byte:
		d := json.NewDecoder(bytes.NewReader(x))
		d.UseNumber()
		return d.Decode(r)
	case string:
		d := json.NewDecoder(bytes.NewBufferString(x))
		d.UseNumber()
		return d.Decode(r)
	}
	return fmt.Errorf("invalid records")
}
func str(m M, k string) string { x, _ := m[k].(string); return x }
func num(m M, k string) float64 {
	switch x := m[k].(type) {
	case json.Number:
		v, _ := x.Float64()
		return v
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return 0
}
func obj(m M, k string) M {
	switch x := m[k].(type) {
	case M:
		return x
	case map[string]any:
		return M(x)
	}
	return M{}
}
func stamp(t time.Time) string       { return t.UTC().Format(time.RFC3339Nano) }
func parsed(m M, k string) time.Time { v, _ := time.Parse(time.RFC3339Nano, str(m, k)); return v }
func identity(parts ...string) string { // Preserve the original Python json.dumps identity and historic event keys.
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(parts)
	raw := bytes.TrimSuffix(b.Bytes(), []byte("\n"))
	raw = bytes.ReplaceAll(raw, []byte(`","`), []byte(`", "`))
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

type Cursor struct {
	Key                                       string `gorm:"primaryKey" json:"key"`
	HighWatermark                             *time.Time
	ReconcileAfter                            *string
	ReconcileAt, LastSuccessAt, LastFailureAt *time.Time
	Failures                                  int
}

func (Cursor) TableName() string { return "monitor_cursors" }

type Snapshot struct {
	Key                                        string `gorm:"primaryKey"`
	Source, Environment, TaskID, Status, Stage string
	SourceUpdatedAt                            *time.Time
	ObservedAt, NextCheckAt, FirstSeenAt       time.Time
	Evidence                                   M `gorm:"type:json"`
	RuleStates                                 M `gorm:"type:json"`
}

func (Snapshot) TableName() string { return "task_snapshots" }

type Event struct {
	CheckID         string     `json:"check_id"`
	EntityKey       string     `json:"entity_key"`
	Title           string     `json:"title"`
	Definition      M          `gorm:"type:json" json:"definition"`
	EventID         string     `gorm:"primaryKey" json:"event_id"`
	LogicalKey      string     `json:"logical_key"`
	TaskKey         string     `json:"task_key"`
	TaskID          string     `json:"task_id"`
	Episode         int        `json:"episode"`
	RuleCode        string     `json:"rule_code"`
	RuleVersion     string     `json:"rule_version"`
	Status          string     `json:"status"`
	ConditionActive bool       `json:"condition_active"`
	Severity        string     `json:"severity"`
	DiagnosisStatus string     `json:"diagnosis_status"`
	Evidence        M          `gorm:"type:json" json:"evidence"`
	OpenedAt        time.Time  `json:"opened_at"`
	LastSeenAt      time.Time  `json:"last_seen_at"`
	ClosedAt        *time.Time `json:"closed_at"`
	CloseReason     *string    `json:"close_reason"`
	Revision        int        `json:"revision"`
}

func (Event) TableName() string { return "monitor_events" }

type Outbox struct {
	ID          string     `gorm:"primaryKey" json:"id"`
	EventID     string     `json:"event_id"`
	Revision    int        `json:"revision"`
	Destination string     `json:"destination"`
	Kind        string     `json:"kind"`
	Payload     M          `gorm:"type:json" json:"payload"`
	Status      string     `json:"status"`
	NextRetryAt time.Time  `json:"next_retry_at"`
	Attempts    int        `json:"attempts"`
	Error       *string    `json:"error"`
	ConfirmedAt *time.Time `json:"confirmed_at"`
}

func (Outbox) TableName() string { return "delivery_outbox" }

type Audit struct {
	ID      string    `gorm:"primaryKey" json:"id"`
	EventID string    `json:"event_id"`
	Action  string    `json:"action"`
	Actor   string    `json:"actor"`
	At      time.Time `json:"at"`
	Details M         `gorm:"type:json" json:"details"`
}

func (Audit) TableName() string { return "event_audit" }

type Job struct {
	EventID               string `gorm:"primaryKey" json:"event_id"`
	Status                string `json:"diagnosis_status"`
	QueuedAt, DeadlineAt  time.Time
	StartedAt, FinishedAt *time.Time
	ClaimToken            *string
	ToolCalls             int     `json:"tool_calls"`
	Evidence              Records `gorm:"type:json" json:"evidence"`
	Report                M       `gorm:"type:json" json:"report"`
	ReportHash            *string `json:"report_hash"`
	Error                 *string `json:"error"`
}

func (Job) TableName() string { return "diagnosis_jobs" }
