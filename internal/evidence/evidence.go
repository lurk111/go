package evidence

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"io"
	"os"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

type M = map[string]any

const MaxBytes = 65536

var taskPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
var operations = map[string]bool{"task_logs": true, "worker_status": true, "model_error": true, "extraction_summary": true}

type Request struct {
	Event     string `json:"event_id"`
	Task      string `json:"task_id"`
	Operation string `json:"operation"`
	Service   string `json:"service_id"`
	From      int64  `json:"from_ts"`
	To        int64  `json:"to_ts"`
	Limit     int    `json:"limit"`
	Expires   int64  `json:"expires"`
}
type Envelope struct {
	Body      json.RawMessage `json:"body"`
	Signature string          `json:"signature"`
}

func Sign(r Request, key []byte) ([]byte, error) {
	b, e := json.Marshal(r)
	if e != nil {
		return nil, e
	}
	h := hmac.New(sha256.New, key)
	h.Write(b)
	return json.Marshal(Envelope{b, hex.EncodeToString(h.Sum(nil))})
}
func Validate(raw, key []byte, now time.Time) (Request, error) {
	var r Request
	var env Envelope
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if len(raw) > 4096 || dec.Decode(&env) != nil {
		return r, errors.New("invalid_envelope")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return r, errors.New("trailing_data")
	}
	sig, e := hex.DecodeString(env.Signature)
	h := hmac.New(sha256.New, key)
	h.Write(env.Body)
	if e != nil || !hmac.Equal(sig, h.Sum(nil)) {
		return r, errors.New("invalid_signature")
	}
	dec = json.NewDecoder(bytes.NewReader(env.Body))
	dec.DisallowUnknownFields()
	if dec.Decode(&r) != nil {
		return r, errors.New("invalid_body")
	}
	if _, e = uuid.Parse(r.Event); e != nil || !taskPattern.MatchString(r.Task) || !operations[r.Operation] || r.Service != "lab-worker" || r.Expires < now.Unix() || r.Expires > now.Unix()+35 || r.To-r.From <= 0 || r.To-r.From > 1800 || r.To > now.Unix()+5 || r.Limit < 1 || r.Limit > 200 {
		return r, errors.New("request_denied")
	}
	return r, nil
}

var patterns = []*regexp.Regexp{
	regexp.MustCompile(`(?s)-----BEGIN [^-\n]*PRIVATE KEY-----.*?(-----END [^-\n]*PRIVATE KEY-----|$)`),
	regexp.MustCompile(`(?i)\b(bearer|basic)\s+[^\s,;"']+`),
	regexp.MustCompile(`(?i)(authorization|token|password|secret|api[_-]?key|客户名称|联系人|customer|contact)\s*["']?\s*[:=]\s*("[^"]*"|'[^']*'|[^\s,;]+)`),
	regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"']+`),
	regexp.MustCompile(`[\w.+-]+@[\w.-]+\.[A-Za-z]{2,}`),
	regexp.MustCompile(`\d{11,19}[Xx]?`),
}
var sensitive = regexp.MustCompile(`(?i)token|secret|password|authorization|api.?key|customer|contact|客户|联系人`)

func Redact(s string) string {
	r := []rune(s)
	if len(r) > 4096 {
		s = string(r[:4096])
	}
	for _, p := range patterns {
		s = p.ReplaceAllString(s, "[REDACTED]")
	}
	return s
}
func Sanitize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := M{}
		for k, v := range x {
			if sensitive.MatchString(k) {
				m[k] = "[REDACTED]"
			} else {
				m[k] = Sanitize(v)
			}
		}
		return m
	case []any:
		a := []any{}
		for _, v := range x {
			a = append(a, Sanitize(v))
		}
		return a
	case string:
		return Redact(x)
	default:
		return v
	}
}
func Result(r Request, content any, unavailable string, truncated bool) M {
	var reason any
	if unavailable != "" {
		reason = unavailable
	}
	return M{"evidence_id": "ev-" + strings.ReplaceAll(uuid.NewString(), "-", ""), "event_id": r.Event, "task_id": r.Task, "operation": r.Operation, "collected_at": time.Now().UTC().Format(time.RFC3339Nano), "source": "ssh-lab", "content": Sanitize(content), "truncated": truncated, "unavailable_reason": reason}
}
func ReadFixed(root, relative string, limit int) ([]byte, bool, error) {
	dir, e := os.OpenRoot(root)
	if e != nil {
		return nil, false, e
	}
	defer dir.Close()
	info, e := dir.Lstat(relative)
	if e != nil {
		return nil, false, e
	}
	if !info.Mode().IsRegular() {
		return nil, false, errors.New("file_type_denied")
	}
	f, e := dir.OpenFile(relative, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, false, e
	}
	defer f.Close()
	info, e = f.Stat()
	if e != nil || !info.Mode().IsRegular() {
		return nil, false, errors.New("file_type_denied")
	}
	b, e := io.ReadAll(io.LimitReader(f, int64(limit+1)))
	truncated := len(b) > limit
	if truncated {
		b = b[:limit]
	}
	return b, truncated, e
}
func longText(value any) bool {
	switch x := value.(type) {
	case map[string]any:
		for _, v := range x {
			if longText(v) {
				return true
			}
		}
	case []any:
		for _, v := range x {
			if longText(v) {
				return true
			}
		}
	case string:
		return utf8.RuneCountInString(x) > 4096
	}
	return false
}
func Collect(r Request, root string, now time.Time) M {
	if !taskPattern.MatchString(r.Task) || !operations[r.Operation] {
		return Result(r, nil, "request_denied", false)
	}
	path := "tasks/" + r.Task + ".jsonl"
	limit := MaxBytes
	if r.Operation == "worker_status" {
		path = "worker.json"
		limit = 4096
	}
	read := ReadTail
	if r.Operation == "worker_status" {
		read = ReadFixed
	}
	raw, trunc, e := read(root, path, limit)
	if e != nil {
		return Result(r, nil, "evidence_unavailable", false)
	}
	if r.Operation == "worker_status" {
		var beat struct {
			At float64 `json:"at"`
		}
		if json.Unmarshal(raw, &beat) != nil || trunc {
			return Result(r, nil, "invalid_heartbeat", trunc)
		}
		age := float64(now.UnixNano())/1e9 - beat.At
		if age < 0 || age > 86400 {
			return Result(r, nil, "invalid_heartbeat", false)
		}
		return Result(r, M{"heartbeat_age_seconds": age, "heartbeat_fresh": age <= 10, "process_state": "not_observed", "service_id": "lab-worker"}, "", false)
	}
	rows := []any{}
	malformed := 0
	fallback := ""
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var row M
		if json.Unmarshal(line, &row) != nil {
			malformed++
			if len(fallback) < 2000 {
				fallback += Redact(string(line)) + "\n"
			}
			continue
		}
		if row["task_id"] != r.Task {
			continue
		}
		at, ok := row["at"].(float64)
		if !ok {
			malformed++
			continue
		}
		if at < float64(r.From) || at > float64(r.To) {
			continue
		}
		if (r.Operation == "model_error" && row["kind"] != "model_error") || (r.Operation == "extraction_summary" && row["kind"] != "extraction") {
			continue
		}
		rows = append(rows, row)
	}
	if len(rows) > r.Limit {
		rows = rows[len(rows)-r.Limit:]
		trunc = true
	}
	content := M{"records": rows, "parse_failures": malformed}
	if malformed > 0 {
		content["text_fallback"] = Redact(fallback)
	}
	reason := ""
	if len(rows) == 0 && malformed == 0 {
		reason = "no_matching_logs"
	}
	result := Result(r, content, reason, trunc || longText(content))
	out, _ := json.Marshal(result)
	if len(out) > MaxBytes {
		return Result(r, nil, "output_limit", true)
	}
	return result
}
