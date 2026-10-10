package bootstrap

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

func runEvidence(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("init-evidence", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	client := fs.String("client-dir", "/ssh-client", "SSH client volume")
	target := fs.String("target-dir", "/ssh-target", "SSH server volume")
	evidence := fs.String("evidence-dir", "/evidence", "evidence volume")
	host := fs.String("host", "ssh-target", "pinned server name")
	clientUID := fs.Int("client-uid", 10001, "client owner UID")
	clientGID := fs.Int("client-gid", 10001, "client owner GID")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("invalid init-evidence arguments")
	}
	if *host == "" || strings.ContainsAny(*host, " \t\r\n") {
		return fmt.Errorf("invalid SSH host name")
	}
	return initializeEvidence(*client, *target, *evidence, *host, *clientUID, *clientGID, out)
}

func initializeEvidence(client, target, evidence, host string, uid, gid int, out io.Writer) error {
	for _, dir := range []string{client, target, evidence, filepath.Join(evidence, "tasks")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("cannot create SSH/evidence volume directory")
		}
	}
	public, err := keypair(filepath.Join(client, "id_ed25519"), uid, gid)
	if err != nil {
		return err
	}
	hostUID, hostGID := 0, 10001
	if uid < 0 {
		hostUID, hostGID = -1, -1
	}
	hostKey, err := keypair(filepath.Join(target, "host_key"), hostUID, hostGID)
	if err != nil {
		return err
	}
	for path, raw := range map[string][]byte{
		filepath.Join(target, "authorized_keys"): append([]byte(`restrict,command="/opt/bid-mon-helper" `), public...),
		filepath.Join(client, "known_hosts"):     append([]byte(host+" "), hostKey...),
	} {
		ownerUID, ownerGID := uid, gid
		if filepath.Dir(path) == target {
			ownerUID, ownerGID = 10001, 10001
		}
		if uid < 0 {
			ownerUID, ownerGID = -1, -1
		}
		if _, err := createFile(path, raw, 0600, ownerUID, ownerGID); err != nil {
			return err
		}
		existing, err := os.ReadFile(path)
		if err != nil || strings.TrimSpace(string(existing)) != strings.TrimSpace(string(raw)) {
			return fmt.Errorf("existing SSH public identity differs; files were preserved")
		}
	}
	clientBinding := filepath.Join(client, "binding.key")
	if _, err := os.Stat(clientBinding); os.IsNotExist(err) {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return fmt.Errorf("secure binding key generation failed")
		}
		if _, err := createFile(clientBinding, raw, 0600, uid, gid); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("cannot inspect client binding key")
	}
	key, err := os.ReadFile(clientBinding)
	if err != nil || len(key) != 32 {
		return fmt.Errorf("invalid existing client binding key; not replaced")
	}
	targetBinding := filepath.Join(target, "binding.key")
	targetUID, targetGID := 10001, 10001
	if uid < 0 {
		targetUID, targetGID = -1, -1
	}
	if _, err := createFile(targetBinding, key, 0600, targetUID, targetGID); err != nil {
		return err
	}
	existing, err := os.ReadFile(targetBinding)
	if err != nil || string(existing) != string(key) {
		return fmt.Errorf("client and target binding keys differ; existing keys were preserved")
	}
	for _, path := range []string{evidence, filepath.Join(evidence, "tasks")} {
		if uid >= 0 {
			if err := setOwner(path, 10001, 10001); err != nil {
				return err
			}
		}
	}
	_, err = fmt.Fprintln(out, "SSH identities and evidence volume initialized; existing keys preserved; secret values not printed.")
	return err
}

func keypair(path string, uid, gid int) ([]byte, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		_, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("SSH key generation failed")
		}
		block, err := ssh.MarshalPrivateKey(private, "bid-mon installation")
		if err != nil {
			return nil, fmt.Errorf("SSH key encoding failed")
		}
		if _, err := createFile(path, pem.EncodeToMemory(block), 0600, uid, gid); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("cannot inspect existing SSH key")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read existing SSH key")
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid existing SSH key; not replaced")
	}
	return ssh.MarshalAuthorizedKey(signer.PublicKey()), nil
}
