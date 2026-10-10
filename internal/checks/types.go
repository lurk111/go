// Package checks defines business-neutral SQL checks and deterministic rules.
package checks

import "time"

type SecretRef struct {
	Env  string `json:"env,omitempty" yaml:"env,omitempty"`
	File string `json:"file,omitempty" yaml:"file,omitempty"`
}

type Budget struct {
	Timeout  string `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	MaxRows  int    `json:"max_rows,omitempty" yaml:"max_rows,omitempty"`
	MaxBytes int    `json:"max_bytes,omitempty" yaml:"max_bytes,omitempty"`
}

type SourceSpec struct {
	ID     string    `json:"id" yaml:"id"`
	Driver string    `json:"driver" yaml:"driver"`
	DSN    SecretRef `json:"dsn" yaml:"dsn"`
	Budget Budget    `json:"budget,omitempty" yaml:"budget,omitempty"`
}

type FieldSpec struct {
	Column   string `json:"column" yaml:"column"`
	Type     string `json:"type" yaml:"type"`
	Required bool   `json:"required,omitempty" yaml:"required,omitempty"`
}

type RuleSpec struct {
	ID                    string `json:"id" yaml:"id"`
	Version               string `json:"version,omitempty" yaml:"version,omitempty"`
	Trigger               string `json:"trigger" yaml:"trigger"`
	Recover               string `json:"recover" yaml:"recover"`
	Confirmations         int    `json:"confirmations,omitempty" yaml:"confirmations,omitempty"`
	RecoveryConfirmations int    `json:"recovery_confirmations,omitempty" yaml:"recovery_confirmations,omitempty"`
	Severity              string `json:"severity,omitempty" yaml:"severity,omitempty"`
	Summary               string `json:"summary,omitempty" yaml:"summary,omitempty"`
}

type CheckSpec struct {
	ID                 string               `json:"id" yaml:"id"`
	Source             string               `json:"source" yaml:"source"`
	Mode               string               `json:"mode" yaml:"mode"`
	Environment        string               `json:"environment,omitempty" yaml:"environment,omitempty"`
	Enabled            *bool                `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Interval           string               `json:"interval,omitempty" yaml:"interval,omitempty"`
	Query              string               `json:"query" yaml:"query"`
	DetailQuery        string               `json:"detail_query,omitempty" yaml:"detail_query,omitempty"`
	Identity           []string             `json:"identity,omitempty" yaml:"identity,omitempty"`
	Fields             map[string]FieldSpec `json:"fields" yaml:"fields"`
	Rules              []RuleSpec           `json:"rules" yaml:"rules"`
	DiagnosisMode      string               `json:"diagnosis_mode,omitempty" yaml:"diagnosis_mode,omitempty"`
	AllowModel         bool                 `json:"allow_model,omitempty" yaml:"allow_model,omitempty"`
	DiagnosticRules    []DiagnosticRuleSpec `json:"diagnostic_rules,omitempty" yaml:"diagnostic_rules,omitempty"`
	EvidenceProfile    string               `json:"evidence_profile,omitempty" yaml:"evidence_profile,omitempty"`
	NotificationRoutes []string             `json:"notification_routes,omitempty" yaml:"notification_routes,omitempty"`
	Runbook            string               `json:"runbook,omitempty" yaml:"runbook,omitempty"`
}

type DiagnosticRuleSpec struct {
	ID       string   `json:"id" yaml:"id"`
	Pattern  string   `json:"pattern" yaml:"pattern"`
	Category string   `json:"category" yaml:"category"`
	Summary  string   `json:"summary" yaml:"summary"`
	Actions  []string `json:"actions" yaml:"actions"`
}

func (c CheckSpec) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

type ChannelSpec struct {
	ID          string    `json:"id" yaml:"id"`
	Type        string    `json:"type" yaml:"type"`
	URL         string    `json:"url" yaml:"url"`
	TokenSecret SecretRef `json:"token_secret,omitempty" yaml:"token_secret,omitempty"`
	Confirm     string    `json:"confirm,omitempty" yaml:"confirm,omitempty"`
}

type Document struct {
	Version            int           `json:"version" yaml:"version"`
	Sources            []SourceSpec  `json:"sources" yaml:"sources"`
	Checks             []CheckSpec   `json:"checks" yaml:"checks"`
	Channels           []ChannelSpec `json:"channels,omitempty" yaml:"channels,omitempty"`
	EvidenceConfigFile string        `json:"evidence_config_file,omitempty" yaml:"evidence_config_file,omitempty"`
	MonitorPresales    bool          `json:"monitor_presales,omitempty" yaml:"monitor_presales,omitempty"`
	Retention          RetentionSpec `json:"retention,omitempty" yaml:"retention,omitempty"`
}

// Retention is preview only. Zero excludes the category from the preview.
type RetentionSpec struct {
	ClosedEvidenceDays int `json:"closed_evidence_days,omitempty" yaml:"closed_evidence_days,omitempty"`
	SnapshotDays       int `json:"snapshot_days,omitempty" yaml:"snapshot_days,omitempty"`
	DeliveredDays      int `json:"delivered_days,omitempty" yaml:"delivered_days,omitempty"`
}

type Quality string

const (
	QualityValid   Quality = "valid"
	QualityInvalid Quality = "invalid"
)

type Observation struct {
	CheckID    string         `json:"check_id"`
	EntityKey  string         `json:"entity_key"`
	Fields     map[string]any `json:"fields"`
	ObservedAt time.Time      `json:"observed_at"`
	Quality    Quality        `json:"quality"`
	Errors     []string       `json:"errors,omitempty"`
}

type Window struct {
	Start time.Time
	End   time.Time
	After string
}

type Decision string

const (
	Trigger Decision = "trigger"
	Clear   Decision = "clear"
	Unknown Decision = "unknown"
)

type RuleResult struct {
	RuleID   string   `json:"rule_id"`
	Version  string   `json:"version"`
	Decision Decision `json:"decision"`
	Reason   string   `json:"reason,omitempty"`
}

// RuleState tracks observations, not elapsed scheduler ticks. An unavailable or
// invalid observation breaks both sequences and never confirms recovery.
type RuleState struct {
	Bad  int `json:"bad"`
	Good int `json:"good"`
}

func (s RuleState) Advance(r RuleResult, triggerConfirm, recoveryConfirm int) RuleState {
	triggerConfirm = max(triggerConfirm, 1)
	recoveryConfirm = max(recoveryConfirm, 1)
	switch r.Decision {
	case Trigger:
		return RuleState{Bad: min(s.Bad+1, triggerConfirm)}
	case Clear:
		return RuleState{Good: min(s.Good+1, recoveryConfirm)}
	default:
		return RuleState{}
	}
}
