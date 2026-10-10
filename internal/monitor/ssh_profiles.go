package monitor

import (
	"bidmon/internal/evidence"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"regexp"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type SSHTargetConfig struct {
	ID         string `json:"id"`
	Address    string `json:"address"`
	User       string `json:"user"`
	KeyFile    string `json:"key_file"`
	KnownHosts string `json:"known_hosts"`
	BindingKey string `json:"binding_key"`
}

type EvidenceConfig struct {
	Version  int                `json:"version"`
	Targets  []SSHTargetConfig  `json:"targets"`
	Profiles []evidence.Profile `json:"profiles"`
}

var sshTargetID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,79}$`)
var sshUserPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.-]{0,31}$`)

func LoadEvidenceConfig(path string) (*EvidenceConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("evidence_config_unavailable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (256<<10)+1))
	if err != nil || len(raw) > 256<<10 {
		return nil, errors.New("evidence_config_invalid")
	}
	return ParseEvidenceConfig(raw)
}

func ParseEvidenceConfig(raw []byte) (*EvidenceConfig, error) {
	var c EvidenceConfig
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) > 256<<10 || d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || c.Version != 1 || len(c.Targets) < 1 || len(c.Targets) > 100 {
		return nil, errors.New("evidence_config_invalid")
	}
	targets := map[string]bool{}
	for i := range c.Targets {
		t := &c.Targets[i]
		if t.User == "" {
			t.User = "diag"
		}
		host, port, err := net.SplitHostPort(t.Address)
		if !sshTargetID.MatchString(t.ID) || targets[t.ID] || err != nil || host == "" || port == "" || !sshUserPattern.MatchString(t.User) || t.KeyFile == "" || t.KnownHosts == "" || t.BindingKey == "" {
			return nil, errors.New("evidence_config_invalid")
		}
		targets[t.ID] = true
	}
	profilesRaw, _ := json.Marshal(evidence.ProfileDocument{Version: 1, Profiles: c.Profiles})
	if _, err := evidence.ParseProfiles(profilesRaw); err != nil {
		return nil, errors.New("evidence_config_invalid")
	}
	for _, p := range c.Profiles {
		if !targets[p.TargetID] {
			return nil, errors.New("evidence_target_unknown")
		}
	}
	return &c, nil
}

func (c *EvidenceConfig) Resolve(checkID, profileID string) (SSHTargetConfig, evidence.Profile, error) {
	if c != nil {
		for _, p := range c.Profiles {
			if p.ID != profileID {
				continue
			}
			allowed := false
			for _, id := range p.CheckIDs {
				allowed = allowed || id == checkID
			}
			if allowed {
				for _, target := range c.Targets {
					if target.ID == p.TargetID {
						return target, p, nil
					}
				}
			}
		}
	}
	return SSHTargetConfig{}, evidence.Profile{}, errors.New("evidence_profile_denied")
}

// CollectProfile calls one pre-registered target. SSH never executes a caller
// supplied command; host pinning, signed scope and response identity are checked.
func CollectProfile(ctx context.Context, target SSHTargetConfig, request evidence.ProfileRequest) M {
	now := time.Now()
	request.Version = 2
	if request.TargetID == "" {
		request.TargetID = target.ID
	}
	if request.Expires == 0 {
		request.Expires = now.Unix() + 30
	}
	if request.From == 0 && request.To == 0 {
		request.From, request.To = now.Unix()-1799, now.Unix()+1
	}
	if request.Limit == 0 {
		request.Limit = 200
	}
	fail := func(reason string) M { return M(evidence.ProfileResult(request, nil, reason, false, time.Now())) }
	if request.TargetID != target.ID {
		return fail("evidence_target_mismatch")
	}
	key, err := os.ReadFile(target.BindingKey)
	if err != nil {
		return fail("binding_unavailable")
	}
	raw, err := evidence.SignProfile(request, key)
	if err != nil {
		return fail("binding_unavailable")
	}
	if _, err := evidence.ValidateProfile(raw, key, now); err != nil {
		return fail("request_denied")
	}
	private, err := os.ReadFile(target.KeyFile)
	if err != nil {
		return fail("ssh_key_unavailable")
	}
	signer, err := ssh.ParsePrivateKey(private)
	if err != nil {
		return fail("ssh_key_unavailable")
	}
	check, err := knownhosts.New(target.KnownHosts)
	if err != nil {
		return fail("host_key_unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", target.Address)
	if err != nil {
		return fail("ssh_unavailable")
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	user := target.User
	if user == "" {
		user = "diag"
	}
	sc, ch, req, err := ssh.NewClientConn(conn, target.Address, &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: check, Timeout: 3 * time.Second})
	if err != nil {
		return fail("ssh_or_host_key_unavailable")
	}
	client := ssh.NewClient(sc, ch, req)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return fail("ssh_unavailable")
	}
	defer session.Close()
	out := &bounded{Limit: evidence.MaxBytes}
	session.Stdout, session.Stderr, session.Stdin = out, &bounded{Limit: 4096}, bytes.NewReader(raw)
	if err := session.Run("bid-mon-evidence"); err != nil {
		return fail("helper_denied_or_unavailable")
	}
	var result evidence.M
	d := json.NewDecoder(bytes.NewReader(out.Bytes()))
	d.UseNumber()
	if d.Decode(&result) != nil || d.Decode(new(any)) != io.EOF {
		return fail("invalid_evidence")
	}
	if err := evidence.ValidateProfileResult(result, request, time.Now()); err != nil {
		return fail(err.Error())
	}
	result["content"] = evidence.Sanitize(result["content"])
	return M(result)
}

// HelperProfiles emits the independent target-side file without private-key paths.
func (c *EvidenceConfig) HelperProfiles(targetID string) evidence.ProfileDocument {
	d := evidence.ProfileDocument{Version: 1, Profiles: []evidence.Profile{}}
	if c != nil {
		for _, p := range c.Profiles {
			if p.TargetID == targetID {
				d.Profiles = append(d.Profiles, p)
			}
		}
	}
	return d
}
