package bootstrap

import (
	"bidmon/internal/checks"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const DefaultImage = "bid-mon:2.0.0-dev"

var instanceName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)

// Run handles installation commands before runtime configuration loads, without logging secrets.
func Run(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "init":
		return true, runInit(args[1:], os.Stdout)
	case "init-evidence":
		return true, runEvidence(args[1:], os.Stdout)
	case "doctor":
		return true, runDoctor(args[1:], os.Stdout)
	case "config":
		return true, runConfig(args[1:], os.Stdout)
	default:
		return false, nil
	}
}

type initOptions struct {
	Dir, Mode, Image, LabImage, SSHImage, Instance string
	Port, UID, GID                                 int
}

func runInit(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := initOptions{}
	fs.StringVar(&o.Dir, "dir", ".", "installation directory")
	fs.StringVar(&o.Mode, "mode", "business", "business or demo")
	fs.StringVar(&o.Image, "image", DefaultImage, "fixed runtime image")
	fs.StringVar(&o.LabImage, "demo-image", "bid-mon-lab:2.0.0-dev", "fixed demonstration image")
	fs.StringVar(&o.SSHImage, "ssh-image", "bid-mon-go-ssh:2.0.0-dev", "fixed SSH demonstration image")
	fs.StringVar(&o.Instance, "instance", "bid-mon", "Compose project name")
	fs.IntVar(&o.Port, "port", 18080, "HTTP port")
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		uid, gid = 10001, 10001
	}
	fs.IntVar(&o.UID, "uid", uid, "owner and runtime UID")
	fs.IntVar(&o.GID, "gid", gid, "owner and runtime GID")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("invalid init arguments")
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("init does not accept positional arguments")
	}
	return initialize(o, out)
}

func initialize(o initOptions, out io.Writer) error {
	if o.LabImage == "" {
		o.LabImage = "bid-mon-lab:2.0.0-dev"
	}
	if o.SSHImage == "" {
		o.SSHImage = "bid-mon-go-ssh:2.0.0-dev"
	}
	if o.Mode != "business" && o.Mode != "demo" {
		return fmt.Errorf("mode must be business or demo")
	}
	if !instanceName.MatchString(o.Instance) {
		return fmt.Errorf("instance must use lowercase letters, numbers, '-' or '_'")
	}
	if o.Port < 1 || o.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	for _, image := range []string{o.Image, o.LabImage, o.SSHImage} {
		if image == "" || strings.ContainsAny(image, "\r\n \t#$") {
			return fmt.Errorf("invalid image reference")
		}
	}
	if o.UID < -1 || o.GID < -1 {
		return fmt.Errorf("invalid owner ID")
	}
	dir, err := filepath.Abs(o.Dir)
	if err != nil {
		return fmt.Errorf("cannot resolve installation directory")
	}
	if err = os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("cannot create installation directory")
	}
	path := filepath.Join(dir, ".env")
	values := map[string]string{}
	if raw, err := os.ReadFile(path); err == nil {
		values, err = parseEnv(raw)
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("cannot read installation .env")
	} else {
		uid, gid := o.UID, o.GID
		if uid < 0 {
			uid = 10001
		}
		if gid < 0 {
			gid = 10001
		}
		values = map[string]string{
			"COMPOSE_PROJECT_NAME": o.Instance, "BID_MON_MODE": o.Mode,
			"BID_MON_IMAGE": o.Image, "BID_MON_LAB_IMAGE": o.LabImage,
			"BID_MON_SSH_IMAGE": o.SSHImage,
			"BID_MON_HTTP_PORT": strconv.Itoa(o.Port), "BID_MON_BIND_ADDRESS": "127.0.0.1",
			"BID_MON_UID": strconv.Itoa(uid), "BID_MON_GID": strconv.Itoa(gid),
		}
		for _, k := range secretKeys() {
			secret, err := randomSecret()
			if err != nil {
				return err
			}
			values[k] = secret
		}
		var lines []string
		for _, k := range []string{"COMPOSE_PROJECT_NAME", "BID_MON_MODE", "BID_MON_IMAGE", "BID_MON_LAB_IMAGE", "BID_MON_SSH_IMAGE", "BID_MON_BIND_ADDRESS", "BID_MON_HTTP_PORT", "BID_MON_UID", "BID_MON_GID", "POSTGRES_PASSWORD", "MON_ADMIN_TOKEN", "MYSQL_ROOT_PASSWORD", "MYSQL_PASSWORD", "MONITOR_MYSQL_PASSWORD"} {
			lines = append(lines, k+"="+values[k])
		}
		if _, err = createFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600, o.UID, o.GID); err != nil {
			return err
		}
		// Another initializer may have won exclusive creation; use its values.
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("cannot read installation .env")
		}
		values, err = parseEnv(raw)
		if err != nil {
			return err
		}
	}
	for _, k := range secretKeys() {
		if len(values[k]) < 24 {
			return fmt.Errorf("existing .env requires %s (at least 24 characters); existing credentials were preserved", k)
		}
	}
	// Keep the persisted runtime identity on upgrades, including manual root init.
	if o.UID >= 0 {
		for name, destination := range map[string]*int{"BID_MON_UID": &o.UID, "BID_MON_GID": &o.GID} {
			if value := values[name]; value != "" {
				id, err := strconv.Atoi(value)
				if err != nil || id < 0 {
					return fmt.Errorf("invalid existing %s; installation preserved", name)
				}
				*destination = id
			}
		}
	}
	configDir := filepath.Join(dir, "deploy", "phase2", "config")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("cannot create configuration directory")
	}
	if err := setOwner(configDir, o.UID, o.GID); err != nil {
		return err
	}
	for name, contents := range map[string][]byte{
		"monitor.json": []byte("{\n  \"version\": 1,\n  \"sources\": [],\n  \"checks\": [],\n  \"channels\": [],\n  \"monitor_presales\": false\n}\n"),
		"model.json":   []byte("{}\n"),
	} {
		if _, err := createFile(filepath.Join(configDir, name), contents, 0600, o.UID, o.GID); err != nil {
			return err
		}
	}
	// Distributable templates are copied once; installed configuration is preserved.
	for _, name := range []string{"demo", "evidence", "helper-profiles"} {
		template := filepath.Join(dir, "deploy", "phase2", name+".template.json")
		if raw, err := os.ReadFile(template); err == nil {
			mode := os.FileMode(0600)
			if name == "helper-profiles" {
				mode = 0644
			} // Contains only public path/authorization metadata, readable by remote diag UID.
			if _, err := createFile(filepath.Join(configDir, name+".json"), raw, mode, o.UID, o.GID); err != nil {
				return err
			}
		} else if o.Mode == "demo" {
			return fmt.Errorf("%s demo template is missing or unreadable", name)
		}
	}
	credentials, _ := json.MarshalIndent(map[string]string{"admin_token": values["MON_ADMIN_TOKEN"], "usage": "Enter admin_token on the login page. Do not publish this file."}, "", "  ")
	if _, err := createFile(filepath.Join(configDir, "credentials.json"), append(credentials, '\n'), 0600, o.UID, o.GID); err != nil {
		return err
	}
	masterPath := filepath.Join(configDir, "master.key")
	if _, err := os.Stat(masterPath); os.IsNotExist(err) {
		key, err := randomSecret()
		if err != nil {
			return err
		}
		if _, err := createFile(masterPath, []byte(key+"\n"), 0600, o.UID, o.GID); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("cannot inspect installation encryption key")
	}
	master, err := os.ReadFile(masterPath)
	if err != nil {
		return fmt.Errorf("cannot read installation encryption key")
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(string(master)))
	if err != nil || len(decoded) != 32 {
		return fmt.Errorf("invalid installation encryption key; existing key was preserved")
	}
	// Repair ownership only; preserve credential and configuration contents.
	for _, name := range []string{".env", "deploy/phase2/config/monitor.json", "deploy/phase2/config/model.json", "deploy/phase2/config/credentials.json", "deploy/phase2/config/master.key", "deploy/phase2/config/demo.json", "deploy/phase2/config/evidence.json", "deploy/phase2/config/helper-profiles.json"} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("installation file %s is not a regular file", name)
		}
		if err := setOwner(path, o.UID, o.GID); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(out, "Installation initialized; existing files and credentials preserved.\nAdmin credential file: %s\nApplication: http://127.0.0.1:%s\n", filepath.Join(configDir, "credentials.json"), values["BID_MON_HTTP_PORT"])
	return err
}

func secretKeys() []string {
	return []string{"POSTGRES_PASSWORD", "MON_ADMIN_TOKEN", "MYSQL_ROOT_PASSWORD", "MYSQL_PASSWORD", "MONITOR_MYSQL_PASSWORD"}
}

func randomSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("secure random generation failed")
	}
	return hex.EncodeToString(b), nil
}

func parseEnv(raw []byte) (map[string]string, error) {
	result := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("invalid .env entry; contents were not logged")
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && ((v[0] == '\'' && v[len(v)-1] == '\'') || (v[0] == '"' && v[len(v)-1] == '"')) {
			v = v[1 : len(v)-1]
		}
		result[strings.TrimSpace(k)] = v
	}
	return result, nil
}

// createFile uses exclusive creation; existing files are never truncated.
func createFile(path string, raw []byte, mode os.FileMode, uid, gid int) (bool, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if os.IsExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cannot create %s", filepath.Base(path))
	}
	_, writeErr := f.Write(raw)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return false, fmt.Errorf("cannot save %s", filepath.Base(path))
	}
	if err := setOwner(path, uid, gid); err != nil {
		return true, err
	}
	return true, nil
}

func setOwner(path string, uid, gid int) error {
	if uid < 0 && gid < 0 {
		return nil
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("cannot assign installation file ownership")
	}
	return nil
}

func runDoctor(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("dir", ".", "installation directory")
	file := fs.String("file", "", "optional monitor configuration to validate")
	connections := fs.Bool("connections", false, "explicitly ping configured business databases")
	address := fs.String("url", "", "optional ready endpoint base URL")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("invalid doctor arguments")
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("doctor does not accept positional arguments")
	}
	if *connections && *file == "" {
		return fmt.Errorf("doctor --connections requires --file")
	}
	if *file != "" {
		if err := doctorConfig(*file, *connections, out); err != nil {
			return err
		}
	} else {
		for _, path := range []string{".env", "deploy/phase2/config/monitor.json", "deploy/phase2/config/credentials.json", "deploy/phase2/config/master.key"} {
			info, err := os.Stat(filepath.Join(*dir, filepath.FromSlash(path)))
			if err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("missing or unreadable %s; run init first", path)
			}
			fmt.Fprintln(out, "OK", path)
		}
	}
	if *address != "" {
		client := &http.Client{Timeout: 5 * time.Second}
		res, err := client.Get(strings.TrimRight(*address, "/") + "/health/ready")
		if err != nil {
			return fmt.Errorf("service readiness request failed; check containers and port")
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("service is not ready (HTTP %d)", res.StatusCode)
		}
		fmt.Fprintln(out, "OK service readiness")
	}
	return nil
}

func runConfig(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "validate" {
		return fmt.Errorf("usage: config validate --file PATH")
	}
	fs := flag.NewFlagSet("config validate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	file := fs.String("file", "", "monitor YAML or JSON configuration")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 || *file == "" {
		return fmt.Errorf("usage: config validate --file PATH")
	}
	d, err := checks.Load(*file)
	if err != nil {
		return fmt.Errorf("configuration validation failed: %w", err)
	}
	_, err = fmt.Fprintf(out, "OK configuration version %d; %d sources, %d checks, %d channels. No events, notifications or model requests were created.\n", d.Version, len(d.Sources), len(d.Checks), len(d.Channels))
	return err
}

func doctorConfig(path string, connections bool, out io.Writer) error {
	d, err := checks.Load(path)
	if err != nil {
		return fmt.Errorf("configuration validation failed: %w", err)
	}
	fmt.Fprintln(out, "OK configuration structure, queries and rules")
	for _, source := range d.Sources {
		if strings.HasPrefix(source.DSN.File, "stored:") {
			if connections {
				return fmt.Errorf("source %s uses an encrypted stored secret; run its connection test through the running administrator API", source.ID)
			}
			fmt.Fprintln(out, "SKIP source", source.ID, "encrypted secret requires running service")
			continue
		}
		if _, err := checks.ResolveSecret(source.DSN); err != nil {
			return fmt.Errorf("source %s secret reference unavailable", source.ID)
		}
		fmt.Fprintln(out, "OK source", source.ID, "secret reference")
		if connections {
			runner, err := checks.NewRunner(source, checks.ResolveSecret)
			if err != nil {
				return fmt.Errorf("source %s connection configuration unavailable", source.ID)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err = runner.Ping(ctx)
			cancel()
			_ = runner.Close()
			if err != nil {
				return fmt.Errorf("source %s connection unavailable", source.ID)
			}
			fmt.Fprintln(out, "OK source", source.ID, "connection")
		}
	}
	for _, channel := range d.Channels {
		if channel.TokenSecret.Env == "" && channel.TokenSecret.File == "" {
			continue
		}
		if strings.HasPrefix(channel.TokenSecret.File, "stored:") {
			fmt.Fprintln(out, "SKIP channel", channel.ID, "encrypted secret requires running service")
			continue
		}
		if _, err := checks.ResolveSecret(channel.TokenSecret); err != nil {
			return fmt.Errorf("channel %s secret reference unavailable", channel.ID)
		}
		fmt.Fprintln(out, "OK channel", channel.ID, "secret reference; no notification sent")
	}
	return nil
}
