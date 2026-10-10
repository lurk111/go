package monitor

import (
	"bidmon/internal/checks"
	"context"
	"errors"
	"gorm.io/gorm"
	"time"
)

// PreviewRetention executes COUNT queries only. No cleanup is scheduled.
func PreviewRetention(ctx context.Context, db *gorm.DB, policy checks.RetentionSpec, now time.Time) (M, error) {
	for _, days := range []int{policy.ClosedEvidenceDays, policy.SnapshotDays, policy.DeliveredDays} {
		if days < 0 || days > 3650 {
			return nil, errors.New("retention days must be 0..3650")
		}
	}
	result := M{"preview_only": true, "automatic_cleanup": false, "policy": policy}
	var events, snapshots, deliveries int64
	if policy.ClosedEvidenceDays > 0 {
		if err := db.WithContext(ctx).Model(&Event{}).Where("NOT condition_active AND status <> 'open' AND closed_at < ?", now.AddDate(0, 0, -policy.ClosedEvidenceDays)).Where("NOT EXISTS (SELECT 1 FROM delivery_outbox o WHERE o.event_id=monitor_events.event_id AND o.status IN ('pending','retry','sending','needs_attention'))").Where("NOT EXISTS (SELECT 1 FROM diagnosis_jobs j WHERE j.event_id=monitor_events.event_id AND j.status IN ('queued','running'))").Count(&events).Error; err != nil {
			return nil, err
		}
	}
	if policy.DeliveredDays > 0 {
		if err := db.WithContext(ctx).Model(&Outbox{}).Where("status='delivered' AND confirmed_at < ?", now.AddDate(0, 0, -policy.DeliveredDays)).Count(&deliveries).Error; err != nil {
			return nil, err
		}
	}
	if policy.SnapshotDays > 0 {
		if err := db.WithContext(ctx).Model(&Snapshot{}).Where("observed_at < ?", now.AddDate(0, 0, -policy.SnapshotDays)).Where("NOT EXISTS (SELECT 1 FROM monitor_events e WHERE e.task_key=task_snapshots.key AND e.condition_active)").Count(&snapshots).Error; err != nil {
			return nil, err
		}
	}
	result["closed_event_evidence"] = events
	result["inactive_snapshots"] = snapshots
	result["confirmed_deliveries"] = deliveries
	return result, nil
}
