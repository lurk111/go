package monitor

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestIntegrationSSHRestrictions(t *testing.T) {
	if os.Getenv("RUN_GO_SSH") != "1" {
		t.Skip("Go SSH target required")
	}
	f := fixtureFor(t)
	raw, e := os.ReadFile(f.cfg.KeyFile)
	if e != nil {
		t.Fatal(e)
	}
	signer, e := ssh.ParsePrivateKey(raw)
	if e != nil {
		t.Fatal(e)
	}
	check, e := knownhosts.New(f.cfg.KnownHosts)
	if e != nil {
		t.Fatal(e)
	}
	client, e := ssh.Dial("tcp", f.cfg.SSHHost, &ssh.ClientConfig{User: "diag", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: check, Timeout: 3 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	session, e := client.NewSession()
	if e != nil {
		t.Fatal(e)
	}
	if e = session.Run("touch /tmp/bid-mon-command-escape"); e == nil {
		t.Fatal("arbitrary command accepted")
	}
	session.Close()
	if conn, e := client.Dial("tcp", "postgres:5432"); e == nil {
		conn.Close()
		t.Fatal("port forwarding accepted")
	}
	_, private, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	wrong, e := ssh.NewSignerFromKey(private)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "known_hosts")
	if e = os.WriteFile(path, []byte(knownhosts.Line([]string{f.cfg.SSHHost}, wrong.PublicKey())+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	cfg := f.cfg
	cfg.KnownHosts = path
	result := CollectSSH(f.ctx, cfg, Event{EventID: uuid.NewString(), TaskID: "test-task"}, "worker_status")
	if str(result, "unavailable_reason") == "" {
		t.Fatal("changed host key accepted")
	}
}
