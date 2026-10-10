package evidence

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func profileFixture(t *testing.T) (ProfileDocument, ProfileRequest, time.Time) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	p := Profile{ID: "order-logs", TargetID: "app-a", ServiceID: "order-worker", CheckIDs: []string{"orders-stale"}, Root: t.TempDir(), Files: []string{"current.jsonl", "current.jsonl.1"}, Format: "jsonl", KeyField: "order_id", TimeField: "at"}
	r := ProfileRequest{Version: 2, EventID: uuid.NewString(), CheckID: "orders-stale", TargetID: p.TargetID, ServiceID: p.ServiceID, ProfileID: p.ID, ResourceID: "same-id", Operation: "logs", From: now.Add(-time.Minute).Unix(), To: now.Unix() + 1, Expires: now.Unix() + 30, Limit: 200}
	return ProfileDocument{Version: 1, Profiles: []Profile{p}}, r, now
}

func writeProfileRows(t *testing.T, root, file string, rows ...M) {
	t.Helper()
	var raw []byte
	for _, row := range rows {
		b, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, append(b, '\n')...)
	}
	if err := os.WriteFile(filepath.Join(root, file), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func profileRecords(t *testing.T, result M) []any {
	t.Helper()
	if result["unavailable_reason"] != nil {
		t.Fatalf("unexpected missing evidence: %v", result)
	}
	return result["content"].(map[string]any)["records"].([]any)
}

func TestProfileProtocolAndIdentity(t *testing.T) {
	_, request, now := profileFixture(t)
	key := []byte("isolated-test-signing-key")
	for _, tc := range []struct {
		name string
		edit func(*ProfileRequest)
	}{{"valid", func(*ProfileRequest) {}}, {"unknown_version", func(r *ProfileRequest) { r.Version = 3 }}, {"expired", func(r *ProfileRequest) { r.Expires = now.Unix() - 1 }}, {"future", func(r *ProfileRequest) { r.Expires = now.Unix() + 36 }}, {"window", func(r *ProfileRequest) { r.From -= 1900 }}, {"reverse", func(r *ProfileRequest) { r.From = r.To + 1 }}, {"negative_timestamp", func(r *ProfileRequest) { r.From = -1 }}, {"bad_target", func(r *ProfileRequest) { r.TargetID = "../app" }}, {"operation", func(r *ProfileRequest) { r.Operation = "shell" }}, {"lines", func(r *ProfileRequest) { r.Limit = 201 }}, {"control", func(r *ProfileRequest) { r.ResourceID = "id\n" }}} {
		t.Run(tc.name, func(t *testing.T) {
			r := request
			tc.edit(&r)
			raw, err := SignProfile(r, key)
			if err != nil {
				t.Fatal(err)
			}
			_, err = ValidateProfile(raw, key, now)
			if (err == nil) != (tc.name == "valid") {
				t.Fatal(err)
			}
		})
	}
	raw, _ := SignProfile(request, key)
	if _, err := ValidateProfile(raw, []byte("wrong"), now); err == nil {
		t.Fatal("invalid signature accepted")
	}
	if _, err := ValidateProfile(append(raw, []byte("{}")...), key, now); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	result := ProfileResult(request, M{"records": []any{}}, "", false, now)
	if err := ValidateProfileResult(result, request, now); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"check_id", "target_id", "service_id", "profile_id", "resource_id", "event_id", "operation"} {
		copy := M{}
		for k, v := range result {
			copy[k] = v
		}
		copy[field] = "other"
		if err := ValidateProfileResult(copy, request, now); err == nil {
			t.Fatalf("mismatched %s accepted", field)
		}
	}
	request.Operation = "snapshot"
	snapshot := Snapshot(request, M{"status": "failed", "token": "private"}, now)
	if err := ValidateProfileResult(snapshot, request, now); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "private") {
		t.Fatal("snapshot not redacted")
	}
}

func TestProfileConfigurationAndScope(t *testing.T) {
	doc, request, now := profileFixture(t)
	for _, tc := range []struct {
		name string
		edit func(*Profile)
	}{{"valid", func(*Profile) {}}, {"escape", func(p *Profile) { p.Files = []string{"../secret"} }}, {"absolute", func(p *Profile) { p.Files = []string{"/etc/passwd"} }}, {"glob", func(p *Profile) { p.Files = []string{"app*"} }}, {"unknown_template", func(p *Profile) { p.Files = []string{"{path}.log"} }}, {"relative_root", func(p *Profile) { p.Root = "relative" }}, {"unknown_format", func(p *Profile) { p.Format = "shell" }}, {"key_missing", func(p *Profile) { p.KeyField = "" }}} {
		t.Run(tc.name, func(t *testing.T) {
			p := doc.Profiles[0]
			tc.edit(&p)
			raw, _ := json.Marshal(ProfileDocument{Version: 1, Profiles: []Profile{p}})
			_, err := ParseProfiles(raw)
			if (err == nil) != (tc.name == "valid") {
				t.Fatal(err)
			}
		})
	}
	for _, field := range []string{"check", "target", "service", "profile"} {
		r := request
		switch field {
		case "check":
			r.CheckID = "other"
		case "target":
			r.TargetID = "other"
		case "service":
			r.ServiceID = "other"
		case "profile":
			r.ProfileID = "other"
		}
		if got := CollectProfile(context.Background(), r, doc, now); got["unavailable_reason"] != "profile_denied" {
			t.Fatalf("cross-%s request accepted: %v", field, got)
		}
	}
	p := doc.Profiles[0]
	p.Files = []string{"{resource_id}.log"}
	doc.Profiles = []Profile{p}
	request.ResourceID = "../secret"
	if got := CollectProfile(context.Background(), request, doc, now); got["unavailable_reason"] != "resource_path_denied" {
		t.Fatal("resource traversal accepted", got)
	}
}

func TestProfileLargeLogTailAndRotation(t *testing.T) {
	doc, r, now := profileFixture(t)
	p := &doc.Profiles[0]
	older := M{"order_id": r.ResourceID, "at": now.Add(-20 * time.Second).Unix(), "message": "rotation-match"}
	newer := M{"order_id": r.ResourceID, "at": now.Add(-time.Second).Unix(), "message": "latest-match", "password": "private"}
	other := M{"order_id": "other", "at": now.Unix(), "message": "private-other-object"}
	writeProfileRows(t, p.Root, p.Files[1], older)
	writeProfileRows(t, p.Root, p.Files[0], newer, other)
	rows := profileRecords(t, CollectProfile(context.Background(), r, doc, now))
	if len(rows) != 2 || rows[0].(map[string]any)["message"] != "rotation-match" || rows[1].(map[string]any)["password"] != "[REDACTED]" {
		t.Fatalf("rotation and scope failed: %v", rows)
	}
	path := filepath.Join(p.Root, p.Files[0])
	first := strings.Repeat(`{"order_id":"old","at":1,"message":"old"}`+"\n", 6000)
	last, _ := json.Marshal(newer)
	if err := os.WriteFile(path, append([]byte(first), append(last, '\n')...), 0600); err != nil {
		t.Fatal(err)
	}
	result := CollectProfile(context.Background(), r, doc, now)
	rows = profileRecords(t, result)
	if len(rows) != 1 || rows[0].(map[string]any)["message"] != "latest-match" || result["truncated"] != true {
		t.Fatalf("newest large-file log missed: %v", result)
	}
	r.Limit = 1
	writeProfileRows(t, p.Root, p.Files[0], newer)
	result = CollectProfile(context.Background(), r, doc, now)
	if rows := profileRecords(t, result); len(rows) != 1 || rows[0].(map[string]any)["message"] != "latest-match" || result["truncated"] != true {
		t.Fatalf("record limit failed: %v", result)
	}
}

func TestProfileLongLinePathsAndCancellation(t *testing.T) {
	doc, r, now := profileFixture(t)
	p := &doc.Profiles[0]
	p.Files = []string{"app.jsonl"}
	row, _ := json.Marshal(M{"order_id": r.ResourceID, "at": now.Unix(), "message": "last"})
	path := filepath.Join(p.Root, p.Files[0])
	if err := os.WriteFile(path, append([]byte(strings.Repeat("x", MaxBytes*2)+"\n"), append(row, '\n')...), 0600); err != nil {
		t.Fatal(err)
	}
	if rows := profileRecords(t, CollectProfile(context.Background(), r, doc, now)); len(rows) != 1 {
		t.Fatal("partial long line treated as a record", rows)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", MaxBytes*2)), 0600); err != nil {
		t.Fatal(err)
	}
	result := CollectProfile(context.Background(), r, doc, now)
	if result["truncated"] != true || result["unavailable_reason"] != "no_matching_logs" {
		t.Fatal("long line handling failed", result)
	}
	outside := filepath.Join(t.TempDir(), "private")
	os.WriteFile(outside, row, 0600)
	os.Remove(path)
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadTail(p.Root, p.Files[0], MaxBytes); err == nil {
		t.Fatal("symlink followed")
	}
	if _, _, err := ReadTail(p.Root, "../private", MaxBytes); err == nil {
		t.Fatal("out-of-root file accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result := CollectProfile(ctx, r, doc, now); result["unavailable_reason"] != "collection_canceled" {
		t.Fatal(result)
	}
}

func TestProfileTextAndHeartbeat(t *testing.T) {
	doc, r, now := profileFixture(t)
	p := &doc.Profiles[0]
	p.Format, p.Files = "text", []string{"app.log"}
	p.KeyPattern, p.TimePattern, p.TimeLayout = `\bobject=([^ ]+)`, `^([^ ]+)`, time.RFC3339
	line := now.Format(time.RFC3339) + " object=" + r.ResourceID + " error token=private\n"
	line += now.Format(time.RFC3339) + " object=other hidden-other\n"
	os.WriteFile(filepath.Join(p.Root, p.Files[0]), []byte(line), 0600)
	result := CollectProfile(context.Background(), r, doc, now)
	rows := profileRecords(t, result)
	encoded, _ := json.Marshal(rows)
	if len(rows) != 1 || strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "hidden-other") {
		t.Fatal(string(encoded))
	}
	p.Format, p.Files, p.TimeField, p.TimeLayout, p.FreshnessSeconds = "heartbeat", []string{"worker.json"}, "at", "", 30
	r.Operation = "service_status"
	heartbeat, _ := json.Marshal(M{"service_id": p.ServiceID, "at": now.Add(-20 * time.Second).Unix()})
	os.WriteFile(filepath.Join(p.Root, p.Files[0]), heartbeat, 0600)
	result = CollectProfile(context.Background(), r, doc, now)
	if result["unavailable_reason"] != nil || result["content"].(map[string]any)["heartbeat_fresh"] != true {
		t.Fatal(result)
	}
	heartbeat, _ = json.Marshal(M{"service_id": "other-service", "at": now.Unix()})
	os.WriteFile(filepath.Join(p.Root, p.Files[0]), heartbeat, 0600)
	if result := CollectProfile(context.Background(), r, doc, now); result["unavailable_reason"] != "invalid_heartbeat" {
		t.Fatal("cross-service heartbeat accepted", result)
	}
}

func TestProfileSameObjectOnDifferentServices(t *testing.T) {
	doc, r, now := profileFixture(t)
	first := doc.Profiles[0]
	second := first
	second.ID, second.TargetID, second.ServiceID, second.Root = "other-logs", "app-b", "billing-worker", t.TempDir()
	doc.Profiles = append(doc.Profiles, second)
	writeProfileRows(t, first.Root, first.Files[0], M{"order_id": r.ResourceID, "at": now.Unix(), "message": "order"})
	writeProfileRows(t, second.Root, second.Files[0], M{"order_id": r.ResourceID, "at": now.Unix(), "message": "billing"})
	r.TargetID, r.ServiceID, r.ProfileID = second.TargetID, second.ServiceID, second.ID
	rows := profileRecords(t, CollectProfile(context.Background(), r, doc, now))
	if len(rows) != 1 || rows[0].(map[string]any)["message"] != "billing" {
		t.Fatal("same-ID service scopes mixed", rows)
	}
}

func TestProfileNumericObjectAndUnavailableFiles(t *testing.T) {
	doc, r, now := profileFixture(t)
	r.ResourceID = "9007199254740993"
	p := &doc.Profiles[0]
	// Write an integer identifier that cannot be represented exactly by float64.
	row := M{"order_id": json.Number(r.ResourceID), "at": now.Unix(), "message": "precise-id"}
	writeProfileRows(t, p.Root, p.Files[0], row)
	result := CollectProfile(context.Background(), r, doc, now)
	rows := profileRecords(t, result)
	if len(rows) != 1 || rows[0].(map[string]any)["order_id"] != json.Number(r.ResourceID) || result["content"].(map[string]any)["files_unavailable"] != json.Number("1") {
		t.Fatal("numeric identifier or missing rotation failed", result)
	}
	// Named maps must not bypass recursive redaction in database snapshots.
	type namedMap map[string]any
	snapshot := Snapshot(r, namedMap{"password": "private-value"}, now)
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "private-value") {
		t.Fatal("named map bypassed sanitization")
	}
}

func TestProfileResourceLimitMatchesSQLIdentity(t *testing.T) {
	doc, r, now := profileFixture(t)
	r.ResourceID = strings.Repeat("r", 512)
	key := []byte("test-key")
	raw, _ := SignProfile(r, key)
	if _, err := ValidateProfile(raw, key, now); err != nil {
		t.Fatal("SQL's 512-byte resource identity rejected", err)
	}
	writeProfileRows(t, doc.Profiles[0].Root, doc.Profiles[0].Files[0], M{"order_id": r.ResourceID, "at": now.Unix(), "message": "long-identity-match"})
	if rows := profileRecords(t, CollectProfile(context.Background(), r, doc, now)); len(rows) != 1 {
		t.Fatal("long identity could not associate shared logs", rows)
	}
	doc.Profiles[0].Files = []string{"tasks/{resource_id}.jsonl"}
	if result := CollectProfile(context.Background(), r, doc, now); result["unavailable_reason"] != "resource_path_denied" {
		t.Fatal("long identity relaxed path template restriction", result)
	}
	r.ResourceID += "r"
	raw, _ = SignProfile(r, key)
	if _, err := ValidateProfile(raw, key, now); err == nil {
		t.Fatal("identity exceeding SQL limit accepted")
	}
}
