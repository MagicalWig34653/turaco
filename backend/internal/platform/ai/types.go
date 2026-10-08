// Package ai is Turaco's AI runtime (F12, ADR-0029): the provider port, the tool registry, the session-bound
// conversation runtime, caps and usage, and the AI settings. It is a platform package and owns no business data.
// Modules contribute read tools through their public contracts; this package never imports a module.
//
// The structural controls (not the prompt) are what keep the assistant inside the User's authority: a fixed tool
// set derived from the User's permissions and the provider's allowed data classes, schema-validated arguments,
// a per-call permission re-check from the HTTP session, a per-conversation resource scope, field-allowlisted
// result DTOs, server-held transcripts and atomic cap reservations.
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// DataClass classifies data for provider eligibility (A6). The list is closed: secrets, Audit records, Workforce
// Presence details, raw provider payloads, Artifact file contents and Remote Access session data are not classes
// and can never be sent.
type DataClass string

const (
	ClassPublicReference DataClass = "public_reference"
	ClassBusinessRecord  DataClass = "business_record"
	ClassPersonalContact DataClass = "personal_contact"
	ClassDeviceContext   DataClass = "device_context"
)

// DataClasses is the closed class list.
var DataClasses = []DataClass{ClassPublicReference, ClassBusinessRecord, ClassPersonalContact, ClassDeviceContext}

// Valid reports whether c is a known class.
func (c DataClass) Valid() bool { return slices.Contains(DataClasses, c) }

// Risk is a tool's risk class (A2). Only read tools exist in A-A.
type Risk string

const (
	RiskRead       Risk = "read"
	RiskWrite      Risk = "write"
	RiskHighImpact Risk = "high_impact"
)

// Field is one allowed leaf of a tool's output DTO. Path uses dots for objects and [] for arrays
// ("messages[].body"). Class is the data class of that field; MaxLen caps strings in runes (default 500).
type Field struct {
	Path   string
	Class  DataClass
	MaxLen int
}

// Target names the record a tool call reads, for the turn resource scope (A11): the input property Arg holds the
// id of a record of Type.
type Target struct {
	Type string
	Arg  string
}

// Tool is one AI Tool: a typed function over a module's public application contract (A2). The handler returns a
// dedicated DTO; the runtime serializes it and rejects any field that is not declared in Output.
type Tool struct {
	Name        string
	Description string
	// InputSchema is a JSON schema in the supported subset (see ValidateInput): an object with typed properties,
	// additionalProperties false, maxLength on strings and minimum/maximum on integers.
	InputSchema json.RawMessage
	// Permission is the module's existing permission key; it is checked for every call against the caller.
	Permission string
	Risk       Risk
	// Output lists every allowed leaf of the result (A13). The data classes sent are derived from it.
	Output []Field
	// Target is set for tools that read one record by id; nil for discovery tools such as knowledge.search.
	Target *Target
	// Handler runs with the caller built from the HTTP session. It must return ErrToolNotFound,
	// ErrToolForbidden or ErrToolInvalid for the matching module outcomes; any other error is an internal failure.
	Handler func(ctx context.Context, caller Caller, input json.RawMessage) (any, error)
}

// Classes returns the distinct data classes of the tool's output fields.
func (t Tool) Classes() []DataClass {
	var out []DataClass
	for _, f := range t.Output {
		if !slices.Contains(out, f.Class) {
			out = append(out, f.Class)
		}
	}
	slices.Sort(out)
	return out
}

// Tool outcomes a handler may return.
var (
	ErrToolNotFound  = errors.New("ai: tool target not found")
	ErrToolForbidden = errors.New("ai: tool call not permitted")
	ErrToolInvalid   = errors.New("ai: tool arguments invalid")
)

// Caller is the requesting User's authorization context. It is built from the HTTP session only, never from the
// model, the request body or a tool argument (A3).
type Caller struct {
	UserID        string
	TenantID      string
	SessionID     string
	CorrelationID string
	Permissions   map[string]struct{}
}

// Has reports whether the caller holds the permission.
func (c Caller) Has(permission string) bool {
	_, ok := c.Permissions[permission]
	return ok
}

// ResourceRef identifies a record by module type and id (A11).
type ResourceRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type allowedClassesKey struct{}

// WithAllowedClasses stores the active provider's allowed data classes for handlers that adapt to them.
func WithAllowedClasses(ctx context.Context, classes []DataClass) context.Context {
	return context.WithValue(ctx, allowedClassesKey{}, classes)
}

// AllowedClasses returns the classes stored by WithAllowedClasses.
func AllowedClasses(ctx context.Context) []DataClass {
	c, _ := ctx.Value(allowedClassesKey{}).([]DataClass)
	return c
}

// Message is one conversation message. Roles: user, assistant, tool.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`
	ToolCallID string     `json:"toolCallId,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ToolCall is a model-requested tool invocation.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolDef is the model-facing definition of an offered tool.
type ToolDef struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// ChatRequest is one provider call.
type ChatRequest struct {
	System          string
	Messages        []Message
	Tools           []ToolDef
	MaxOutputTokens int
}

// ChatResponse is a provider's answer: text or tool calls, with the token usage it reported.
type ChatResponse struct {
	Content   string
	ToolCalls []ToolCall
	TokensIn  int
	TokensOut int
}

// Capabilities describes a provider adapter.
type Capabilities struct {
	ToolCalling bool
	Local       bool
	MaxContext  int
}

// Provider is the port to a model runtime (A4). Adapters only translate wire formats; vendor types never leave
// the adapter.
type Provider interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	Capabilities() Capabilities
}

// ErrProviderFailed marks a provider call that failed; the cause is logged by the adapter without content.
var ErrProviderFailed = errors.New("ai: provider call failed")

// ProviderRecord is the stored configuration of an AI Provider.
type ProviderRecord struct {
	ID                 string
	Kind               string
	DisplayName        string
	EndpointURL        string
	Model              string
	Local              bool
	AllowedDataClasses []DataClass
	DPARecordedOn      *string
	NoTrainingConfirm  bool
	Region             string
	SecretRef          *string
	Enabled            bool
	PriceInPerMTok     float64
	PriceOutPerMTok    float64
	Version            int
	CreatedAt          string
	UpdatedAt          string
}

// Allows reports whether the provider may receive data of class c.
func (p ProviderRecord) Allows(c DataClass) bool { return slices.Contains(p.AllowedDataClasses, c) }

// ProviderFactory builds the adapter for a stored provider. The composition root supplies it, so platform/ai
// does not import the adapters or read secrets itself.
type ProviderFactory func(rec ProviderRecord) (Provider, error)

// Errors of the service. Transport maps them to typed API codes.
var (
	ErrDisabled            = errors.New("ai: disabled")
	ErrForbidden           = errors.New("ai: forbidden")
	ErrNotFound            = errors.New("ai: not found")
	ErrVersionConflict     = errors.New("ai: version conflict")
	ErrRateLimited         = errors.New("ai: rate limited")
	ErrBudgetExceeded      = errors.New("ai: budget exceeded")
	ErrProviderUnavailable = errors.New("ai: provider unavailable")
	ErrProviderChanged     = errors.New("ai: provider changed")
	ErrTurnInProgress      = errors.New("ai: turn in progress")
	ErrContextTooLarge     = errors.New("ai: conversation too large")
)

// InvalidInputError is a client-correctable validation failure.
type InvalidInputError struct{ Message string }

func (e *InvalidInputError) Error() string { return e.Message }

func invalid(format string, a ...any) error {
	return &InvalidInputError{Message: fmt.Sprintf(format, a...)}
}
