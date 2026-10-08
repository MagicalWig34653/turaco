package application

import (
	"errors"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/scheduling"
)

// Recurrence frequencies and the rule type live in the platform scheduling
// package; Tasks keeps these names for its own API and tests.
const (
	FreqDaily   = scheduling.FreqDaily
	FreqWeekly  = scheduling.FreqWeekly
	FreqMonthly = scheduling.FreqMonthly
)

// Rule says when a Recurring Task Definition runs (see scheduling.Rule).
type Rule = scheduling.Rule

// validateRule validates a rule and maps scheduling's sentinel to Tasks'
// validation error.
func validateRule(r Rule) error { return mapRuleErr(r.Validate()) }

// nextRunAfter returns the next occurrence after t, mapping validation errors.
func nextRunAfter(r Rule, t time.Time) (time.Time, error) {
	n, err := r.NextAfter(t)
	return n, mapRuleErr(err)
}

func mapRuleErr(err error) error {
	var inv *scheduling.InvalidError
	if errors.As(err, &inv) {
		return invalid("%s", inv.Message)
	}
	return err
}
