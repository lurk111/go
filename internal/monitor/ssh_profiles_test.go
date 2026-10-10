package monitor

import (
	"bidmon/internal/evidence"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestEvidenceTargetConfiguration(t *testing.T) {
	c := EvidenceConfig{Version: 1, Targets: []SSHTargetConfig{{ID: "orders-host", Address: "127.0.0.1:22", KeyFile: "/keys/diag", KnownHosts: "/keys/known_hosts", BindingKey: "/keys/binding"}}, Profiles: []evidence.Profile{{ID: "orders-logs", TargetID: "orders-host", ServiceID: "orders-worker", CheckIDs: []string{"orders-delay"}, Root: "/evidence", Files: []string{"orders.jsonl"}, Format: "jsonl", KeyField: "order_id", TimeField: "at"}}}
	raw, _ := json.Marshal(c)
	parsed, err := ParseEvidenceConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	target, profile, err := parsed.Resolve("orders-delay", "orders-logs")
	if err != nil || target.User != "diag" || profile.ID != "orders-logs" {
		t.Fatal(target, profile, err)
	}
	if _, _, err := parsed.Resolve("other", "orders-logs"); err == nil {
		t.Fatal("cross-check profile allowed")
	}
	if len(parsed.HelperProfiles("orders-host").Profiles) != 1 || len(parsed.HelperProfiles("other").Profiles) != 0 {
		t.Fatal("helper configuration mixed targets")
	}
	c.Profiles[0].TargetID = "unknown"
	raw, _ = json.Marshal(c)
	if _, err := ParseEvidenceConfig(raw); err == nil {
		t.Fatal("unknown target accepted")
	}
	if _, err := ParseEvidenceConfig([]byte(`{"version":1,"targets":[],"profiles":[],"command":"shell"}`)); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestProfileSSHFailureIdentity(t *testing.T) {
	now := time.Now()
	r := evidence.ProfileRequest{Version: 2, EventID: uuid.NewString(), CheckID: "orders", TargetID: "app-a", ServiceID: "worker", ProfileID: "logs", ResourceID: "one", Operation: "logs", From: now.Unix() - 20, To: now.Unix(), Limit: 10, Expires: now.Unix() + 30}
	result := CollectProfile(context.Background(), SSHTargetConfig{ID: "app-a", BindingKey: "/nonexistent/test-key"}, r)
	if result["unavailable_reason"] != "binding_unavailable" {
		t.Fatal(result)
	}
	if err := evidence.ValidateProfileResult(evidence.M(result), r, now); err != nil {
		t.Fatal(err)
	}
	if result := CollectProfile(context.Background(), SSHTargetConfig{ID: "other"}, r); result["unavailable_reason"] != "evidence_target_mismatch" {
		t.Fatal("target mismatch accepted", result)
	}
}

func TestIntegrationProfileSSHIdentityAndAuthorization(t *testing.T) {
	if os.Getenv("RUN_GO_PROFILE_SSH") != "1" {
		t.Skip("v2 SSH helper/profile deployment required")
	}
	c, err := LoadEvidenceConfig(os.Getenv("MON_EVIDENCE_CONFIG_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	profile := c.Profiles[0]
	target, _, err := c.Resolve(profile.CheckIDs[0], profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	r := evidence.ProfileRequest{Version: 2, EventID: uuid.NewString(), CheckID: profile.CheckIDs[0], TargetID: profile.TargetID, ServiceID: profile.ServiceID, ProfileID: profile.ID, ResourceID: "profile-test-" + uuid.NewString(), Operation: "logs", From: now.Unix() - 20, To: now.Unix() + 1, Limit: 10, Expires: now.Unix() + 30}
	if profile.Format == "heartbeat" {
		r.Operation = "service_status"
	}
	result := CollectProfile(context.Background(), target, r)
	reason := str(result, "unavailable_reason")
	if reason != "" && reason != "no_matching_logs" && reason != "evidence_unavailable" && reason != "invalid_heartbeat" {
		t.Fatalf("real SSH helper/profile failed: %s", reason)
	}
	if err := evidence.ValidateProfileResult(evidence.M(result), r, time.Now()); err != nil {
		t.Fatal(err)
	}
	r.CheckID = "not-authorized-check"
	result = CollectProfile(context.Background(), target, r)
	if result["unavailable_reason"] != "profile_denied" {
		t.Fatal("real helper allowed cross-check evidence", result["unavailable_reason"])
	}
}
