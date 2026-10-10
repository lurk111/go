package monitor

import (
	"bidmon/internal/checks"
	"bidmon/internal/evidence"
	"context"
	"encoding/json"
	"time"
)

// Detail SQL comes from the frozen check definition, never from log/model text.
func collectGenericDetails(ctx context.Context, cfg Config, event Event) M {
	req := evidence.ProfileRequest{Version: 2, EventID: event.EventID, CheckID: event.CheckID, ResourceID: event.EntityKey, Operation: "details"}
	finish := func(content any, reason string) M {
		result := M(evidence.ProfileResult(req, content, reason, false, time.Now()))
		result["source"] = "monitor-db"
		result["task_id"] = event.TaskID
		raw, _ := json.Marshal(result)
		if len(raw) > evidence.MaxBytes {
			result["content"] = nil
			result["truncated"] = true
			result["unavailable_reason"] = "output_limit"
		}
		return result
	}
	spec := eventCheck(event)
	var source checks.SourceSpec
	raw, _ := json.Marshal(event.Definition["source"])
	if json.Unmarshal(raw, &source) != nil || source.ID != spec.Source {
		return finish(nil, "source_definition_unavailable")
	}
	resolver := checks.ResolveSecret
	if cfg.Runtime != nil {
		resolver = cfg.Runtime.resolveSecret
	}
	runner, err := checks.NewRunner(source, resolver)
	if err != nil {
		return finish(nil, "source_unavailable")
	}
	defer runner.Close()
	fields := previousFields(spec, obj(event.Evidence, "fields"))
	if len(fields) == 0 {
		return finish(nil, "detail_fields_unavailable")
	}
	rows, err := runner.Details(ctx, spec, checks.Observation{CheckID: spec.ID, EntityKey: event.EntityKey, Fields: map[string]any(fields)})
	if err != nil {
		return finish(nil, "detail_query_failed")
	}
	if len(rows) == 0 {
		return finish(nil, "no_matching_details")
	}
	return finish(rows, "")
}
