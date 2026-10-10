package checks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
)

type postgresFixture struct {
	admin     *sqlx.DB
	readerDSN string
	schema    string
	table     string
}

func newPostgresFixture(t *testing.T) postgresFixture {
	t.Helper()
	if os.Getenv("RUN_CHECKS_PG_INTEGRATION") != "1" {
		t.Skip("set RUN_CHECKS_PG_INTEGRATION=1 to run isolated PostgreSQL checks")
	}
	dsn := os.Getenv("CHECKS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("CHECKS_TEST_POSTGRES_DSN is required")
	}
	admin, err := sqlx.Open("pgx", dsn)
	if err != nil {
		t.Fatal("fixture administrator connection configuration failed")
	}
	t.Cleanup(func() { admin.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		t.Fatal("fixture administrator connection unavailable")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal("fixture random identity unavailable")
	}
	suffix := hex.EncodeToString(random[:])
	schema, role := "checks_pg_"+suffix, "checks_reader_"+suffix
	var passwordBytes [32]byte
	if _, err := rand.Read(passwordBytes[:]); err != nil {
		t.Fatal("fixture random password unavailable")
	}
	password := hex.EncodeToString(passwordBytes[:])
	quote := func(s string) string { return pgx.Identifier{s}.Sanitize() }
	schemaCreated, roleCreated := false, false
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if schemaCreated {
			if _, err := admin.ExecContext(cleanup, "DROP SCHEMA "+quote(schema)+" CASCADE"); err != nil {
				t.Error("isolated fixture schema cleanup failed")
			}
		}
		if roleCreated {
			// Revoke this fresh role's grants in the current database before DROP.
			if _, err := admin.ExecContext(cleanup, "DROP OWNED BY "+quote(role)); err != nil {
				t.Error("isolated fixture grant cleanup failed")
			}
			if _, err := admin.ExecContext(cleanup, "DROP ROLE "+quote(role)); err != nil {
				t.Error("isolated fixture role cleanup failed")
			}
		}
	})
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quote(schema)); err != nil {
		t.Fatal("isolated fixture schema creation failed")
	}
	schemaCreated = true
	if _, err := admin.ExecContext(ctx, fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s'", quote(role), password)); err != nil {
		t.Fatal("isolated SELECT-only role creation failed; fixture administrator needs CREATEROLE")
	}
	roleCreated = true
	table := pgx.Identifier{schema, "callback_orders"}.Sanitize()
	if _, err := admin.ExecContext(ctx, "CREATE TABLE "+table+" (order_id text PRIMARY KEY, callback_state text NOT NULL, retry_count integer NOT NULL, age_seconds bigint NOT NULL, updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP)"); err != nil {
		t.Fatal("isolated fixture table creation failed")
	}
	var database string
	if err := admin.QueryRowContext(ctx, "SELECT current_database()").Scan(&database); err != nil {
		t.Fatal("fixture database identity unavailable")
	}
	for _, q := range []string{"GRANT CONNECT ON DATABASE " + quote(database) + " TO " + quote(role), "GRANT USAGE ON SCHEMA " + quote(schema) + " TO " + quote(role), "GRANT SELECT ON " + table + " TO " + quote(role)} {
		if _, err := admin.ExecContext(ctx, q); err != nil {
			t.Fatal("fixture read-only grants failed")
		}
	}
	if _, err := admin.ExecContext(ctx, "INSERT INTO "+table+" (order_id,callback_state,retry_count,age_seconds) VALUES ('alpha','AWAITING_ACK',2,300),('bravo','DELIVERED',0,5),('charlie','DLQ',5,600)"); err != nil {
		t.Fatal("synthetic fixture insertion failed")
	}
	readerDSN, err := replacePostgresCredentials(dsn, role, password)
	if err != nil {
		t.Fatal("fixture reader DSN preparation failed")
	}
	return postgresFixture{admin: admin, readerDSN: readerDSN, schema: schema, table: table}
}

// Preserve administrator-provided transport settings without exposing any DSN.
func replacePostgresCredentials(dsn, user, password string) (string, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", err
		}
		u.User = url.UserPassword(user, password)
		q := u.Query()
		q.Del("user")
		q.Del("password")
		u.RawQuery = q.Encode()
		return u.String(), nil
	}
	// Generated user/password contain only safe ASCII letters, digits and '_' and
	// later libpq keyword settings override earlier administrator credentials.
	return dsn + " user='" + user + "' password='" + password + "'", nil
}

func (f postgresFixture) runner(t *testing.T, b Budget) *Runner {
	t.Helper()
	if b.Timeout == "" {
		b.Timeout = "3s"
	}
	if b.MaxRows == 0 {
		b.MaxRows = 20
	}
	if b.MaxBytes == 0 {
		b.MaxBytes = 4096
	}
	r, err := NewRunner(SourceSpec{ID: "orders-pg", Driver: "postgres", DSN: SecretRef{Env: "CHECKS_READER_PLACEHOLDER"}, Budget: b}, func(SecretRef) (string, error) { return f.readerDSN, nil })
	if err != nil {
		t.Fatal("fixture reader runner configuration failed")
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func (f postgresFixture) entityCheck() CheckSpec {
	return CheckSpec{ID: "pg-callback", Source: "orders-pg", Mode: "entity", Identity: []string{"id"}, Query: "SELECT order_id, callback_state, retry_count, age_seconds, updated_at FROM " + f.table + " WHERE updated_at >= :window_start ORDER BY order_id", Fields: map[string]FieldSpec{
		"id": {Column: "order_id", Type: "string", Required: true}, "state": {Column: "callback_state", Type: "string", Required: true}, "retries": {Column: "retry_count", Type: "int", Required: true}, "age": {Column: "age_seconds", Type: "int", Required: true}, "updated": {Column: "updated_at", Type: "timestamp", Required: true},
	}, Rules: []RuleSpec{{ID: "callback-failed", Version: "v1", Trigger: "fields.state == 'DLQ'", Recover: "fields.state == 'DELIVERED'", Confirmations: 1, RecoveryConfirmations: 2}}}
}

func TestIntegrationPostgresReadOnlyChecks(t *testing.T) {
	f := newPostgresFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	window := Window{Start: time.Now().UTC().Add(-time.Hour), End: time.Now().UTC()}
	t.Run("entity_and_business_states", func(t *testing.T) {
		r := f.runner(t, Budget{})
		c := f.entityCheck()
		rules, err := CompileRules(c)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := r.Query(ctx, c, window)
		if err != nil || len(rows) != 3 {
			t.Fatal("real entity query did not return three complete synthetic observations")
		}
		want := map[string]Decision{"alpha": Unknown, "bravo": Clear, "charlie": Trigger}
		for _, o := range rows {
			if o.Quality != QualityValid {
				t.Fatal("real PostgreSQL field conversion failed")
			}
			if _, ok := o.Fields["retries"].(int64); !ok {
				t.Fatal("integer field is not strongly typed")
			}
			if _, ok := o.Fields["updated"].(time.Time); !ok {
				t.Fatal("timestamp field is not strongly typed")
			}
			if got := rules[0].Evaluate(o, nil, time.Now()).Decision; got != want[o.EntityKey] {
				t.Fatal("business state rule result mismatch")
			}
		}
	})
	t.Run("aggregate_zero_is_explicit_recovery", func(t *testing.T) {
		r := f.runner(t, Budget{})
		c := CheckSpec{ID: "pg-total", Source: "orders-pg", Mode: "aggregate", Identity: []string{"metric"}, Query: "SELECT 'callback-failures' AS metric, COUNT(*) AS failures FROM " + f.table + " WHERE callback_state = 'DLQ'", Fields: map[string]FieldSpec{"metric": {Column: "metric", Type: "string", Required: true}, "count": {Column: "failures", Type: "int", Required: true}}, Rules: []RuleSpec{{ID: "total", Trigger: "fields.count > 0", Recover: "fields.count == 0"}}}
		rules, _ := CompileRules(c)
		rows, err := r.Query(ctx, c, window)
		if err != nil || len(rows) != 1 || rules[0].Evaluate(rows[0], nil, time.Now()).Decision != Trigger {
			t.Fatal("real aggregate failure query failed")
		}
		key := rows[0].EntityKey
		if _, err := f.admin.ExecContext(ctx, "UPDATE "+f.table+" SET callback_state='DELIVERED' WHERE order_id='charlie'"); err != nil {
			t.Fatal("synthetic recovery setup failed")
		}
		defer f.admin.ExecContext(context.Background(), "UPDATE "+f.table+" SET callback_state='DLQ' WHERE order_id='charlie'")
		rows, err = r.Query(ctx, c, window)
		if err != nil || len(rows) != 1 || rows[0].EntityKey != key || rules[0].Evaluate(rows[0], nil, time.Now()).Decision != Clear {
			t.Fatal("zero aggregate did not return stable explicit recovery observation")
		}
	})
	t.Run("type_error_is_unknown", func(t *testing.T) {
		r := f.runner(t, Budget{})
		c := f.entityCheck()
		c.Fields["retries"] = FieldSpec{Column: "callback_state", Type: "int", Required: true}
		rows, err := r.Query(ctx, c, window)
		if err != nil || len(rows) != 3 {
			t.Fatal("field quality must not fabricate a disappeared object")
		}
		rules, _ := CompileRules(c)
		for _, o := range rows {
			if o.Quality != QualityInvalid || rules[0].Evaluate(o, nil, time.Now()).Decision != Unknown {
				t.Fatal("bad type must produce unknown")
			}
		}
	})
	t.Run("row_budget_discards_entire_batch", func(t *testing.T) {
		r := f.runner(t, Budget{MaxRows: 1})
		rows, err := r.Query(ctx, f.entityCheck(), window)
		if err == nil || rows != nil {
			t.Fatal("real row budget returned partial observations")
		}
	})
	t.Run("byte_budget_discards_entire_batch", func(t *testing.T) {
		r := f.runner(t, Budget{MaxBytes: 32})
		rows, err := r.Query(ctx, f.entityCheck(), window)
		if err == nil || rows != nil {
			t.Fatal("real byte budget returned partial observations")
		}
	})
	t.Run("empty_rows_do_not_recover", func(t *testing.T) {
		r := f.runner(t, Budget{})
		c := f.entityCheck()
		c.Query += " LIMIT 0"
		rows, err := r.Query(ctx, c, window)
		if err != nil || len(rows) != 0 {
			t.Fatal("empty query should produce no synthetic clear observation")
		}
	})
	t.Run("read_only_transaction_and_role", func(t *testing.T) {
		r := f.runner(t, Budget{})
		tx, err := r.db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal("real read-only transaction unavailable")
		}
		var setting string
		if err := tx.QueryRowContext(ctx, "SHOW transaction_read_only").Scan(&setting); err != nil || setting != "on" {
			tx.Rollback()
			t.Fatal("source transaction was not read-only")
		}
		if _, err := tx.ExecContext(ctx, "UPDATE "+f.table+" SET retry_count=999 WHERE order_id='alpha'"); err == nil {
			tx.Rollback()
			t.Fatal("read-only transaction allowed writing")
		}
		tx.Rollback()
		_, err = r.db.ExecContext(ctx, "DELETE FROM "+f.table+" WHERE order_id='alpha'")
		if pgErr, ok := err.(*pgconn.PgError); !ok || pgErr.Code != "42501" {
			t.Fatal("SELECT-only role did not independently reject writing")
		}
		var count int
		if err := f.admin.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+f.table).Scan(&count); err != nil || count != 3 {
			t.Fatal("source business data changed")
		}
	})
	t.Run("query_timeout_discards_entire_batch", func(t *testing.T) {
		r := f.runner(t, Budget{})
		if err := r.Ping(ctx); err != nil {
			t.Fatal("timeout fixture connection unavailable")
		}
		r.timeout = 20 * time.Millisecond
		c := CheckSpec{ID: "pg-expensive", Source: "orders-pg", Mode: "aggregate", Query: "SELECT SUM(i) AS total FROM generate_series(1, 1000000000) AS i", Fields: map[string]FieldSpec{"total": {Column: "total", Type: "int", Required: true}}}
		begin := time.Now()
		rows, err := r.Query(ctx, c, window)
		if err == nil || rows != nil || time.Since(begin) > 5*time.Second {
			t.Fatal("query timeout failed to bound client work or discarded batch")
		}
	})
}
