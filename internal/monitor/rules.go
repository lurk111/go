package monitor

import "time"

var ruleNames = []string{"TASK_FAILED", "QUEUE_DELAY", "EXECUTION_STALE", "RESULT_MISSING", "RESULT_EMPTY_SUSPECT", "RESULT_STRUCTURE_INVALID", "SOURCE_STATE_UNKNOWN"}

func bit(v bool) *bool { return &v }
func Evaluate(t Task, summary M, now time.Time, c Config, prev M) map[string]*bool {
	r := map[string]*bool{}
	for _, k := range ruleNames {
		r[k] = nil
	}
	known := t.Status == "pending" || t.Status == "running" || t.Status == "completed" || t.Status == "failed" || t.Status == "canceled"
	r["SOURCE_STATE_UNKNOWN"] = bit(!known)
	if !known {
		return r
	}
	if t.Status == "failed" {
		r["TASK_FAILED"] = bit(true)
	} else if t.Status == "completed" {
		r["TASK_FAILED"] = bit(false)
	}
	r["QUEUE_DELAY"] = bit(false)
	if t.Status == "pending" {
		since := t.Created
		if len(prev) > 0 && str(prev, "status") != "pending" {
			since = now
		} else if !parsed(prev, "pending_since").IsZero() {
			since = parsed(prev, "pending_since")
		}
		r["QUEUE_DELAY"] = bit(now.Sub(since) > c.Queue)
	}
	if t.Status == "running" {
		if now.Sub(t.Updated) > c.Stale {
			r["EXECUTION_STALE"] = bit(true)
		} else if len(prev) == 0 || t.Stage != str(prev, "stage") || (!parsed(prev, "progress_observed_at").IsZero() && now.Sub(parsed(prev, "progress_observed_at")) <= c.Stale) {
			r["EXECUTION_STALE"] = bit(false)
		}
	} else if t.Status == "completed" {
		r["EXECUTION_STALE"] = bit(false)
	}
	if t.Status == "completed" {
		finished := t.Updated
		if t.Finished != nil {
			finished = *t.Finished
		}
		if now.Sub(finished) >= c.Grace {
			state := str(summary, "state")
			if state == "missing" || state == "present" || state == "empty" {
				r["RESULT_MISSING"] = bit(state == "missing")
			}
			if state == "present" || state == "empty" {
				r["RESULT_EMPTY_SUSPECT"] = bit(state == "empty")
			}
			if state != "missing" {
				r["RESULT_STRUCTURE_INVALID"] = bit(state == "invalid" || state == "ambiguous")
			}
		}
	}
	return r
}
func advance(prev M, v *bool) M {
	bad, good := 0, 0
	if v != nil {
		if *v {
			bad = min(int(num(prev, "bad"))+1, 3)
		} else {
			good = min(int(num(prev, "good"))+1, 3)
		}
	}
	return M{"bad": bad, "good": good}
}
