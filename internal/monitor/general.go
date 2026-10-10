package monitor

import (
	"bidmon/internal/checks"
	"bidmon/internal/evidence"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

type ConfigVersion struct {
	Version   string    `gorm:"primaryKey" json:"version"`
	Document  M         `gorm:"type:json" json:"document"`
	CreatedAt time.Time `json:"created_at"`
	Actor     string    `json:"actor"`
	Active    bool      `json:"active"`
}

func (ConfigVersion) TableName() string { return "monitor_config_versions" }

type CheckHealth struct {
	ID          string     `json:"id"`
	LastSuccess *time.Time `json:"last_success_at"`
	LastFailure *time.Time `json:"last_failure_at"`
	Error       string     `json:"error,omitempty"`
	DurationMS  int64      `json:"duration_ms"`
	Rows        int        `json:"rows"`
	Enabled     bool       `json:"enabled"`
}
type checkPlan struct {
	Spec            checks.CheckSpec
	Rules           []checks.CompiledRule
	Next            time.Time
	SemanticVersion string
}
type GeneralRuntime struct {
	DB       *gorm.DB
	Cfg      Config
	scanMu   sync.Mutex
	mu       sync.RWMutex
	doc      *checks.Document
	version  string
	plans    []*checkPlan
	runners  map[string]*checks.Runner
	health   map[string]CheckHealth
	scanTurn int
}

func NewGeneralRuntime(ctx context.Context, db *gorm.DB, cfg Config) (*GeneralRuntime, error) {
	r := &GeneralRuntime{DB: db, Cfg: cfg, runners: map[string]*checks.Runner{}, health: map[string]CheckHealth{}}
	doc := cfg.Document
	var active ConfigVersion
	err := db.WithContext(ctx).Where("active", true).First(&active).Error
	if err == nil {
		raw, _ := json.Marshal(active.Document)
		doc, err = checks.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("stored configuration invalid")
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if doc == nil {
		doc = &checks.Document{Version: 1}
	}
	if err = r.Install(ctx, doc, "bootstrap"); err != nil {
		return nil, err
	}
	return r, nil
}

func documentMap(doc *checks.Document) M {
	raw, _ := json.Marshal(doc)
	var m M
	_ = json.Unmarshal(raw, &m)
	return m
}
func (r *GeneralRuntime) Install(ctx context.Context, doc *checks.Document, actor string) error {
	if err := checks.Validate(doc); err != nil {
		return err
	}
	plans := []*checkPlan{}
	for _, spec := range doc.Checks {
		compiled, err := checks.CompileRules(spec)
		if err != nil {
			return err
		}
		var source checks.SourceSpec
		for _, candidate := range doc.Sources {
			if candidate.ID == spec.Source {
				source = candidate
				break
			}
		}
		semantic, _ := json.Marshal([]any{spec, source})
		hash := sha256.Sum256(semantic)
		plans = append(plans, &checkPlan{Spec: spec, Rules: compiled, SemanticVersion: hex.EncodeToString(hash[:])})
	}
	raw, _ := json.Marshal(doc)
	sum := sha256.Sum256(raw)
	version := hex.EncodeToString(sum[:])
	r.scanMu.Lock()
	defer r.scanMu.Unlock()
	if err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&ConfigVersion{}).Where("active", true).Update("active", false).Error; err != nil {
			return err
		}
		var old ConfigVersion
		err := tx.First(&old, "version=?", version).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(&ConfigVersion{Version: version, Document: documentMap(doc), CreatedAt: time.Now().UTC(), Actor: actor, Active: true}).Error
		}
		if err != nil {
			return err
		}
		return tx.Model(&old).Update("active", true).Error
	}); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, runner := range r.runners {
		runner.Close()
	}
	r.runners = map[string]*checks.Runner{}
	r.plans = plans
	r.doc = doc
	r.version = version
	for _, plan := range plans {
		r.health[plan.Spec.ID] = CheckHealth{ID: plan.Spec.ID, Enabled: plan.Spec.IsEnabled()}
	}
	return nil
}
func (r *GeneralRuntime) Current() (*checks.Document, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	raw, _ := json.Marshal(r.doc)
	var doc checks.Document
	_ = json.Unmarshal(raw, &doc)
	return &doc, r.version
}
func (r *GeneralRuntime) Health() []CheckHealth {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rows := []CheckHealth{}
	for _, p := range r.plans {
		rows = append(rows, r.health[p.Spec.ID])
	}
	return rows
}
func (r *GeneralRuntime) Close() {
	r.scanMu.Lock()
	defer r.scanMu.Unlock()
	for _, runner := range r.runners {
		runner.Close()
	}
}

func (r *GeneralRuntime) Scan(ctx context.Context) error {
	r.scanMu.Lock()
	defer r.scanMu.Unlock()
	now := time.Now().UTC()
	work := []*checkPlan{}
	for _, p := range r.plans {
		if p.Spec.IsEnabled() && !now.Before(p.Next) {
			work = append(work, p)
		}
	}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	work = interleaveChecks(work, r.scanTurn)
	r.scanTurn++
	for _, p := range work {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		}
		wg.Add(1)
		go func(p *checkPlan) { defer wg.Done(); defer func() { <-sem }(); r.runCheck(ctx, p, now) }(p)
	}
	wg.Wait()
	return nil
}

// Round robin prevents a large set of slow checks from occupying every slot
// before another source gets its first check. The first source rotates per scan.
func interleaveChecks(work []*checkPlan, turn int) []*checkPlan {
	groups := map[string][]*checkPlan{}
	order := []string{}
	for _, p := range work {
		if _, ok := groups[p.Spec.Source]; !ok {
			order = append(order, p.Spec.Source)
		}
		groups[p.Spec.Source] = append(groups[p.Spec.Source], p)
	}
	if len(order) == 0 {
		return work
	}
	start := turn % len(order)
	result := make([]*checkPlan, 0, len(work))
	for len(result) < len(work) {
		for offset := range order {
			source := order[(start+offset)%len(order)]
			if rows := groups[source]; len(rows) > 0 {
				result = append(result, rows[0])
				groups[source] = rows[1:]
			}
		}
	}
	return result
}
func (r *GeneralRuntime) source(id string) (*checks.Runner, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if runner := r.runners[id]; runner != nil {
		return runner, nil
	}
	for _, spec := range r.doc.Sources {
		if spec.ID == id {
			runner, err := checks.NewRunner(spec, r.resolveSecret)
			if err != nil {
				return nil, err
			}
			r.runners[id] = runner
			return runner, nil
		}
	}
	return nil, errors.New("source_not_found")
}
func (r *GeneralRuntime) runCheck(ctx context.Context, p *checkPlan, now time.Time) {
	begin := time.Now()
	interval, _ := time.ParseDuration(p.Spec.Interval)
	if interval < time.Second {
		interval = 15 * time.Second
	}
	p.Next = now.Add(interval)
	health := CheckHealth{ID: p.Spec.ID, Enabled: true}
	r.mu.RLock()
	old := r.health[p.Spec.ID]
	r.mu.RUnlock()
	health.LastSuccess = old.LastSuccess
	health.LastFailure = old.LastFailure
	runner, err := r.source(p.Spec.Source)
	var observations []checks.Observation
	if err == nil {
		observations, err = runner.Query(ctx, p.Spec, checks.Window{Start: now.Add(-24 * time.Hour), End: now})
	}
	if err == nil {
		for _, obs := range observations {
			if err = r.observe(ctx, p, obs, now); err != nil {
				break
			}
		}
		if err == nil {
			keys := []string{}
			for _, o := range observations {
				keys = append(keys, identity("check:"+p.Spec.ID, p.Spec.Environment, o.EntityKey))
			}
			query := r.DB.WithContext(ctx).Model(&Snapshot{}).Where("source=? AND environment=?", "check:"+p.Spec.ID, p.Spec.Environment)
			if len(keys) > 0 {
				query = query.Where("key NOT IN ?", keys)
			}
			err = query.Update("rule_states", M{}).Error
		}
	}
	if err != nil {
		health.LastFailure = &now
		health.Error = "check_failed: " + safeCheckError(err)
		// Break consecutive confirmation across missing/failed scans, preserving incidents.
		_ = r.DB.WithContext(ctx).Model(&Snapshot{}).Where("source=? AND environment=?", "check:"+p.Spec.ID, p.Spec.Environment).Update("rule_states", M{}).Error
	} else {
		health.LastSuccess = &now
		health.Rows = len(observations)
	}
	health.DurationMS = time.Since(begin).Milliseconds()
	r.mu.Lock()
	r.health[p.Spec.ID] = health
	r.mu.Unlock()
}
func safeCheckError(err error) string {
	// Driver errors may contain SQL, hostnames or credentials. Keep runtime logs generic.
	return "query, mapping or persistence unavailable"
}
func (r *GeneralRuntime) definition(p *checkPlan, rule checks.RuleSpec) M {
	r.mu.RLock()
	defer r.mu.RUnlock()
	channels := []any{}
	for _, id := range p.Spec.NotificationRoutes {
		for _, ch := range r.doc.Channels {
			if ch.ID == id {
				raw, _ := json.Marshal(ch)
				var m map[string]any
				_ = json.Unmarshal(raw, &m)
				channels = append(channels, m)
			}
		}
	}
	raw, _ := json.Marshal(p.Spec)
	var spec M
	_ = json.Unmarshal(raw, &spec)
	d := M{"check_id": p.Spec.ID, "title": rule.Summary, "severity": rule.Severity, "rule_version": rule.Version, "config_version": r.version, "check": spec, "channels": channels, "evidence_config_file": r.doc.EvidenceConfigFile}
	for _, source := range r.doc.Sources {
		if source.ID == p.Spec.Source {
			raw, _ := json.Marshal(source)
			var frozen M
			_ = json.Unmarshal(raw, &frozen)
			d["source"] = frozen
			break
		}
	}
	if p.Spec.Runbook != "" {
		if strings.HasPrefix(p.Spec.Runbook, "file:") {
			if file, err := os.Open(strings.TrimPrefix(p.Spec.Runbook, "file:")); err == nil {
				defer file.Close()
				raw, err := io.ReadAll(io.LimitReader(file, 16385))
				if err == nil && len(raw) <= 16384 {
					d["runbook_text"] = Redact(string(raw))
				}
			}
		} else {
			d["runbook_text"] = Redact(p.Spec.Runbook)
		}
	}
	return d
}
func (r *GeneralRuntime) observe(ctx context.Context, p *checkPlan, obs checks.Observation, now time.Time) error {
	return r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		key := identity("check:"+p.Spec.ID, p.Spec.Environment, obs.EntityKey)
		var s Snapshot
		err := tx.First(&s, "key=?", key).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s = Snapshot{Key: key, Source: "check:" + p.Spec.ID, Environment: p.Spec.Environment, TaskID: obs.EntityKey, FirstSeenAt: now, RuleStates: M{}}
		}
		previous := previousFields(p.Spec, obj(s.Evidence, "fields"))
		s.Status = "observed"
		s.ObservedAt = now
		s.NextCheckAt = now.Add(24 * time.Hour)
		s.Evidence = M{"check_id": p.Spec.ID, "entity_key": obs.EntityKey, "fields": evidenceFields(obs.Fields), "quality": string(obs.Quality), "observed_at": stamp(obs.ObservedAt), "source_available": true}
		if s.RuleStates == nil {
			s.RuleStates = M{}
		}
		for i, rule := range p.Spec.Rules {
			result := p.Rules[i].Evaluate(obs, map[string]any(previous), now)
			old := obj(s.RuleStates, rule.ID)
			if str(old, "semantic_version") != p.SemanticVersion {
				old = M{}
			}
			state := checks.RuleState{Bad: int(num(old, "bad")), Good: int(num(old, "good"))}.Advance(result, rule.Confirmations, rule.RecoveryConfirmations)
			counts := M{"bad": state.Bad, "good": state.Good, "version": rule.Version, "trigger": rule.Trigger, "recover": rule.Recover, "semantic_version": p.SemanticVersion, "decision": string(result.Decision), "reason": result.Reason}
			s.RuleStates[rule.ID] = counts
			var signal *bool
			if result.Decision == checks.Trigger {
				signal = bit(true)
			} else if result.Decision == checks.Clear {
				signal = bit(false)
			}
			if err = applyConfigured(tx, s, rule.ID, signal, counts, rule.Confirmations, rule.RecoveryConfirmations, now, r.definition(p, rule)); err != nil {
				return err
			}
		}
		return tx.Save(&s).Error
	})
}
func evidenceFields(fields map[string]any) M {
	// Evidence is scrubbed before persistence as well as before remote model calls.
	return M(evidence.Sanitize(fields).(map[string]any))
}

// JSON timestamps and numbers are restored to the declared field types so CEL
// previous uses the same contract as the current observation after a restart.
func previousFields(spec checks.CheckSpec, saved M) M {
	if len(saved) == 0 {
		return M{}
	}
	row := map[string]any{}
	for name, field := range spec.Fields {
		value := saved[name]
		if n, ok := value.(json.Number); ok {
			value = n.String()
		}
		if field.Type == "json" && value != nil {
			raw, _ := json.Marshal(value)
			value = string(raw)
		}
		row[field.Column] = value
	}
	obs, err := checks.MapRow(spec, row, time.Now().UTC())
	if err != nil || obs.Quality != checks.QualityValid {
		return M{}
	}
	return M(obs.Fields)
}

func (r *GeneralRuntime) Preview(ctx context.Context, doc *checks.Document, id string) ([]checks.Observation, error) {
	if err := checks.Validate(doc); err != nil {
		return nil, err
	}
	for _, spec := range doc.Checks {
		if spec.ID == id {
			for _, source := range doc.Sources {
				if source.ID == spec.Source {
					runner, err := checks.NewRunner(source, r.resolveSecret)
					if err != nil {
						return nil, errors.New("source_unavailable")
					}
					defer runner.Close()
					rows, err := runner.Query(ctx, spec, checks.Window{Start: time.Now().UTC().Add(-24 * time.Hour), End: time.Now().UTC()})
					if err != nil {
						return nil, errors.New("query_or_mapping_failed")
					}
					return rows, nil
				}
			}
		}
	}
	return nil, errors.New("check_not_found")
}
