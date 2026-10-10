package checks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
)

const configFixture = `version: 1
sources:
  - id: orders
    driver: mysql
    dsn: {env: ORDER_DATABASE_DSN}
checks:
  - id: callbacks
    source: orders
    mode: entity
    query: SELECT order_id, callback_state, retry_count FROM callback_orders
    identity: [id]
    fields:
      id: {column: order_id, type: string}
      state: {column: callback_state, type: string, required: true}
      retries: {column: retry_count, type: int, required: true}
    rules:
      - id: callback_failure
        trigger: fields.state == 'DEAD_LETTER'
        recover: fields.state == 'DELIVERED'
        confirmations: 5
`

func TestConfigurationStrictAndDefaults(t *testing.T) {
	d, err := Parse([]byte(configFixture))
	if err != nil {
		t.Fatal(err)
	}
	if d.Checks[0].Environment != "default" || d.Checks[0].DiagnosisMode != "rule_only" || d.Checks[0].AllowModel || !d.Checks[0].Fields["id"].Required || d.Sources[0].Budget.MaxRows != 1000 {
		t.Fatal("defaults missing")
	}
	if _, err := Parse([]byte(`{"version":1,"sources":[],"checks":[],"channels":[]}`)); err != nil {
		t.Fatal("bootstrap must be valid", err)
	}
	for _, bad := range []string{
		configFixture + "unknown: true\n",
		strings.Replace(configFixture, "driver: mysql", "driver: mysql\n    password: secret", 1),
		strings.Replace(configFixture, "dsn: {env: ORDER_DATABASE_DSN}", "dsn: {env: ORDER_DATABASE_DSN, file: /secret}", 1),
		strings.Replace(configFixture, "type: int", "type: integer", 1),
		strings.Replace(configFixture, "source: orders", "source: missing", 1),
		strings.Replace(configFixture, "confirmations: 5", "confirmations: -1", 1),
		strings.Replace(configFixture, "trigger: fields.state == 'DEAD_LETTER'", "trigger: fields.state +", 1),
		configFixture + "---\nversion: 1\n",
		`{"version":1,"version":1,"sources":[],"checks":[]}`,
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Fatalf("accepted invalid config %s", bad)
		}
	}
	d.Sources = append(d.Sources, d.Sources[0])
	if err := Validate(d); err == nil {
		t.Fatal("duplicate source accepted")
	}
}

func TestRulesThreeStatesAndConflictingEvidence(t *testing.T) {
	d, err := Parse([]byte(configFixture))
	if err != nil {
		t.Fatal(err)
	}
	rs, _ := CompileRules(d.Checks[0])
	rule := rs[0]
	now := time.Now().UTC()
	for _, tt := range []struct {
		name    string
		state   any
		quality Quality
		want    Decision
	}{
		{"failure", "DEAD_LETTER", QualityValid, Trigger}, {"recovery", "DELIVERED", QualityValid, Clear}, {"waiting_not_recovery", "WAITING_ACK", QualityValid, Unknown}, {"cancel_not_recovery", "CANCELED", QualityValid, Unknown}, {"bad_type", int64(1), QualityValid, Unknown}, {"invalid_observation", "DELIVERED", QualityInvalid, Unknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o := Observation{Fields: map[string]any{"state": tt.state}, Quality: tt.quality}
			if got := rule.Evaluate(o, nil, now); got.Decision != tt.want {
				t.Fatalf("got %+v", got)
			}
		})
	}
	if got := rule.Evaluate(Observation{Fields: map[string]any{}, Quality: QualityValid}, nil, now); got.Decision != Unknown {
		t.Fatal("missing field must be unknown")
	}
	conflict, _ := CompileRules(CheckSpec{Rules: []RuleSpec{{ID: "conflict", Trigger: "true", Recover: "true"}}})
	if got := conflict[0].Evaluate(Observation{Quality: QualityValid}, nil, now); got.Decision != Unknown || got.Reason != "conflicting_trigger_and_recovery" {
		t.Fatal(got)
	}
	if _, err := CompileRules(CheckSpec{Rules: []RuleSpec{{ID: "non_bool", Trigger: "42", Recover: "false"}}}); err == nil {
		t.Fatal("non-boolean expression accepted")
	}
}

func TestRuleStateSupportsMoreThanThreeAndBreaksOnUnknown(t *testing.T) {
	s := RuleState{}
	for i := 1; i <= 7; i++ {
		s = s.Advance(RuleResult{Decision: Trigger}, 5, 4)
		if s.Bad != min(i, 5) || s.Good != 0 {
			t.Fatal(s)
		}
	}
	s = s.Advance(RuleResult{Decision: Unknown}, 5, 4)
	if s.Bad != 0 || s.Good != 0 {
		t.Fatal("unknown preserved consecutive history")
	}
	for i := 1; i <= 4; i++ {
		s = s.Advance(RuleResult{Decision: Clear}, 5, 4)
	}
	if s.Good != 4 || s.Bad != 0 {
		t.Fatal(s)
	}
}

func TestMapRowQualityAndStableAggregateIdentity(t *testing.T) {
	d, _ := Parse([]byte(configFixture))
	c := d.Checks[0]
	o, err := MapRow(c, map[string]any{"order_id": []byte("ord-1"), "callback_state": "DEAD_LETTER", "retry_count": []byte("5")}, time.Now())
	if err != nil || o.EntityKey != "ord-1" || o.Fields["retries"] != int64(5) || o.Quality != QualityValid {
		t.Fatal(o, err)
	}
	o, err = MapRow(c, map[string]any{"order_id": "ord-1", "callback_state": "DELIVERED", "retry_count": "invalid"}, time.Now())
	if err != nil || o.Quality != QualityInvalid {
		t.Fatal("type mismatch must mark unknown", o, err)
	}
	if _, err := MapRow(c, map[string]any{"callback_state": "DELIVERED"}, time.Now()); err == nil {
		t.Fatal("missing identity accepted")
	}
	c.Mode = "aggregate"
	c.Identity = []string{"state", "id"}
	a, _ := MapRow(c, map[string]any{"order_id": "same", "callback_state": "groupA", "retry_count": int64(1)}, time.Now())
	b, _ := MapRow(c, map[string]any{"retry_count": int64(9), "callback_state": "groupA", "order_id": "same"}, time.Now())
	if a.EntityKey != b.EntityKey {
		t.Fatal("group identity changed with order/count", a, b)
	}
}

func TestRejectUnsafeSQLAndAllowQuotedValues(t *testing.T) {
	for _, s := range []string{"DELETE FROM orders", "WITH x AS (DELETE FROM orders RETURNING *) SELECT * FROM x", "SELECT 1; DELETE FROM orders", "SELECT * INTO OUTFILE '/tmp/x' FROM orders", "SELECT * FROM orders FOR UPDATE", "SELECT * FROM orders FOR SHARE", "SELECT SLEEP(10)", "SELECT pg_sleep(5)", "SELECT `sleep`(5)", "SELECT /* !no */ 1 /*!50000 INTO OUTFILE '/tmp/x' */", "SELECT 'unterminated", "SELECT 'back\\slash'"} {
		if err := ValidateSQL(s); err == nil {
			t.Fatalf("unsafe query accepted %q", s)
		}
	}
	for _, s := range []string{"SELECT id FROM orders WHERE updated_at >= :window_start", "SELECT 'DELETE; DROP TABLE x' AS label", "SELECT count(*) AS failures FROM orders WHERE state = 'FAILED'", "-- ordinary comment\nSELECT CAST(id AS TEXT) FROM orders", "SELECT $$literal; DROP TABLE x$$ AS label"} {
		if err := ValidateSQL(s); err != nil {
			t.Fatalf("safe query %q rejected: %v", s, err)
		}
	}
}

func mockRunner(t *testing.T, budget Budget) (*Runner, sqlmock.Sqlmock, CheckSpec) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	d, _ := Parse([]byte(configFixture))
	c := d.Checks[0]
	c.Query = "SELECT order_id, callback_state, retry_count FROM callback_orders"
	if budget.MaxRows == 0 {
		budget.MaxRows = 10
	}
	if budget.MaxBytes == 0 {
		budget.MaxBytes = 1024
	}
	return &Runner{db: sqlx.NewDb(db, "mysql"), spec: SourceSpec{ID: "orders", Budget: budget}, timeout: 20 * time.Millisecond}, mock, c
}

func TestQueryBudgetsNeverReturnPartialObservation(t *testing.T) {
	for _, tt := range []struct {
		name     string
		budget   Budget
		rows     *sqlmock.Rows
		queryErr error
		delay    time.Duration
	}{
		{"row_limit", Budget{MaxRows: 1}, sqlmock.NewRows([]string{"order_id", "callback_state", "retry_count"}).AddRow("a", "WAITING_ACK", 1).AddRow("b", "WAITING_ACK", 2), nil, 0},
		{"byte_limit", Budget{MaxBytes: 50}, sqlmock.NewRows([]string{"order_id", "callback_state", "retry_count"}).AddRow("a", strings.Repeat("x", 60), 1), nil, 0},
		{"partial_stream", Budget{}, sqlmock.NewRows([]string{"order_id", "callback_state", "retry_count"}).AddRow("a", "WAITING_ACK", 1).AddRow("b", "WAITING_ACK", 2).RowError(1, errors.New("connection lost secret=value")), nil, 0},
		{"timeout", Budget{}, nil, nil, 100 * time.Millisecond},
		{"duplicate_identity", Budget{}, sqlmock.NewRows([]string{"order_id", "callback_state", "retry_count"}).AddRow("a", "WAITING_ACK", 1).AddRow("a", "DELIVERED", 2), nil, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, m, c := mockRunner(t, tt.budget)
			m.ExpectBegin()
			q := m.ExpectQuery("SELECT order_id")
			if tt.delay > 0 {
				q.WillDelayFor(tt.delay)
			}
			if tt.rows != nil {
				q.WillReturnRows(tt.rows)
			} else {
				q.WillReturnError(errors.New("timeout secret=value"))
			}
			if tt.name == "duplicate_identity" {
				m.ExpectCommit()
			} else {
				m.ExpectRollback()
			}
			got, err := r.Query(context.Background(), c, Window{})
			if err == nil || got != nil {
				t.Fatal("partial/invalid batch escaped", got, err)
			}
			if strings.Contains(err.Error(), "secret=value") {
				t.Fatal("driver details leaked")
			}
			if tt.name != "timeout" {
				if err := m.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestQueryEmptyIsNotSyntheticRecoveryAndCommitIsRequired(t *testing.T) {
	r, m, c := mockRunner(t, Budget{})
	m.ExpectBegin()
	m.ExpectQuery("SELECT order_id").WillReturnRows(sqlmock.NewRows([]string{"order_id", "callback_state", "retry_count"}))
	m.ExpectCommit()
	got, err := r.Query(context.Background(), c, Window{})
	if err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	r, m, c = mockRunner(t, Budget{})
	m.ExpectBegin()
	m.ExpectQuery("SELECT order_id").WillReturnRows(sqlmock.NewRows([]string{"order_id", "callback_state", "retry_count"}).AddRow("a", "DELIVERED", 1))
	m.ExpectCommit().WillReturnError(errors.New("commit lost secret=value"))
	got, err = r.Query(context.Background(), c, Window{})
	if err == nil || got != nil {
		t.Fatal("uncommitted batch escaped", got, err)
	}
}

func TestSecretReferences(t *testing.T) {
	t.Setenv("CHECKS_TEST_SECRET", "not-a-real-dsn")
	if got, err := ResolveSecret(SecretRef{Env: "CHECKS_TEST_SECRET"}); err != nil || got != "not-a-real-dsn" {
		t.Fatal(got, err)
	}
	for _, s := range []SecretRef{{}, {Env: "CHECKS_TEST_SECRET", File: "ignored"}, {Env: "missing-variable"}, {Env: "CHECKS_TEST_MISSING"}} {
		if _, err := ResolveSecret(s); err == nil {
			t.Fatal("invalid/missing reference accepted")
		}
	}
}

func TestPostgresNamedBindingAndScopedDetail(t *testing.T) {
	r, m, c := mockRunner(t, Budget{})
	r.db = sqlx.NewDb(r.db.DB, "pgx")
	c.Query = "SELECT order_id, callback_state, retry_count FROM callback_orders WHERE updated_at >= :window_start"
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m.ExpectBegin()
	m.ExpectQuery(`updated_at >= \$1`).WithArgs(start).WillReturnRows(sqlmock.NewRows([]string{"order_id", "callback_state", "retry_count"}).AddRow("ord-pg", "DELIVERED", 1))
	m.ExpectCommit()
	got, err := r.Query(context.Background(), c, Window{Start: start})
	if err != nil || len(got) != 1 || got[0].EntityKey != "ord-pg" {
		t.Fatal(got, err)
	}
	c.DetailQuery = "SELECT callback_state FROM callback_orders WHERE order_id = :id"
	m.ExpectBegin()
	m.ExpectQuery(`order_id = \$1`).WithArgs("ord-pg").WillReturnRows(sqlmock.NewRows([]string{"callback_state"}).AddRow("DELIVERED"))
	m.ExpectCommit()
	if _, err := r.Details(context.Background(), c, got[0]); err != nil {
		t.Fatal(err)
	}
	wrong := got[0]
	wrong.CheckID = "another-check"
	if _, err := r.Details(context.Background(), c, wrong); err == nil {
		t.Fatal("cross-check detail accepted")
	}
	if err := m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExampleConfigurations(t *testing.T) {
	for _, name := range []string{"monitor.mysql.yaml", "monitor.postgres.yaml", "monitor.aggregate.yaml"} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load("../../examples/order-callback/" + name); err != nil {
				t.Fatal(err)
			}
		})
	}
}
