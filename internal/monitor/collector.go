package monitor

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"time"
)

type Collector struct {
	Cfg    Config
	DB     *gorm.DB
	Source *Source
}

func (c *Collector) key(id string) string { return identity(c.Cfg.Source, c.Cfg.Environment, id) }
func (c *Collector) cursor() string       { return c.Cfg.Source + "/" + c.Cfg.Environment }
func (c *Collector) Observe(ctx context.Context, t Task, now time.Time) error {
	summary := M{"state": "not_checked"}
	var err error
	if t.Status == "completed" {
		summary, err = c.Source.Result(ctx, t)
		if err != nil {
			return err
		}
	}
	return c.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var s Snapshot
		err := tx.First(&s, "key=?", c.key(t.ID)).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		fresh := errors.Is(err, gorm.ErrRecordNotFound)
		if !fresh && now.Sub(s.ObservedAt) < c.Cfg.Poll*8/10 {
			return nil
		}
		prev := s.Evidence
		if fresh {
			s = Snapshot{Key: c.key(t.ID), Source: c.Cfg.Source, Environment: c.Cfg.Environment, TaskID: t.ID, FirstSeenAt: now, Evidence: M{}, RuleStates: M{}}
		}
		var cur Cursor
		err = tx.First(&cur, "key=?", c.cursor()).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if cur.LastFailureAt != nil && !cur.LastFailureAt.Before(s.ObservedAt) {
			for k := range s.RuleStates {
				s.RuleStates[k] = advance(nil, nil)
			}
		}
		signals := Evaluate(t, summary, now, c.Cfg, prev)
		pending := parsed(prev, "pending_since")
		if t.Status == "pending" && (pending.IsZero() || str(prev, "status") != "pending") {
			pending = t.Created
			if len(prev) > 0 {
				pending = now
			}
		}
		progress := str(prev, "progress_observed_at")
		if len(prev) > 0 && t.Stage != str(prev, "stage") {
			progress = stamp(now)
		}
		s.Status = t.Status
		s.Stage = t.Stage
		s.SourceUpdatedAt = &t.Updated
		s.ObservedAt = now
		s.NextCheckAt = now.Add(c.Cfg.Poll)
		s.Evidence = M{"status": t.Status, "stage": t.Stage, "result": summary, "source_available": true, "source_updated_at": stamp(t.Updated), "progress_observed_at": progress}
		if t.Status == "pending" {
			s.Evidence["pending_since"] = stamp(pending)
		}
		for _, rule := range ruleNames {
			signal := signals[rule]
			counts := advance(obj(s.RuleStates, rule), signal)
			needed := 2
			if rule == "TASK_FAILED" && signal != nil && *signal {
				needed = 1
			}
			s.RuleStates[rule] = counts
			if err = apply(tx, s, rule, signal, counts, needed, now); err != nil {
				return err
			}
		}
		return tx.Save(&s).Error
	})
}
func (c *Collector) Health(ctx context.Context, success bool, now time.Time) error {
	return c.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var cur Cursor
		if err := tx.FirstOrCreate(&cur, Cursor{Key: c.cursor()}).Error; err != nil {
			return err
		}
		if success {
			cur.Failures = 0
		} else {
			cur.Failures++
			cur.LastFailureAt = &now
		}
		if err := tx.Save(&cur).Error; err != nil {
			return err
		}
		var s Snapshot
		err := tx.First(&s, "key=?", c.key("__source__")).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s = Snapshot{Key: c.key("__source__"), Source: c.Cfg.Source, Environment: c.Cfg.Environment, TaskID: "__source__", Status: "source", FirstSeenAt: now, NextCheckAt: now, RuleStates: M{}}
		}
		rule := "MONITOR_SOURCE_UNAVAILABLE"
		v := !success
		counts := advance(obj(s.RuleStates, rule), &v)
		s.RuleStates = M{rule: counts}
		s.Evidence = M{"source_available": success, "consecutive_failures": cur.Failures}
		s.ObservedAt = now
		needed := 3
		if success {
			needed = 2
		}
		if err = apply(tx, s, rule, &v, counts, needed, now); err != nil {
			return err
		}
		return tx.Save(&s).Error
	})
}
func (c *Collector) Scan(ctx context.Context) error {
	now := time.Now().UTC()
	var cur Cursor
	err := c.DB.WithContext(ctx).First(&cur, "key=?", c.cursor()).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var upper time.Time
	if err = c.Source.DB.QueryRowContext(ctx, "SELECT UTC_TIMESTAMP(6)").Scan(&upper); err != nil {
		return err
	}
	lower := upper.Add(-24 * time.Hour)
	if cur.HighWatermark != nil {
		lower = *cur.HighWatermark
	}
	lower = lower.Add(-120 * time.Second)
	at := lower
	after := ""
	for {
		rows, e := c.Source.Rows(ctx, "updated_at>=? AND updated_at<=? AND (updated_at>? OR (updated_at=? AND task_id>?)) ORDER BY updated_at,task_id LIMIT ?", lower, upper, at, at, after, c.Cfg.Page)
		if e != nil {
			return e
		}
		for _, t := range rows {
			if e = c.Observe(ctx, t, now); e != nil {
				return e
			}
		}
		if len(rows) < c.Cfg.Page {
			break
		}
		at = rows[len(rows)-1].Updated
		after = rows[len(rows)-1].ID
	}
	if cur.HighWatermark == nil {
		after = ""
		for {
			rows, e := c.Source.Rows(ctx, "status IN ('pending','running') AND task_id>? ORDER BY task_id LIMIT ?", after, c.Cfg.Page)
			if e != nil {
				return e
			}
			for _, t := range rows {
				if e = c.Observe(ctx, t, now); e != nil {
					return e
				}
			}
			if len(rows) < c.Cfg.Page {
				break
			}
			after = rows[len(rows)-1].ID
		}
	}
	var due []Snapshot
	if err = c.DB.Where("source=? AND environment=? AND task_id <> '__source__' AND next_check_at<=?", c.Cfg.Source, c.Cfg.Environment, now).Order("next_check_at").Limit(c.Cfg.Page).Find(&due).Error; err != nil {
		return err
	}
	for _, s := range due {
		rows, e := c.Source.Rows(ctx, "task_id=?", s.TaskID)
		if e != nil {
			return e
		}
		if len(rows) > 0 {
			if e = c.Observe(ctx, rows[0], now); e != nil {
				return e
			}
		} else {
			s.Evidence["source_available"] = false
			s.Evidence["unavailable_reason"] = "source_missing"
			for k := range s.RuleStates {
				s.RuleStates[k] = advance(nil, nil)
			}
			s.NextCheckAt = now.Add(c.Cfg.Poll)
			if e = c.DB.Save(&s).Error; e != nil {
				return e
			}
		}
	}
	if cur.ReconcileAt == nil || cur.ReconcileAfter != nil || now.Sub(*cur.ReconcileAt) > 300*time.Second {
		after = ""
		if cur.ReconcileAfter != nil {
			after = *cur.ReconcileAfter
		}
		rows, e := c.Source.Rows(ctx, "(created_at>=? OR status IN ('pending','running')) AND task_id>? ORDER BY task_id LIMIT ?", upper.Add(-24*time.Hour), after, c.Cfg.Page)
		if e != nil {
			return e
		}
		for _, t := range rows {
			if e = c.Observe(ctx, t, now); e != nil {
				return e
			}
		}
		cur.ReconcileAfter = nil
		if len(rows) == c.Cfg.Page {
			x := rows[len(rows)-1].ID
			cur.ReconcileAfter = &x
		}
		cur.ReconcileAt = &now
	}
	cur.Key = c.cursor()
	if cur.HighWatermark == nil || upper.After(*cur.HighWatermark) {
		cur.HighWatermark = &upper
	}
	cur.LastSuccessAt = &now
	if err = c.DB.Save(&cur).Error; err != nil {
		return err
	}
	return c.Health(ctx, true, now)
}
