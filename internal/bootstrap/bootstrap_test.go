package bootstrap

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func options(dir string) initOptions {
	return initOptions{Dir: dir, Mode: "business", Image: DefaultImage, Instance: "test-monitor", Port: 18880, UID: -1, GID: -1}
}

func TestIndependentInstallationsDoNotShareCredentials(t *testing.T) {
	var previous map[string]string
	var previousMaster []byte
	for i := 0; i < 2; i++ {
		dir := t.TempDir()
		var output bytes.Buffer
		if err := initialize(options(dir), &output); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, ".env"))
		if err != nil {
			t.Fatal(err)
		}
		values, err := parseEnv(raw)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range secretKeys() {
			if len(values[name]) != 64 || strings.Contains(output.String(), values[name]) {
				t.Fatal("invalid or logged secret", name)
			}
			if previous != nil && values[name] == previous[name] {
				t.Fatal("shared credential", name)
			}
		}
		var credentials map[string]string
		raw, err = os.ReadFile(filepath.Join(dir, "deploy", "phase2", "config", "credentials.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &credentials); err != nil {
			t.Fatal(err)
		}
		if credentials["admin_token"] != values["MON_ADMIN_TOKEN"] {
			t.Fatal("credential mismatch")
		}
		previous = values
		master, err := os.ReadFile(filepath.Join(dir, "deploy", "phase2", "config", "master.key"))
		if err != nil {
			t.Fatal(err)
		}
		if len(bytes.TrimSpace(master)) != 64 || bytes.Contains([]byte(output.String()), bytes.TrimSpace(master)) {
			t.Fatal("invalid or logged encryption key")
		}
		if previousMaster != nil && bytes.Equal(previousMaster, master) {
			t.Fatal("shared encryption key")
		}
		previousMaster = master
	}
}

func TestRepeatedInitPreservesCredentialsAndUserConfig(t *testing.T) {
	dir := t.TempDir()
	if err := initialize(options(dir), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(dir, ".env")
	before, _ := os.ReadFile(envPath)
	masterPath := filepath.Join(dir, "deploy", "phase2", "config", "master.key")
	beforeMaster, _ := os.ReadFile(masterPath)
	path := filepath.Join(dir, "deploy", "phase2", "config", "monitor.json")
	want := []byte(`{"custom":"preserve even when invalid"}`)
	if err := os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	changed := options(dir)
	changed.Port = 19000
	changed.Instance = "different"
	changed.Mode = "demo"
	for _, name := range []string{"demo", "evidence", "helper-profiles"} {
		if err := os.WriteFile(filepath.Join(dir, "deploy", "phase2", name+".template.json"), []byte(`{"version":1,"sources":[],"checks":[],"channels":[]}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := initialize(changed, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(envPath)
	got, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) || !bytes.Equal(want, got) {
		t.Fatal("existing installation overwritten")
	}
	afterMaster, _ := os.ReadFile(masterPath)
	if !bytes.Equal(beforeMaster, afterMaster) {
		t.Fatal("encryption key replaced")
	}
}

func TestInvalidExistingEnvIsPreservedWithoutSecretInError(t *testing.T) {
	dir := t.TempDir()
	want := []byte("MON_ADMIN_TOKEN=do-not-log-this\n")
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	err := initialize(options(dir), &bytes.Buffer{})
	if err == nil || strings.Contains(err.Error(), "do-not-log-this") {
		t.Fatal("expected safe validation error")
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, want) {
		t.Fatal("invalid existing .env was replaced")
	}
}

func TestInitRejectsInvalidArgumentsBeforeWriting(t *testing.T) {
	for _, change := range []func(*initOptions){
		func(o *initOptions) { o.Port = 65536 }, func(o *initOptions) { o.Mode = "invalid" },
		func(o *initOptions) { o.Instance = "bad/path" }, func(o *initOptions) { o.Image = "image\nsecret=bad" },
	} {
		dir := t.TempDir()
		o := options(dir)
		change(&o)
		if err := initialize(o, &bytes.Buffer{}); err == nil {
			t.Fatal("invalid options accepted")
		}
		if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
			t.Fatal("invalid options wrote environment")
		}
	}
}

func TestEvidenceKeysArePreservedAndIdentityFilesAgree(t *testing.T) {
	dir := t.TempDir()
	client, target, evidence := filepath.Join(dir, "client"), filepath.Join(dir, "target"), filepath.Join(dir, "evidence")
	var output bytes.Buffer
	if err := initializeEvidence(client, target, evidence, "ssh-target", -1, -1, &output); err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.Join(client, "id_ed25519"), filepath.Join(target, "host_key"), filepath.Join(client, "binding.key"), filepath.Join(client, "known_hosts"), filepath.Join(target, "authorized_keys")}
	before := map[string][]byte{}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = raw
	}
	if err := initializeEvidence(client, target, evidence, "ssh-target", -1, -1, &output); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		raw, _ := os.ReadFile(path)
		if !bytes.Equal(raw, before[path]) {
			t.Fatal("identity replaced", path)
		}
	}
	signer, err := ssh.ParsePrivateKey(before[paths[0]])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(before[paths[4]], bytes.TrimSpace(ssh.MarshalAuthorizedKey(signer.PublicKey()))) {
		t.Fatal("authorized key differs")
	}
	binding, _ := os.ReadFile(filepath.Join(target, "binding.key"))
	if !bytes.Equal(binding, before[paths[2]]) {
		t.Fatal("binding keys differ")
	}
	if strings.Contains(output.String(), string(before[paths[0]])) || strings.Contains(output.String(), string(binding)) {
		t.Fatal("key leaked")
	}
	if err := os.WriteFile(filepath.Join(target, "binding.key"), bytes.Repeat([]byte{0x01}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if err := initializeEvidence(client, target, evidence, "ssh-target", -1, -1, &output); err == nil {
		t.Fatal("binding mismatch not rejected")
	}
}

func TestInvalidExistingPrivateKeyIsNotReplaced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	want := []byte("invalid-key")
	if err := os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := keypair(path, -1, -1); err == nil {
		t.Fatal("corrupt key accepted")
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, want) {
		t.Fatal("corrupt existing key replaced")
	}
}

func TestConfigValidationNeedsNoRuntimeDatabaseOrCredentials(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checks.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"sources":[],"checks":[],"channels":[],"monitor_presales":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runConfig([]string{"validate", "--file", path}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "OK configuration") {
		t.Fatal("missing validation result")
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"unknown":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runConfig([]string{"validate", "--file", path}, &output); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestDoctorChecksSecretWithoutConnectingOrLoggingIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checks.json")
	t.Setenv("BOOTSTRAP_TEST_DSN", "synthetic-private-dsn-do-not-log")
	raw := []byte(`{"version":1,"sources":[{"id":"sample","driver":"mysql","dsn":{"env":"BOOTSTRAP_TEST_DSN"}}],"checks":[],"channels":[]}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runDoctor([]string{"--file", path}, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "synthetic-private-dsn-do-not-log") || strings.Contains(output.String(), "OK source sample connection") {
		t.Fatal("secret logged or connection pretended")
	}
	t.Setenv("BOOTSTRAP_TEST_DSN", "")
	err := runDoctor([]string{"--file", path}, &output)
	if err == nil || strings.Contains(err.Error(), "synthetic-private-dsn-do-not-log") {
		t.Fatal("expected safe missing reference error")
	}
}

func TestCorruptEncryptionKeyIsPreserved(t *testing.T) {
	dir := t.TempDir()
	if err := initialize(options(dir), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "deploy", "phase2", "config", "master.key")
	want := []byte("do-not-reset-encrypted-data-key")
	if err := os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	if err := initialize(options(dir), &bytes.Buffer{}); err == nil {
		t.Fatal("invalid encryption key accepted")
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, want) {
		t.Fatal("encryption key overwritten")
	}
}
