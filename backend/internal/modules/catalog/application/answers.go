package application

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Reference is a typed link from an answer to another record, promoted out of
// the answers so relationships stay queryable (ADR-0025).
type Reference struct {
	FieldKey string
	// Type is user, product, location or asset.
	Type string
	ID   string
}

// ProductLookup is what answer validation needs from Products.
type ProductLookup interface {
	// ActiveProducts returns id -> category id ("" when none) for active products among ids.
	ActiveProducts(ctx context.Context, ids []string) (map[string]string, error)
}

// UserLookup is what answer validation needs from Organization.
type UserLookup interface {
	ActiveUsers(ctx context.Context, ids []string) (map[string]bool, error)
}

// LocationLookup resolves active Organization Locations for request answers.
type LocationLookup interface {
	ActiveLocations(ctx context.Context, ids []string) (map[string]bool, error)
	LocationNames(ctx context.Context, ids []string) (map[string]string, error)
}

// AssetChoice is the display snapshot kept with an asset answer.
type AssetChoice struct{ Name, Reference string }

// AssetLookup authorizes each asset for the requester and returns its display snapshot.
type AssetLookup interface {
	RequestAsset(ctx context.Context, id, requesterID string, viewAll bool) (AssetChoice, bool, error)
}

// FieldErrors collects one message per invalid field key, so a form can show
// them next to the fields. It is a validation error, not an internal one.
type FieldErrors struct{ Errors map[string]string }

func (e *FieldErrors) Error() string {
	keys := make([]string, 0, len(e.Errors))
	for k := range e.Errors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+e.Errors[k])
	}
	return "answers are invalid: " + strings.Join(parts, "; ")
}

// Codes of field errors; clients localize them.
const (
	CodeRequired     = "required"
	CodeType         = "invalid_type"
	CodeTooLong      = "too_long"
	CodeRange        = "out_of_range"
	CodeChoice       = "invalid_choice"
	CodeUnknownField = "unknown_field"
	CodeNotFound     = "not_found"
	CodeDate         = "invalid_date"
	CodeCharacters   = "invalid_characters"
	CodeNotAllowed   = "not_allowed"
	CodeNotActive    = "not_active"
	maxAnswerBytes   = 64 << 10
)

// ValidateAnswers checks answers against the definition: every key must be a
// defined field, required fields must be present, values must match the field
// type and limits, and user/product references must exist and be active (and
// products must be among the field's choices). It returns the normalized
// answers (trimmed text, integral numbers as int) and the typed references.
func ValidateAnswers(ctx context.Context, d Definition, answers map[string]any, users UserLookup, products ProductLookup) (map[string]any, []Reference, error) {
	errs := map[string]string{}
	fields := map[string]Field{}
	for _, f := range d.Fields {
		fields[f.Key] = f
	}
	for k := range answers {
		if _, ok := fields[k]; !ok {
			errs[k] = CodeUnknownField
		}
	}
	out := map[string]any{}
	var refs []Reference
	var userIDs, productIDs []string
	for _, f := range d.Fields {
		raw, present := answers[f.Key]
		if !present || raw == nil || raw == "" {
			if f.Required {
				errs[f.Key] = CodeRequired
			}
			continue
		}
		v, code := normalizeAnswer(f, raw)
		if code != "" {
			errs[f.Key] = code
			continue
		}
		out[f.Key] = v
		switch f.Type {
		case FieldUser:
			userIDs = append(userIDs, v.(string))
		case FieldProduct:
			productIDs = append(productIDs, v.(string))
		}
	}
	if len(userIDs) > 0 {
		active, err := users.ActiveUsers(ctx, userIDs)
		if err != nil {
			return nil, nil, fmt.Errorf("check users: %w", err)
		}
		for _, f := range d.Fields {
			if id, ok := out[f.Key].(string); ok && f.Type == FieldUser {
				if !active[id] {
					errs[f.Key] = CodeNotActive
				} else {
					refs = append(refs, Reference{FieldKey: f.Key, Type: "user", ID: id})
				}
			}
		}
	}
	if len(productIDs) > 0 {
		active, err := products.ActiveProducts(ctx, productIDs)
		if err != nil {
			return nil, nil, fmt.Errorf("check products: %w", err)
		}
		for _, f := range d.Fields {
			id, ok := out[f.Key].(string)
			if !ok || f.Type != FieldProduct {
				continue
			}
			cat, isActive := active[id]
			switch {
			case !isActive:
				errs[f.Key] = CodeNotActive
			case !productAllowed(f, id, cat):
				errs[f.Key] = CodeNotAllowed
			default:
				refs = append(refs, Reference{FieldKey: f.Key, Type: "product", ID: id})
			}
		}
	}
	if len(errs) > 0 {
		return nil, nil, &FieldErrors{Errors: errs}
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].FieldKey < refs[j].FieldKey })
	return out, refs, nil
}

// ValidateRequestAnswers adds location and asset validation to the original
// closed-field validator. The stored answer retains the id and a display
// snapshot; the separate typed reference remains queryable.
func ValidateRequestAnswers(ctx context.Context, d Definition, answers map[string]any, users UserLookup, products ProductLookup, locations LocationLookup, assets AssetLookup, requesterID string, viewAssets bool) (map[string]any, []Reference, error) {
	out, refs, err := ValidateAnswers(ctx, d, answers, users, products)
	if err != nil {
		return nil, nil, err
	}
	errs := map[string]string{}
	var locationIDs []string
	for _, f := range d.Fields {
		if f.Type == FieldLocation {
			if id, ok := out[f.Key].(string); ok {
				locationIDs = append(locationIDs, id)
			}
		}
	}
	if len(locationIDs) > 0 {
		active, err := locations.ActiveLocations(ctx, locationIDs)
		if err != nil {
			return nil, nil, fmt.Errorf("check locations: %w", err)
		}
		names, err := locations.LocationNames(ctx, locationIDs)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve locations: %w", err)
		}
		for _, f := range d.Fields {
			if f.Type != FieldLocation {
				continue
			}
			id, ok := out[f.Key].(string)
			if !ok {
				continue
			}
			if !active[id] || names[id] == "" {
				errs[f.Key] = CodeNotActive
				continue
			}
			out[f.Key] = map[string]string{"id": id, "name": names[id]}
			refs = append(refs, Reference{FieldKey: f.Key, Type: FieldLocation, ID: id})
		}
	}
	for _, f := range d.Fields {
		if f.Type != FieldAsset {
			continue
		}
		id, ok := out[f.Key].(string)
		if !ok {
			continue
		}
		choice, allowed, err := assets.RequestAsset(ctx, id, requesterID, viewAssets)
		if err != nil {
			return nil, nil, fmt.Errorf("check asset: %w", err)
		}
		if !allowed {
			errs[f.Key] = CodeNotAllowed
			continue
		}
		out[f.Key] = map[string]string{"id": id, "name": choice.Name, "reference": choice.Reference}
		refs = append(refs, Reference{FieldKey: f.Key, Type: FieldAsset, ID: id})
	}
	if len(errs) > 0 {
		return nil, nil, &FieldErrors{Errors: errs}
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].FieldKey < refs[j].FieldKey })
	return out, refs, nil
}

func productAllowed(f Field, id, category string) bool {
	if f.CategoryID != "" {
		return strings.EqualFold(f.CategoryID, category)
	}
	for _, p := range f.ProductIDs {
		if strings.EqualFold(p, id) {
			return true
		}
	}
	return false
}

// normalizeAnswer checks one value against its field; it returns an error code or "".
func normalizeAnswer(f Field, raw any) (any, string) {
	switch f.Type {
	case FieldText, FieldLongText:
		s, ok := raw.(string)
		if !ok {
			return nil, CodeType
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, CodeRequired
		}
		if utf8.RuneCountInString(s) > maxLength(f) {
			return nil, CodeTooLong
		}
		if !utf8.ValidString(s) || safetext.ContainsUnsafe(s, f.Type == FieldLongText) {
			return nil, CodeCharacters
		}
		return s, ""
	case FieldNumber:
		n, ok := raw.(float64)
		if !ok || n != math.Trunc(n) || math.Abs(n) > 1e9 {
			return nil, CodeType
		}
		i := int(n)
		if f.Min != nil && i < *f.Min || f.Max != nil && i > *f.Max {
			return nil, CodeRange
		}
		return i, ""
	case FieldBoolean:
		b, ok := raw.(bool)
		if !ok {
			return nil, CodeType
		}
		return b, ""
	case FieldDate:
		s, ok := raw.(string)
		if !ok {
			return nil, CodeType
		}
		if _, err := time.Parse(time.DateOnly, s); err != nil {
			return nil, CodeDate
		}
		return s, ""
	case FieldSelect:
		s, ok := raw.(string)
		if !ok {
			return nil, CodeType
		}
		for _, o := range f.Options {
			if o.Value == s {
				return s, ""
			}
		}
		return nil, CodeChoice
	case FieldUser, FieldProduct, FieldLocation, FieldAsset:
		s, ok := raw.(string)
		if !ok || !uuidRegexp.MatchString(s) {
			return nil, CodeType
		}
		return strings.ToLower(s), ""
	}
	return nil, CodeType
}

// maxLength is the field's limit; a stored definition without one (older or
// hand-written data) falls back to the type's default instead of failing.
func maxLength(f Field) int {
	if f.MaxLength != nil {
		return *f.MaxLength
	}
	if f.Type == FieldLongText {
		return maxLongTextDefault
	}
	return maxTextDefault
}
