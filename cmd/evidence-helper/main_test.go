package main

import (
	"bidmon/internal/evidence"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestHelperChild(t *testing.T) {
	if os.Getenv("BID_MON_HELPER_TEST_CHILD") == "1" {
		main()
		os.Exit(0)
	}
}

func TestHelperProfileProtocol(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	profile := evidence.Profile{ID: "logs", TargetID: "app-a", ServiceID: "worker", CheckIDs: []string{"orders"}, Root: root, Files: []string{"app.jsonl"}, Format: "jsonl", KeyField: "object_id", TimeField: "at"}
	document, _ := json.Marshal(evidence.ProfileDocument{Version: 1, Profiles: []evidence.Profile{profile}})
	configPath, keyPath := filepath.Join(root, "profiles.json"), filepath.Join(root, "binding.key")
	os.WriteFile(configPath, document, 0600)
	key := []byte("isolated-subprocess-test-key")
	os.WriteFile(keyPath, key, 0600)
	line, _ := json.Marshal(map[string]any{"object_id": "one", "at": now.Unix(), "message": "model timeout", "token": "private"})
	os.WriteFile(filepath.Join(root, "app.jsonl"), append(line, '\n'), 0600)
	r := evidence.ProfileRequest{Version: 2, EventID: uuid.NewString(), CheckID: "orders", TargetID: "app-a", ServiceID: "worker", ProfileID: "logs", ResourceID: "one", Operation: "logs", From: now.Unix() - 10, To: now.Unix() + 1, Limit: 10, Expires: now.Unix() + 30}
	raw, _ := evidence.SignProfile(r, key)
	for _, command := range []string{"bid-mon-evidence", "presales-diag", "touch /tmp/escaped"} {
		t.Run(command, func(t *testing.T) {
			child := exec.Command(os.Args[0], "-test.run=^TestHelperChild$")
			child.Env = append(os.Environ(), "BID_MON_HELPER_TEST_CHILD=1", "SSH_ORIGINAL_COMMAND="+command, "BID_MON_HELPER_PROFILES_FILE="+configPath, "BID_MON_HELPER_BINDING_KEY_FILE="+keyPath)
			child.Stdin = bytes.NewReader(raw)
			output, err := child.Output()
			if command == "touch /tmp/escaped" {
				if err == nil || string(output) != `{"error":"request_denied"}` {
					t.Fatalf("arbitrary command accepted: %s, %v", output, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("helper rejected v2 request: %s, %v", output, err)
			}
			var result evidence.M
			if json.Unmarshal(output, &result) != nil || evidence.ValidateProfileResult(result, r, time.Now()) != nil || strings.Contains(string(output), "private") {
				t.Fatalf("invalid helper evidence: %s", output)
			}
		})
	}
}
