package monitor

import (
	"bidmon/internal/checks"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	ChecksFile                                                                                        string
	Document                                                                                          *checks.Document
	Runtime                                                                                           *GeneralRuntime
	Model                                                                                             *ModelConfig
	Database, MySQL, Token, Sink, Source, Environment, Addr, SSHHost, KeyFile, KnownHosts, BindingKey string
	Poll, Queue, Stale, Grace, DiagTimeout                                                            time.Duration
	Page, Attempts, DiagConcurrency                                                                   int
	Diagnose                                                                                          bool
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func seconds(k string, d int) time.Duration {
	v, e := strconv.Atoi(env(k, strconv.Itoa(d)))
	if e != nil || v < 0 {
		panic("invalid duration " + k)
	}
	return time.Duration(v) * time.Second
}
func LoadConfig() (Config, error) {
	c := Config{Database: os.Getenv("GO_DATABASE_URL"), MySQL: os.Getenv("GO_SOURCE_DSN"), Token: os.Getenv("MON_ADMIN_TOKEN"), Sink: env("MON_SINK_URL", "http://notification-sink:8000/notifications"), Source: env("MON_SOURCE_NAME", "presales-lab"), Environment: env("MON_ENVIRONMENT", "lab"), Addr: env("GO_ADDR", ":8000"), SSHHost: env("GO_SSH_ADDRESS", "ssh-target:22"), KeyFile: "/ssh-client/id_ed25519", KnownHosts: "/ssh-client/known_hosts", BindingKey: "/ssh-client/binding.key", Poll: seconds("MON_POLL_SECONDS", 15), Queue: seconds("MON_QUEUE_SECONDS", 180), Stale: seconds("MON_STALE_SECONDS", 900), Grace: seconds("MON_RESULT_GRACE_SECONDS", 30), DiagTimeout: seconds("MON_DIAGNOSIS_TIMEOUT_SECONDS", 120), Page: 200, Attempts: 6, DiagConcurrency: 2, Diagnose: env("MON_DIAGNOSIS_ENABLED", "true") == "true"}
	c.ChecksFile = os.Getenv("GO_MONITOR_CONFIG")
	if c.ChecksFile != "" {
		raw, err := os.ReadFile(c.ChecksFile)
		if err != nil {
			return c, fmt.Errorf("monitor_config_unavailable")
		}
		c.Document, err = checks.Parse(raw)
		if err != nil {
			return c, err
		}
	}
	if c.Database == "" || (c.MySQL == "" && c.Document == nil) || len(c.Token) < 24 || c.Poll < time.Second || c.DiagTimeout < time.Second || c.DiagTimeout > 300*time.Second {
		return c, fmt.Errorf("invalid configuration")
	}
	if path := os.Getenv("MON_MODEL_CONFIG_FILE"); path != "" {
		model, err := ReadModelConfig(path)
		if err != nil {
			return c, err
		}
		c.Model = model
	}
	return c, nil
}

func (c Config) PresalesEnabled() bool {
	return c.MySQL != "" && (c.Document == nil || c.Document.MonitorPresales)
}
