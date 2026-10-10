package monitor

import (
	"bidmon/internal/checks"
	"testing"
)

func TestSchedulerInterleavesSourcesAndRotates(t *testing.T) {
	work := []*checkPlan{}
	for i := 0; i < 100; i++ {
		work = append(work, &checkPlan{Spec: checks.CheckSpec{Source: "slow"}})
	}
	good := &checkPlan{Spec: checks.CheckSpec{Source: "good"}}
	work = append(work, good)
	for turn := 0; turn < 2; turn++ {
		ordered := interleaveChecks(work, turn)
		if len(ordered) != len(work) || ordered[1-turn] != good {
			t.Fatal("good source queued behind all slow checks")
		}
		if ordered[2] != work[1] {
			t.Fatal("check order within source changed")
		}
	}
	if len(interleaveChecks(nil, 0)) != 0 {
		t.Fatal("empty schedule")
	}
}
