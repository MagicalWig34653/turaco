// Package application holds the Catalog use cases. A Catalog Item carries one
// definition: form fields from a closed set of types, approval steps and
// fulfillment task templates (ADR-0025). The catalog owns the schema and the
// validation of answers against it.
package application

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Field types: the closed set a form may use.
const (
	FieldText     = "text"
	FieldLongText = "longtext"
	FieldNumber   = "number"
	FieldBoolean  = "boolean"
	FieldDate     = "date"
	FieldSelect   = "select"
	FieldUser     = "user"
	FieldProduct  = "product"
)

// Limits keep a definition small and reviewable.
const (
	MaxFields          = 30
	MaxApprovalSteps   = 5
	MaxFulfillment     = 20
	MaxOptions         = 50
	MaxProductChoices  = 50
	maxLabel           = 100
	maxHelp            = 300
	maxTextDefault     = 200
	maxTextLimit       = 1000
	maxLongTextDefault = 2000
	maxLongTextLimit   = 5000
	maxTaskTitle       = 150
	maxTaskDesc        = 2000
	maxDueAfterHours   = 8760
)

var (
	fieldKey   = regexp.MustCompile(`^[a-z][a-zA-Z0-9]{0,39}$`)
	optionVal  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
	uuidRegexp = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// Option is one choice of a select field.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Field is one question of the request form.
type Field struct {
	Key      string `json:"key"`
	Type     string `json:"type"`
	Label    string `json:"label"`
	Help     string `json:"help,omitempty"`
	Required bool   `json:"required"`
	// MaxLength bounds text and longtext answers.
	MaxLength *int `json:"maxLength,omitempty"`
	// Min and Max bound number answers (inclusive).
	Min *int `json:"min,omitempty"`
	Max *int `json:"max,omitempty"`
	// Options are the choices of a select field.
	Options []Option `json:"options,omitempty"`
	// CategoryID or ProductIDs restrict a product field to active products of
	// that category or to the listed products; exactly one is required.
	CategoryID string   `json:"categoryId,omitempty"`
	ProductIDs []string `json:"productIds,omitempty"`
}

// ApprovalStep names who approves; exactly one of the three.
type ApprovalStep struct {
	ApproverUserID *string `json:"approverUserId,omitempty"`
	ApproverTeamID *string `json:"approverTeamId,omitempty"`
	// Approver "manager" means the manager of the requested-for User.
	Approver string `json:"approver,omitempty"`
	// FallbackTeamID is only valid with Approver "manager": when the requested-for User has no manager, or the manager
	// may not decide (requester, requested-for or named in an answer), this Team approves instead. The Team's members
	// are still bound by the exclusions, so nobody approves their own request.
	FallbackTeamID *string `json:"fallbackTeamId,omitempty"`
}

// TaskTemplate is one fulfillment task created when the request is approved.
type TaskTemplate struct {
	Title          string  `json:"title"`
	Description    string  `json:"description,omitempty"`
	Priority       string  `json:"priority,omitempty"`
	AssignedUserID *string `json:"assignedUserId,omitempty"`
	AssignedTeamID *string `json:"assignedTeamId,omitempty"`
	// Optional (false) tasks do not have to be completed for the request to complete.
	Mandatory     *bool `json:"mandatory,omitempty"`
	DueAfterHours *int  `json:"dueAfterHours,omitempty"`
}

// IsMandatory is true unless the template says otherwise.
func (t TaskTemplate) IsMandatory() bool { return t.Mandatory == nil || *t.Mandatory }

// Definition is the whole schema of a Catalog Item.
type Definition struct {
	Fields []Field `json:"fields"`
	// AllowRequestedFor lets the requester pick another User the request is for.
	AllowRequestedFor bool           `json:"allowRequestedFor"`
	Approvals         []ApprovalStep `json:"approvals"`
	Fulfillment       []TaskTemplate `json:"fulfillment"`
}

// ParseDefinition decodes and validates a definition. Unknown properties and
// trailing data are rejected; the result is normalized (trimmed text).
// References to Users, Teams, Products and Categories are checked for shape
// only; the service checks that they exist.
func ParseDefinition(raw []byte) (Definition, error) {
	var d Definition
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return Definition{}, invalid("definition is not valid: %v", cleanJSONError(err))
	}
	if dec.More() {
		return Definition{}, invalid("definition has trailing data")
	}
	if err := d.normalizeAndValidate(); err != nil {
		return Definition{}, err
	}
	return d, nil
}

func cleanJSONError(err error) string {
	msg := err.Error()
	if len(msg) > 120 {
		msg = msg[:120]
	}
	return msg
}

func text(field, s string, min, max int, multiline bool) (string, error) {
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	if n < min || n > max || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, multiline) {
		return "", invalid("%s must be %d-%d characters without control or invisible formatting characters", field, min, max)
	}
	return s, nil
}

func (d *Definition) normalizeAndValidate() error {
	if len(d.Fields) > MaxFields {
		return invalid("a definition has at most %d fields", MaxFields)
	}
	keys := map[string]struct{}{}
	for i := range d.Fields {
		f := &d.Fields[i]
		if !fieldKey.MatchString(f.Key) {
			return invalid("field key %q must start with a lower-case letter and contain only letters and digits (at most 40)", f.Key)
		}
		if _, dup := keys[f.Key]; dup {
			return invalid("field key %q is used twice", f.Key)
		}
		keys[f.Key] = struct{}{}
		var err error
		if f.Label, err = text("field label", f.Label, 1, maxLabel, false); err != nil {
			return err
		}
		if f.Help != "" {
			if f.Help, err = text("field help", f.Help, 1, maxHelp, false); err != nil {
				return err
			}
		}
		if err := f.validateType(); err != nil {
			return err
		}
	}
	if len(d.Approvals) > MaxApprovalSteps {
		return invalid("a definition has at most %d approval steps", MaxApprovalSteps)
	}
	for i, a := range d.Approvals {
		n := 0
		if a.ApproverUserID != nil {
			n++
			if !uuidRegexp.MatchString(*a.ApproverUserID) {
				return invalid("approval step %d: approverUserId must be a user id", i+1)
			}
		}
		if a.ApproverTeamID != nil {
			n++
			if !uuidRegexp.MatchString(*a.ApproverTeamID) {
				return invalid("approval step %d: approverTeamId must be a team id", i+1)
			}
		}
		if a.Approver != "" {
			n++
			if a.Approver != "manager" {
				return invalid("approval step %d: approver must be manager", i+1)
			}
		}
		if n != 1 {
			return invalid("approval step %d needs exactly one approver", i+1)
		}
		if a.FallbackTeamID != nil {
			if a.Approver != "manager" {
				return invalid("approval step %d: fallbackTeamId is only valid together with approver manager", i+1)
			}
			if !uuidRegexp.MatchString(*a.FallbackTeamID) {
				return invalid("approval step %d: fallbackTeamId must be a team id", i+1)
			}
		}
	}
	if len(d.Fulfillment) > MaxFulfillment {
		return invalid("a definition has at most %d fulfillment tasks", MaxFulfillment)
	}
	for i := range d.Fulfillment {
		t := &d.Fulfillment[i]
		var err error
		if t.Title, err = text("task title", t.Title, 1, maxTaskTitle, false); err != nil {
			return err
		}
		if t.Description != "" {
			if t.Description, err = text("task description", t.Description, 1, maxTaskDesc, true); err != nil {
				return err
			}
		}
		switch t.Priority {
		case "", "low", "normal", "high", "urgent":
		default:
			return invalid("task %d: priority must be low, normal, high or urgent", i+1)
		}
		if t.AssignedUserID != nil && !uuidRegexp.MatchString(*t.AssignedUserID) || t.AssignedTeamID != nil && !uuidRegexp.MatchString(*t.AssignedTeamID) {
			return invalid("task %d: assignees must be user or team ids", i+1)
		}
		if t.DueAfterHours != nil && (*t.DueAfterHours < 1 || *t.DueAfterHours > maxDueAfterHours) {
			return invalid("task %d: dueAfterHours must be 1 to %d", i+1, maxDueAfterHours)
		}
	}
	return nil
}

func (f *Field) validateType() error {
	if f.Type != FieldText && f.Type != FieldLongText {
		if f.MaxLength != nil {
			return invalid("field %q: maxLength applies to text fields only", f.Key)
		}
	}
	if f.Type != FieldNumber && (f.Min != nil || f.Max != nil) {
		return invalid("field %q: min and max apply to number fields only", f.Key)
	}
	if f.Type != FieldSelect && len(f.Options) > 0 {
		return invalid("field %q: options apply to select fields only", f.Key)
	}
	if f.Type != FieldProduct && (f.CategoryID != "" || len(f.ProductIDs) > 0) {
		return invalid("field %q: categoryId and productIds apply to product fields only", f.Key)
	}
	switch f.Type {
	case FieldText, FieldLongText:
		def, limit := maxTextDefault, maxTextLimit
		if f.Type == FieldLongText {
			def, limit = maxLongTextDefault, maxLongTextLimit
		}
		if f.MaxLength == nil {
			f.MaxLength = &def
		}
		if *f.MaxLength < 1 || *f.MaxLength > limit {
			return invalid("field %q: maxLength must be 1 to %d", f.Key, limit)
		}
	case FieldNumber:
		if f.Min != nil && f.Max != nil && *f.Min > *f.Max {
			return invalid("field %q: min must not exceed max", f.Key)
		}
	case FieldBoolean, FieldDate, FieldUser:
	case FieldSelect:
		if len(f.Options) == 0 || len(f.Options) > MaxOptions {
			return invalid("field %q: a select field needs 1 to %d options", f.Key, MaxOptions)
		}
		seen := map[string]struct{}{}
		for i := range f.Options {
			o := &f.Options[i]
			if !optionVal.MatchString(o.Value) {
				return invalid("field %q: option value %q must be lower-case letters, digits, - or _", f.Key, o.Value)
			}
			if _, dup := seen[o.Value]; dup {
				return invalid("field %q: option value %q is used twice", f.Key, o.Value)
			}
			seen[o.Value] = struct{}{}
			var err error
			if o.Label, err = text("option label", o.Label, 1, maxLabel, false); err != nil {
				return err
			}
		}
	case FieldProduct:
		if (f.CategoryID == "") == (len(f.ProductIDs) == 0) {
			return invalid("field %q: a product field needs exactly one of categoryId and productIds", f.Key)
		}
		if f.CategoryID != "" && !uuidRegexp.MatchString(f.CategoryID) {
			return invalid("field %q: categoryId must be a category id", f.Key)
		}
		if len(f.ProductIDs) > MaxProductChoices {
			return invalid("field %q: at most %d product choices", f.Key, MaxProductChoices)
		}
		seen := map[string]struct{}{}
		for _, id := range f.ProductIDs {
			if !uuidRegexp.MatchString(id) {
				return invalid("field %q: productIds must be product ids", f.Key)
			}
			if _, dup := seen[id]; dup {
				return invalid("field %q: product %s is listed twice", f.Key, id)
			}
			seen[id] = struct{}{}
		}
	default:
		return invalid("field %q: type must be one of text, longtext, number, boolean, date, select, user, product", f.Key)
	}
	return nil
}

// errTooLarge guards the raw definition size before parsing.
var errTooLarge = errors.New("catalog: definition is too large")

// MaxDefinitionBytes bounds the raw JSON accepted for a definition.
const MaxDefinitionBytes = 64 << 10

func checkSize(raw []byte) error {
	if len(raw) > MaxDefinitionBytes {
		return errTooLarge
	}
	return nil
}

// Marshal returns the canonical stored form.
func (d Definition) Marshal() ([]byte, error) {
	if d.Fields == nil {
		d.Fields = []Field{}
	}
	if d.Approvals == nil {
		d.Approvals = []ApprovalStep{}
	}
	if d.Fulfillment == nil {
		d.Fulfillment = []TaskTemplate{}
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("marshal definition: %w", err)
	}
	return raw, nil
}
