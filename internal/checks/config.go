package checks

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

var identifier = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
var fieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Parse accepts one YAML or JSON document. Unknown fields and duplicate mapping
// keys are rejected by the YAML decoder, including for JSON input.
func Parse(raw []byte) (*Document, error) {
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("configuration exceeds 1 MiB")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var d Document
	if err := decoder.Decode(&d); err != nil {
		return nil, fmt.Errorf("configuration: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("configuration must contain one document")
	}
	if err := Validate(&d); err != nil {
		return nil, err
	}
	return &d, nil
}

func Load(path string) (*Document, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

func validateSecret(s SecretRef, optional bool) error {
	if s.Env == "" && s.File == "" {
		if optional {
			return nil
		}
		return fmt.Errorf("secret requires env or file reference")
	}
	if (s.Env != "") == (s.File != "") {
		return fmt.Errorf("secret must use exactly one of env or file")
	}
	if s.Env != "" && !envName.MatchString(s.Env) {
		return fmt.Errorf("invalid secret environment name")
	}
	if strings.ContainsRune(s.File, 0) || len(s.File) > 1024 {
		return fmt.Errorf("invalid secret file path")
	}
	return nil
}

func Validate(d *Document) error {
	if d == nil || d.Version != 1 {
		return fmt.Errorf("unsupported configuration version; expected 1")
	}
	if len(d.Sources) > 100 || len(d.Checks) > 500 || len(d.Channels) > 100 {
		return fmt.Errorf("too many configuration entries")
	}
	for _, days := range []int{d.Retention.ClosedEvidenceDays, d.Retention.SnapshotDays, d.Retention.DeliveredDays} {
		if days < 0 || days > 3650 {
			return fmt.Errorf("retention preview days must be 0..3650; 0 excludes the category")
		}
	}
	sources := map[string]bool{}
	for i := range d.Sources {
		s := &d.Sources[i]
		if !identifier.MatchString(s.ID) || sources[s.ID] {
			return fmt.Errorf("invalid or duplicate source id %q", s.ID)
		}
		sources[s.ID] = true
		if s.Driver != "mysql" && s.Driver != "postgres" {
			return fmt.Errorf("source %s: driver must be mysql or postgres", s.ID)
		}
		if err := validateSecret(s.DSN, false); err != nil {
			return fmt.Errorf("source %s: %w", s.ID, err)
		}
		if s.Budget.Timeout == "" {
			s.Budget.Timeout = "5s"
		}
		t, err := time.ParseDuration(s.Budget.Timeout)
		if err != nil || t < time.Millisecond || t > 60*time.Second {
			return fmt.Errorf("source %s: timeout must be 1ms..60s", s.ID)
		}
		if s.Budget.MaxRows == 0 {
			s.Budget.MaxRows = 1000
		}
		if s.Budget.MaxBytes == 0 {
			s.Budget.MaxBytes = 1 << 20
		}
		if s.Budget.MaxRows < 1 || s.Budget.MaxRows > 10000 || s.Budget.MaxBytes < 1 || s.Budget.MaxBytes > 8<<20 {
			return fmt.Errorf("source %s: invalid row or byte budget", s.ID)
		}
	}
	channels := map[string]bool{}
	for i := range d.Channels {
		c := &d.Channels[i]
		if !identifier.MatchString(c.ID) || channels[c.ID] {
			return fmt.Errorf("invalid or duplicate channel id %q", c.ID)
		}
		channels[c.ID] = true
		if c.Type != "local_sink" && c.Type != "webhook" {
			return fmt.Errorf("channel %s: unsupported type", c.ID)
		}
		u, err := url.Parse(c.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
			return fmt.Errorf("channel %s: invalid HTTP URL", c.ID)
		}
		if err := validateSecret(c.TokenSecret, true); err != nil {
			return fmt.Errorf("channel %s: %w", c.ID, err)
		}
		if c.Confirm == "" {
			c.Confirm = "http_status"
		}
		if c.Confirm != "http_status" && c.Confirm != "code_zero" {
			return fmt.Errorf("channel %s: confirmation must be http_status or code_zero", c.ID)
		}
	}
	checks := map[string]bool{}
	for i := range d.Checks {
		c := &d.Checks[i]
		if !identifier.MatchString(c.ID) || checks[c.ID] {
			return fmt.Errorf("invalid or duplicate check id %q", c.ID)
		}
		checks[c.ID] = true
		if !sources[c.Source] {
			return fmt.Errorf("check %s: unknown source %q", c.ID, c.Source)
		}
		if c.Mode != "entity" && c.Mode != "aggregate" {
			return fmt.Errorf("check %s: mode must be entity or aggregate", c.ID)
		}
		if c.Environment == "" {
			c.Environment = "default"
		}
		if !identifier.MatchString(c.Environment) {
			return fmt.Errorf("check %s: invalid environment", c.ID)
		}
		if c.Interval == "" {
			c.Interval = "15s"
		}
		t, err := time.ParseDuration(c.Interval)
		if err != nil || t < time.Second || t > 24*time.Hour {
			return fmt.Errorf("check %s: interval must be 1s..24h", c.ID)
		}
		if err := ValidateSQL(c.Query); err != nil {
			return fmt.Errorf("check %s: %w", c.ID, err)
		}
		if c.DetailQuery != "" {
			if err := ValidateSQL(c.DetailQuery); err != nil {
				return fmt.Errorf("check %s detail: %w", c.ID, err)
			}
		}
		if len(c.Fields) == 0 || len(c.Fields) > 100 {
			return fmt.Errorf("check %s: expected 1..100 field mappings", c.ID)
		}
		for name, f := range c.Fields {
			if !fieldName.MatchString(name) || !fieldName.MatchString(f.Column) {
				return fmt.Errorf("check %s: invalid field mapping %s", c.ID, name)
			}
			switch f.Type {
			case "string", "int", "double", "bool", "timestamp", "json":
			default:
				return fmt.Errorf("check %s: invalid type for %s", c.ID, name)
			}
		}
		if c.Mode == "entity" && len(c.Identity) == 0 {
			return fmt.Errorf("check %s: entity mode requires identity", c.ID)
		}
		seen := map[string]bool{}
		for _, k := range c.Identity {
			f, ok := c.Fields[k]
			if !ok || seen[k] {
				return fmt.Errorf("check %s: invalid or duplicate identity field %s", c.ID, k)
			}
			if f.Type == "json" || f.Type == "double" {
				return fmt.Errorf("check %s: identity requires stable scalar fields", c.ID)
			}
			seen[k] = true
			f.Required = true
			c.Fields[k] = f
		}
		if len(c.Rules) == 0 || len(c.Rules) > 50 {
			return fmt.Errorf("check %s: expected 1..50 rules", c.ID)
		}
		seen = map[string]bool{}
		for j := range c.Rules {
			r := &c.Rules[j]
			if !identifier.MatchString(r.ID) || seen[r.ID] {
				return fmt.Errorf("check %s: invalid or duplicate rule id %q", c.ID, r.ID)
			}
			seen[r.ID] = true
			if r.Version == "" {
				r.Version = "v1"
			}
			if len(r.Version) > 16 {
				return fmt.Errorf("check %s: rule version too long", c.ID)
			}
			if r.Confirmations == 0 {
				r.Confirmations = 2
			}
			if r.RecoveryConfirmations == 0 {
				r.RecoveryConfirmations = 2
			}
			if r.Confirmations < 1 || r.Confirmations > 10000 || r.RecoveryConfirmations < 1 || r.RecoveryConfirmations > 10000 {
				return fmt.Errorf("check %s: rule confirmation counts must be 1..10000", c.ID)
			}
			if r.Severity == "" {
				r.Severity = "warning"
			}
			if r.Severity != "info" && r.Severity != "warning" && r.Severity != "critical" {
				return fmt.Errorf("check %s: unsupported severity", c.ID)
			}
			if len(r.Trigger) == 0 || len(r.Trigger) > 8192 || len(r.Recover) == 0 || len(r.Recover) > 8192 {
				return fmt.Errorf("check %s: trigger and recover expressions required, max 8192 bytes", c.ID)
			}
		}
		if _, err := CompileRules(*c); err != nil {
			return fmt.Errorf("check %s: %w", c.ID, err)
		}
		if c.DiagnosisMode == "" {
			c.DiagnosisMode = "rule_only"
		}
		if c.DiagnosisMode != "rule_only" && c.DiagnosisMode != "hybrid" {
			return fmt.Errorf("check %s: invalid diagnosis_mode", c.ID)
		}
		if len(c.DiagnosticRules) > 20 {
			return fmt.Errorf("check %s: at most 20 diagnostic rules", c.ID)
		}
		seen = map[string]bool{}
		for _, r := range c.DiagnosticRules {
			if !identifier.MatchString(r.ID) || seen[r.ID] || len(r.Pattern) == 0 || len(r.Pattern) > 500 || r.Category == "" || len(r.Category) > 128 || utf8.RuneCountInString(r.Summary) > 1000 || len(r.Actions) > 10 {
				return fmt.Errorf("check %s: invalid diagnostic rule", c.ID)
			}
			seen[r.ID] = true
			for _, action := range r.Actions {
				if strings.TrimSpace(action) == "" || utf8.RuneCountInString(action) > 1000 {
					return fmt.Errorf("check %s: invalid diagnostic action", c.ID)
				}
			}
			if _, err := regexp.Compile(r.Pattern); err != nil {
				return fmt.Errorf("check %s: invalid diagnostic regex", c.ID)
			}
		}
		seen = map[string]bool{}
		for _, r := range c.NotificationRoutes {
			if !channels[r] || seen[r] {
				return fmt.Errorf("check %s: unknown or duplicate route %q", c.ID, r)
			}
			seen[r] = true
		}
		if len(c.Runbook) > 4000 {
			return fmt.Errorf("check %s: runbook too long", c.ID)
		}
	}
	return nil
}
