package model

import (
	"testing"
	"time"
)

func TestSetConditionPreservesTransitionTime(t *testing.T) {
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)
	conds := SetCondition(nil, ConditionReady, "False", "Pending", "waiting", t0)
	conds = SetCondition(conds, ConditionReady, "False", "StillPending", "still waiting", t1)
	if !conds[0].LastTransitionTime.Equal(t0) {
		t.Fatalf("transition time changed on same status: %v", conds[0].LastTransitionTime)
	}
	if conds[0].Reason != "StillPending" {
		t.Fatalf("reason=%q", conds[0].Reason)
	}
	conds = SetCondition(conds, ConditionReady, "True", "Running", "", t1)
	if !conds[0].LastTransitionTime.Equal(t1) {
		t.Fatalf("expected new transition time on status change")
	}
}
