package checks

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
)

type SecretResolver func(SecretRef) (string, error)

func ResolveSecret(ref SecretRef) (string, error) {
	if err := validateSecret(ref, false); err != nil {
		return "", err
	}
	if ref.Env != "" {
		v := os.Getenv(ref.Env)
		if v == "" {
			return "", fmt.Errorf("secret environment %s is missing", ref.Env)
		}
		return v, nil
	}
	f, err := os.Open(ref.File)
	if err != nil {
		return "", fmt.Errorf("secret file unavailable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return "", fmt.Errorf("secret file invalid or exceeds 64 KiB")
	}
	v := strings.TrimSpace(string(raw))
	if v == "" {
		return "", fmt.Errorf("secret file is empty")
	}
	return v, nil
}

// ValidateSQL is a deliberately conservative guard, not an SQL authorization
// parser. Only SELECT is supported; read-only database accounts and transactions
// remain mandatory. Executable comments, locking/output clauses, multi-statement
// SQL and known blocking/mutating functions are rejected.
func ValidateSQL(query string) error {
	if len(query) == 0 || len(query) > 64<<10 {
		return fmt.Errorf("query must contain 1..65536 bytes")
	}
	tokens, err := sqlTokens(query)
	if err != nil {
		return err
	}
	if len(tokens) == 0 || tokens[0] != "SELECT" {
		return fmt.Errorf("only SELECT queries are supported")
	}
	denied := map[string]bool{"INSERT": true, "UPDATE": true, "DELETE": true, "MERGE": true, "REPLACE": true, "DROP": true, "CREATE": true, "ALTER": true, "TRUNCATE": true, "GRANT": true, "REVOKE": true, "COPY": true, "CALL": true, "EXEC": true, "EXECUTE": true, "DO": true, "SET": true, "USE": true, "INTO": true, "OUTFILE": true, "DUMPFILE": true, "LOCK": true, "UNLOCK": true, "SLEEP": true, "BENCHMARK": true, "PG_SLEEP": true, "PG_ADVISORY_LOCK": true, "PG_TRY_ADVISORY_LOCK": true, "PG_ADVISORY_XACT_LOCK": true, "PG_TERMINATE_BACKEND": true, "PG_CANCEL_BACKEND": true, "PG_READ_FILE": true, "PG_READ_BINARY_FILE": true, "PG_LS_DIR": true, "LO_IMPORT": true, "LO_EXPORT": true, "LOAD_FILE": true, "DBLINK": true, "DBLINK_EXEC": true, "NEXTVAL": true, "SETVAL": true, "GET_LOCK": true, "RELEASE_LOCK": true}
	for _, t := range tokens {
		if denied[t] {
			return fmt.Errorf("query contains unsupported operation %s", t)
		}
	}
	for i, t := range tokens {
		if t == "FOR" && i+1 < len(tokens) && (tokens[i+1] == "SHARE" || tokens[i+1] == "KEY" || tokens[i+1] == "NO") {
			return fmt.Errorf("locking queries are unsupported")
		}
	}
	return nil
}

var dollarQuote = regexp.MustCompile(`^\$[A-Za-z_0-9]*\$`)

func sqlTokens(query string) ([]string, error) {
	var tokens []string
	for i := 0; i < len(query); {
		b := query[i]
		if unicode.IsSpace(rune(b)) {
			i++
			continue
		}
		if b == ';' {
			return nil, fmt.Errorf("SQL statement separators are unsupported")
		}
		if i+1 < len(query) && query[i:i+2] == "--" || b == '#' {
			for i < len(query) && query[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(query) && query[i:i+2] == "/*" {
			if i+2 < len(query) && (query[i+2] == '!' || query[i+2] == '+') {
				return nil, fmt.Errorf("executable SQL comments are unsupported")
			}
			n := strings.Index(query[i+2:], "*/")
			if n < 0 {
				return nil, fmt.Errorf("unterminated SQL comment")
			}
			i += n + 4
			continue
		}
		if b == '\'' || b == '"' || b == '`' {
			quote := b
			start := i + 1
			i++
			closed := false
			for i < len(query) {
				if query[i] == '\\' {
					return nil, fmt.Errorf("backslash-escaped SQL literals are unsupported; bind parameters instead")
				}
				if query[i] == quote {
					if i+1 < len(query) && query[i+1] == quote {
						i += 2
						continue
					}
					if quote != '\'' {
						tokens = append(tokens, strings.ToUpper(query[start:i]))
					}
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated SQL quote")
			}
			continue
		}
		if b == '$' {
			if mark := dollarQuote.FindString(query[i:]); mark != "" {
				n := strings.Index(query[i+len(mark):], mark)
				if n < 0 {
					return nil, fmt.Errorf("unterminated SQL dollar quote")
				}
				i += 2*len(mark) + n
				continue
			}
		}
		if b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b == '_' {
			start := i
			i++
			for i < len(query) {
				v := query[i]
				if !(v >= 'A' && v <= 'Z' || v >= 'a' && v <= 'z' || v >= '0' && v <= '9' || v == '_') {
					break
				}
				i++
			}
			tokens = append(tokens, strings.ToUpper(query[start:i]))
			continue
		}
		i++
	}
	return tokens, nil
}

type Runner struct {
	db      *sqlx.DB
	spec    SourceSpec
	timeout time.Duration
}

func NewRunner(spec SourceSpec, resolver SecretResolver) (*Runner, error) {
	d := Document{Version: 1, Sources: []SourceSpec{spec}}
	if err := Validate(&d); err != nil {
		return nil, err
	}
	spec = d.Sources[0]
	if resolver == nil {
		resolver = ResolveSecret
	}
	dsn, err := resolver(spec.DSN)
	if err != nil {
		return nil, err
	}
	driver := spec.Driver
	if driver == "postgres" {
		driver = "pgx"
	}
	db, err := sqlx.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("source %s: connection configuration invalid", spec.ID)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)
	timeout, _ := time.ParseDuration(spec.Budget.Timeout)
	return &Runner{db: db, spec: spec, timeout: timeout}, nil
}

func (r *Runner) Close() error { return r.db.Close() }
func (r *Runner) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	if err := r.db.PingContext(ctx); err != nil {
		return fmt.Errorf("source_connection_unavailable")
	}
	return nil
}

func (r *Runner) Query(ctx context.Context, c CheckSpec, w Window) ([]Observation, error) {
	if c.Source != r.spec.ID {
		return nil, fmt.Errorf("check source does not match runner")
	}
	rows, err := r.read(ctx, c.Query, map[string]any{"window_start": w.Start.UTC(), "window_end": w.End.UTC(), "after": w.After})
	if err != nil {
		return nil, err
	}
	observed := time.Now().UTC()
	result := make([]Observation, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		o, err := MapRow(c, row, observed)
		if err != nil {
			return nil, err
		}
		if seen[o.EntityKey] {
			return nil, fmt.Errorf("duplicate entity identity in query result")
		}
		seen[o.EntityKey] = true
		result = append(result, o)
	}
	return result, nil
}

func (r *Runner) Details(ctx context.Context, c CheckSpec, o Observation) ([]map[string]any, error) {
	if c.Source != r.spec.ID || o.CheckID != c.ID {
		return nil, fmt.Errorf("detail scope does not match runner")
	}
	if c.DetailQuery == "" {
		return nil, nil
	}
	params := map[string]any{"entity_key": o.EntityKey}
	for k, v := range o.Fields {
		params[k] = v
	}
	rows, err := r.read(ctx, c.DetailQuery, params)
	if err != nil {
		return nil, err
	}
	// Drivers return textual columns as []byte. JSON would otherwise base64
	// encode them, hiding both readable evidence and credentials from redaction.
	for _, row := range rows {
		for key, value := range row {
			if raw, ok := value.([]byte); ok {
				if !utf8.Valid(raw) {
					return nil, fmt.Errorf("detail text decoding failed")
				}
				row[key] = string(raw)
			}
		}
	}
	return rows, nil
}

func (r *Runner) read(ctx context.Context, query string, params map[string]any) ([]map[string]any, error) {
	if err := ValidateSQL(query); err != nil {
		return nil, err
	}
	bound, args, err := sqlx.Named(query, params)
	if err != nil {
		return nil, fmt.Errorf("query parameter binding failed")
	}
	bound = r.db.Rebind(bound)
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	tx, err := r.db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("read_only_transaction_unavailable")
	}
	defer tx.Rollback()
	rows, err := tx.QueryxContext(ctx, bound, args...)
	if err != nil {
		return nil, fmt.Errorf("query_failed")
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("query_columns_unavailable")
	}
	if len(cols) > 100 {
		return nil, fmt.Errorf("query exceeds 100 columns")
	}
	seen := map[string]bool{}
	for _, col := range cols {
		if seen[col] {
			return nil, fmt.Errorf("duplicate query column alias")
		}
		seen[col] = true
	}
	result := []map[string]any{}
	bytes := 0
	for rows.Next() {
		if len(result) >= r.spec.Budget.MaxRows {
			return nil, fmt.Errorf("query row budget exceeded")
		}
		row := map[string]any{}
		if err := rows.MapScan(row); err != nil {
			return nil, fmt.Errorf("row_decoding_failed")
		}
		for k, v := range row {
			bytes += len(k) + 16
			switch x := v.(type) {
			case []byte:
				bytes += len(x)
			case string:
				bytes += len(x)
			default:
				bytes += 32
			}
			if bytes > r.spec.Budget.MaxBytes {
				return nil, fmt.Errorf("query byte budget exceeded")
			}
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query_stream_failed")
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("query_stream_incomplete")
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("read_only_transaction_incomplete")
	}
	return result, nil
}

func MapRow(c CheckSpec, row map[string]any, observed time.Time) (Observation, error) {
	o := Observation{CheckID: c.ID, Fields: map[string]any{}, ObservedAt: observed.UTC(), Quality: QualityValid}
	for name, f := range c.Fields {
		v, ok := row[f.Column]
		if !ok || v == nil {
			o.Fields[name] = nil
			if f.Required {
				o.Quality = QualityInvalid
				o.Errors = append(o.Errors, name+": missing_required_field")
			}
			continue
		}
		value, err := convert(v, f.Type)
		if err != nil {
			o.Quality = QualityInvalid
			o.Errors = append(o.Errors, name+": invalid_"+f.Type)
			o.Fields[name] = nil
		} else {
			o.Fields[name] = value
		}
	}
	if len(c.Identity) == 0 {
		if c.Mode != "aggregate" {
			return o, fmt.Errorf("entity check requires identity")
		}
		o.EntityKey = "aggregate"
		return o, nil
	}
	parts := make([]any, 0, len(c.Identity))
	for _, name := range c.Identity {
		v, ok := o.Fields[name]
		if !ok || v == nil {
			return o, fmt.Errorf("identity field %s is missing or invalid", name)
		}
		if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
			return o, fmt.Errorf("identity field %s is empty", name)
		}
		parts = append(parts, v)
	}
	if len(parts) == 1 {
		switch v := parts[0].(type) {
		case string:
			o.EntityKey = v
		case int64:
			o.EntityKey = strconv.FormatInt(v, 10)
		case bool:
			o.EntityKey = strconv.FormatBool(v)
		case time.Time:
			o.EntityKey = v.UTC().Format(time.RFC3339Nano)
		default:
			return o, fmt.Errorf("identity type is unsupported")
		}
	} else {
		raw, err := json.Marshal(parts)
		if err != nil {
			return o, fmt.Errorf("identity encoding failed")
		}
		o.EntityKey = string(raw)
	}
	if len(o.EntityKey) > 512 {
		return o, fmt.Errorf("entity identity exceeds 512 bytes")
	}
	return o, nil
}

func convert(v any, kind string) (any, error) {
	if b, ok := v.([]byte); ok {
		v = string(b)
	}
	invalid := func() (any, error) { return nil, fmt.Errorf("type mismatch") }
	switch kind {
	case "string":
		if s, ok := v.(string); ok && utf8.ValidString(s) {
			return s, nil
		}
		return invalid()
	case "int":
		switch x := v.(type) {
		case int:
			return int64(x), nil
		case int32:
			return int64(x), nil
		case int64:
			return x, nil
		case uint64:
			if x <= math.MaxInt64 {
				return int64(x), nil
			}
		case string:
			i, e := strconv.ParseInt(x, 10, 64)
			return i, e
		}
	case "double":
		switch x := v.(type) {
		case float64:
			if !math.IsNaN(x) && !math.IsInf(x, 0) {
				return x, nil
			}
		case float32:
			if !math.IsNaN(float64(x)) && !math.IsInf(float64(x), 0) {
				return float64(x), nil
			}
		case int64:
			return float64(x), nil
		case string:
			f, e := strconv.ParseFloat(x, 64)
			if e == nil && (math.IsNaN(f) || math.IsInf(f, 0)) {
				return invalid()
			}
			return f, e
		}
	case "bool":
		switch x := v.(type) {
		case bool:
			return x, nil
		case int64:
			if x == 0 || x == 1 {
				return x == 1, nil
			}
		case string:
			switch strings.ToLower(x) {
			case "true", "1":
				return true, nil
			case "false", "0":
				return false, nil
			}
		}
	case "timestamp":
		switch x := v.(type) {
		case time.Time:
			return x.UTC(), nil
		case string:
			for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
				if t, e := time.Parse(layout, x); e == nil {
					return t.UTC(), nil
				}
			}
		}
	case "json":
		s, ok := v.(string)
		if !ok {
			return invalid()
		}
		var decoded any
		if err := json.Unmarshal([]byte(s), &decoded); err != nil {
			return nil, err
		}
		return decoded, nil
	}
	return invalid()
}
