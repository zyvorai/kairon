package model

import "time"

// SetCondition upserts a condition. lastTransitionTime is preserved unless Status changes.
func SetCondition(conds []Condition, typ, status, reason, message string, now time.Time) []Condition {
	now = now.UTC()
	for i := range conds {
		if conds[i].Type != typ {
			continue
		}
		if conds[i].Status == status {
			conds[i].Reason = reason
			conds[i].Message = message
			return conds
		}
		conds[i] = Condition{
			Type:               typ,
			Status:             status,
			Reason:             reason,
			Message:            message,
			LastTransitionTime: now,
		}
		return conds
	}
	return append(conds, Condition{
		Type:               typ,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	})
}

func ConditionStatus(conds []Condition, typ string) string {
	for _, c := range conds {
		if c.Type == typ {
			return c.Status
		}
	}
	return ""
}
