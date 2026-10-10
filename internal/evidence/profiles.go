package evidence

// Profiles describe what a helper may read. Requests only reference a profile;
// paths, regular expressions and commands never come from the SSH caller.
import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

type ProfileRequest struct {
	Version    int    `json:"version"`
	EventID    string `json:"event_id"`
	CheckID    string `json:"check_id"`
	TargetID   string `json:"target_id"`
	ServiceID  string `json:"service_id"`
	ProfileID  string `json:"profile_id"`
	ResourceID string `json:"resource_id"`
	Operation  string `json:"operation"`
	From       int64  `json:"from_ts"`
	To         int64  `json:"to_ts"`
	Limit      int    `json:"limit"`
	Expires    int64  `json:"expires"`
}

type ProfileDocument struct {
	Version  int       `json:"version"`
	Profiles []Profile `json:"profiles"`
}

type Profile struct {
	ID               string   `json:"id"`
	TargetID         string   `json:"target_id"`
	ServiceID        string   `json:"service_id"`
	CheckIDs         []string `json:"check_ids"`
	Root             string   `json:"root"`
	Files            []string `json:"files"`
	Format           string   `json:"format"`
	KeyField         string   `json:"key_field,omitempty"`
	TimeField        string   `json:"time_field,omitempty"`
	TimeLayout       string   `json:"time_layout,omitempty"`
	KeyPattern       string   `json:"key_pattern,omitempty"`
	TimePattern      string   `json:"time_pattern,omitempty"`
	FreshnessSeconds int      `json:"freshness_seconds,omitempty"`
	MaxBytes         int      `json:"max_bytes,omitempty"`
	MaxLineBytes     int      `json:"max_line_bytes,omitempty"`
}

var profileIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,79}$`)
var pathResourcePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var unavailableCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,79}$`)

func validResource(s string) bool {
	return s != "" && strings.TrimSpace(s) == s && len(s) <= 512 && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

func decodeStrict(raw []byte, into any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(into) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid_json")
	}
	return nil
}

func SignProfile(r ProfileRequest, key []byte) ([]byte, error) {
	body, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	h := hmac.New(sha256.New, key)
	h.Write(body)
	return json.Marshal(Envelope{Body: body, Signature: hex.EncodeToString(h.Sum(nil))})
}

func ValidateProfile(raw, key []byte, now time.Time) (ProfileRequest, error) {
	var r ProfileRequest
	var env Envelope
	if len(raw) > 4096 || decodeStrict(raw, &env) != nil {
		return r, errors.New("invalid_envelope")
	}
	sig, err := hex.DecodeString(env.Signature)
	h := hmac.New(sha256.New, key)
	h.Write(env.Body)
	if err != nil || !hmac.Equal(sig, h.Sum(nil)) {
		return r, errors.New("invalid_signature")
	}
	if decodeStrict(env.Body, &r) != nil {
		return r, errors.New("invalid_body")
	}
	if err := ValidateProfileScope(r, now); err != nil {
		return r, err
	}
	return r, nil
}

func ValidateProfileScope(r ProfileRequest, now time.Time) error {
	if _, err := uuid.Parse(r.EventID); err != nil || r.Version != 2 || !profileIDPattern.MatchString(r.CheckID) || !profileIDPattern.MatchString(r.TargetID) || !profileIDPattern.MatchString(r.ServiceID) || !profileIDPattern.MatchString(r.ProfileID) || !validResource(r.ResourceID) || (r.Operation != "logs" && r.Operation != "service_status") || r.Expires < now.Unix() || r.Expires > now.Unix()+35 || r.From < 0 || r.From >= r.To || r.From < r.To-1800 || r.To > now.Unix()+5 || r.Limit < 1 || r.Limit > 200 {
		return errors.New("request_denied")
	}
	return nil
}

func safeProfilePath(s string) bool {
	if s == "" || strings.ContainsAny(s, `\:*?[]`) || strings.ContainsRune(s, 0) || strings.HasPrefix(s, "/") {
		return false
	}
	template := strings.ReplaceAll(s, "{resource_id}", "resource")
	return !strings.ContainsAny(template, "{}") && path.Clean(template) == template && template != "." && template != ".." && !strings.HasPrefix(template, "../")
}

func ReadProfiles(file string) (ProfileDocument, error) {
	f, err := os.Open(file)
	if err != nil {
		return ProfileDocument{}, errors.New("profiles_unavailable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (256<<10)+1))
	if err != nil || len(raw) > 256<<10 {
		return ProfileDocument{}, errors.New("profiles_invalid")
	}
	return ParseProfiles(raw)
}

func ParseProfiles(raw []byte) (ProfileDocument, error) {
	var d ProfileDocument
	if len(raw) > 256<<10 || decodeStrict(raw, &d) != nil || d.Version != 1 || len(d.Profiles) < 1 || len(d.Profiles) > 100 {
		return d, errors.New("profiles_invalid")
	}
	ids := map[string]bool{}
	for _, p := range d.Profiles {
		if !profileIDPattern.MatchString(p.ID) || ids[p.ID] || !profileIDPattern.MatchString(p.TargetID) || !profileIDPattern.MatchString(p.ServiceID) || !filepath.IsAbs(p.Root) || len(p.CheckIDs) < 1 || len(p.CheckIDs) > 100 || len(p.Files) < 1 || len(p.Files) > 8 || p.MaxBytes < 0 || p.MaxBytes > MaxBytes || p.MaxLineBytes < 0 || p.MaxLineBytes > 16384 {
			return d, errors.New("profiles_invalid")
		}
		ids[p.ID] = true
		checks := map[string]bool{}
		for _, id := range p.CheckIDs {
			if !profileIDPattern.MatchString(id) || checks[id] {
				return d, errors.New("profiles_invalid")
			}
			checks[id] = true
		}
		for _, file := range p.Files {
			if !safeProfilePath(file) {
				return d, errors.New("profiles_invalid")
			}
		}
		switch p.Format {
		case "jsonl":
			if p.KeyField == "" || p.TimeField == "" {
				return d, errors.New("profiles_invalid")
			}
		case "text":
			for _, pattern := range []string{p.KeyPattern, p.TimePattern} {
				r, err := regexp.Compile(pattern)
				if err != nil || len(pattern) > 1024 || r.NumSubexp() != 1 {
					return d, errors.New("profiles_invalid")
				}
			}
			if p.TimeLayout == "" {
				return d, errors.New("profiles_invalid")
			}
		case "heartbeat":
			if p.TimeField == "" || len(p.Files) != 1 || p.FreshnessSeconds < 1 || p.FreshnessSeconds > 86400 {
				return d, errors.New("profiles_invalid")
			}
		default:
			return d, errors.New("profiles_invalid")
		}
	}
	return d, nil
}

func (d ProfileDocument) Resolve(r ProfileRequest) (Profile, error) {
	for _, p := range d.Profiles {
		if p.ID != r.ProfileID || p.TargetID != r.TargetID || p.ServiceID != r.ServiceID {
			continue
		}
		for _, check := range p.CheckIDs {
			if check == r.CheckID && ((p.Format == "heartbeat" && r.Operation == "service_status") || (p.Format != "heartbeat" && r.Operation == "logs")) {
				return p, nil
			}
		}
	}
	return Profile{}, errors.New("profile_denied")
}

func ProfileResult(r ProfileRequest, content any, unavailable string, truncated bool, now time.Time) M {
	var reason any
	if unavailable != "" {
		reason = unavailable
	}
	// Normalize named maps/structs too: Sanitize's recursive map handling should
	// not be bypassed when an adapter uses its own observation type.
	var normalized any
	raw, err := json.Marshal(content)
	if err == nil {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		err = decoder.Decode(&normalized)
	}
	if err != nil {
		normalized, reason = nil, "invalid_content"
	}
	return M{"version": 2, "evidence_id": "ev-" + strings.ReplaceAll(uuid.NewString(), "-", ""), "event_id": r.EventID, "check_id": r.CheckID, "target_id": r.TargetID, "service_id": r.ServiceID, "profile_id": r.ProfileID, "resource_id": r.ResourceID, "operation": r.Operation, "source": "ssh-helper", "collected_at": now.UTC().Format(time.RFC3339Nano), "content": Sanitize(normalized), "truncated": truncated || longText(normalized), "unavailable_reason": reason}
}

func Snapshot(r ProfileRequest, content any, now time.Time) M {
	r.Operation = "snapshot"
	result := ProfileResult(r, content, "", longText(content), now)
	result["source"] = "monitor-db"
	if raw, _ := json.Marshal(result); len(raw) > MaxBytes {
		result["content"], result["truncated"], result["unavailable_reason"] = nil, true, "output_limit"
	}
	return result
}

func ValidateProfileResult(result M, expected ProfileRequest, now time.Time) error {
	if result["version"] != 2 && result["version"] != float64(2) && result["version"] != json.Number("2") {
		return errors.New("invalid_evidence")
	}
	for k, v := range map[string]string{"event_id": expected.EventID, "check_id": expected.CheckID, "target_id": expected.TargetID, "service_id": expected.ServiceID, "profile_id": expected.ProfileID, "resource_id": expected.ResourceID, "operation": expected.Operation} {
		if result[k] != v {
			return errors.New("evidence_identity_mismatch")
		}
	}
	id, _ := result["evidence_id"].(string)
	if len(id) != 35 || !strings.HasPrefix(id, "ev-") {
		return errors.New("invalid_evidence")
	}
	if _, err := hex.DecodeString(id[3:]); err != nil {
		return errors.New("invalid_evidence")
	}
	source := "ssh-helper"
	if expected.Operation == "snapshot" || expected.Operation == "details" {
		source = "monitor-db"
	}
	stamp, _ := result["collected_at"].(string)
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil || now.Sub(at) > time.Minute || at.Sub(now) > time.Minute || result["source"] != source {
		return errors.New("invalid_evidence")
	}
	if _, ok := result["truncated"].(bool); !ok {
		return errors.New("invalid_evidence")
	}
	if _, ok := result["content"]; !ok {
		return errors.New("invalid_evidence")
	}
	if _, ok := result["unavailable_reason"]; !ok {
		return errors.New("invalid_evidence")
	}
	if result["unavailable_reason"] != nil {
		if reason, ok := result["unavailable_reason"].(string); !ok || !unavailableCodePattern.MatchString(reason) {
			return errors.New("invalid_evidence")
		}
	} else if result["content"] == nil {
		return errors.New("invalid_evidence")
	}
	if raw, err := json.Marshal(result); err != nil || len(raw) > MaxBytes {
		return errors.New("invalid_evidence")
	}
	return nil
}

// ReadTail only opens an authorized regular file beneath root. It discards an
// incomplete first line rather than treating a slice of a long line as a record.
func ReadTail(root, relative string, limit int) ([]byte, bool, error) {
	if !safeProfilePath(relative) || limit < 1 || limit > MaxBytes {
		return nil, false, errors.New("path_denied")
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, false, err
	}
	defer dir.Close()
	info, err := dir.Lstat(relative)
	if err != nil || !info.Mode().IsRegular() {
		return nil, false, errors.New("file_type_denied")
	}
	f, err := dir.OpenFile(relative, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, false, errors.New("file_type_denied")
	}
	size := info.Size()
	start := max(int64(0), size-int64(limit))
	readAt := start
	if start > 0 {
		readAt--
	}
	b := make([]byte, int(size-readAt))
	n, err := f.ReadAt(b, readAt)
	if err != nil && err != io.EOF {
		return nil, false, err
	}
	b = b[:n]
	truncated := start > 0 || n != int(size-readAt)
	if start > 0 && len(b) > 0 {
		previous := b[0]
		b = b[1:]
		if previous != '\n' {
			if i := bytes.IndexByte(b, '\n'); i >= 0 {
				b = b[i+1:]
			} else {
				b = nil
			}
		}
	}
	return b, truncated, nil
}

func profileTime(value any, layout string) (time.Time, bool) {
	switch v := value.(type) {
	case json.Number:
		n, err := v.Float64()
		if err != nil {
			return time.Time{}, false
		}
		return profileTime(n, layout)
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1e12 {
			return time.Time{}, false
		}
		seconds, fraction := math.Modf(v)
		return time.Unix(int64(seconds), int64(fraction*1e9)).UTC(), true
	case string:
		if layout == "" {
			layout = time.RFC3339Nano
		}
		t, err := time.Parse(layout, v)
		return t, err == nil
	default:
		return time.Time{}, false
	}
}

func resourceValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return ""
	}
}

func profileField(m M, field string) any {
	var current any = map[string]any(m)
	for _, key := range strings.Split(field, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = object[key]
	}
	return current
}

func CollectProfile(ctx context.Context, r ProfileRequest, document ProfileDocument, now time.Time) M {
	fail := func(reason string) M { return ProfileResult(r, nil, reason, false, now) }
	if err := ValidateProfileScope(r, now); err != nil {
		return fail("request_denied")
	}
	p, err := document.Resolve(r)
	if err != nil || !validResource(r.ResourceID) || r.Limit < 1 || r.Limit > 200 {
		return fail("profile_denied")
	}
	budget := p.MaxBytes
	if budget == 0 {
		budget = MaxBytes
	}
	maxLine := p.MaxLineBytes
	if maxLine == 0 {
		maxLine = 8192
	}
	var keyRE, timeRE *regexp.Regexp
	if p.Format == "text" {
		keyRE, err = regexp.Compile(p.KeyPattern)
		if err != nil {
			return fail("profiles_invalid")
		}
		timeRE, err = regexp.Compile(p.TimePattern)
		if err != nil {
			return fail("profiles_invalid")
		}
	}
	type entry struct {
		at  time.Time
		row any
	}
	rows := []entry{}
	truncated, parseFailures, filesUnavailable, oversized := false, 0, 0, 0
	for index, template := range p.Files {
		if ctx.Err() != nil {
			return fail("collection_canceled")
		}
		if budget < 1 {
			truncated = true
			break
		}
		file := template
		if strings.Contains(file, "{resource_id}") {
			if !pathResourcePattern.MatchString(r.ResourceID) {
				return fail("resource_path_denied")
			}
			file = strings.ReplaceAll(file, "{resource_id}", r.ResourceID)
		}
		raw, cut, err := ReadTail(p.Root, file, budget)
		if err != nil {
			filesUnavailable++
			continue
		}
		truncated = truncated || cut
		// A tail operation has consumed its full read allowance if the file was
		// truncated, even if a partial first line was discarded.
		if cut {
			budget = 0
		} else {
			budget -= len(raw)
		}
		if p.Format == "heartbeat" {
			var heartbeat M
			if cut || json.Unmarshal(raw, &heartbeat) != nil {
				return fail("invalid_heartbeat")
			}
			at, ok := profileTime(profileField(heartbeat, p.TimeField), p.TimeLayout)
			age := now.Sub(at).Seconds()
			if !ok || age < 0 || age > 86400 || (heartbeat["service_id"] != nil && heartbeat["service_id"] != p.ServiceID) {
				return fail("invalid_heartbeat")
			}
			return ProfileResult(r, M{"heartbeat_age_seconds": age, "heartbeat_fresh": age <= float64(p.FreshnessSeconds), "process_state": "not_observed", "service_id": p.ServiceID}, "", false, now)
		}
		for _, line := range bytes.Split(raw, []byte("\n")) {
			if ctx.Err() != nil {
				return fail("collection_canceled")
			}
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			if len(line) > maxLine {
				oversized++
				truncated = true
				continue
			}
			var at time.Time
			var row any
			var ok bool
			if p.Format == "jsonl" {
				var record M
				decoder := json.NewDecoder(bytes.NewReader(line))
				decoder.UseNumber()
				if decoder.Decode(&record) != nil || decoder.Decode(new(any)) != io.EOF {
					parseFailures++
					continue
				}
				if resourceValue(profileField(record, p.KeyField)) != r.ResourceID {
					continue
				}
				at, ok = profileTime(profileField(record, p.TimeField), p.TimeLayout)
				row = record
			} else {
				key := keyRE.FindSubmatch(line)
				if len(key) != 2 || string(key[1]) != r.ResourceID {
					continue
				}
				stamp := timeRE.FindSubmatch(line)
				if len(stamp) == 2 {
					at, ok = profileTime(string(stamp[1]), p.TimeLayout)
				}
				row = M{"message": Redact(string(line)), "at": at.UTC().Format(time.RFC3339Nano), "resource_id": r.ResourceID}
			}
			if !ok {
				parseFailures++
				continue
			}
			if at.Unix() < r.From || at.Unix() > r.To {
				continue
			}
			rows = append(rows, entry{at: at, row: row})
		}
		if budget < 1 && index < len(p.Files)-1 {
			truncated = true
		}
	}
	if len(rows) == 0 && filesUnavailable == len(p.Files) {
		return fail("evidence_unavailable")
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].at.Before(rows[j].at) })
	if len(rows) > r.Limit {
		rows = rows[len(rows)-r.Limit:]
		truncated = true
	}
	records := []any{}
	for _, row := range rows {
		records = append(records, row.row)
	}
	content := M{"records": records, "parse_failures": parseFailures, "files_unavailable": filesUnavailable, "oversized_lines": oversized}
	reason := ""
	if len(records) == 0 {
		reason = "no_matching_logs"
	}
	result := ProfileResult(r, content, reason, truncated || longText(content), now)
	for {
		raw, err := json.Marshal(result)
		if err == nil && len(raw) <= MaxBytes {
			return result
		}
		if len(records) == 0 {
			return ProfileResult(r, nil, "output_limit", true, now)
		}
		records = records[1:]
		content["records"] = records
		result["content"] = Sanitize(content)
		result["truncated"] = true
	}
}
