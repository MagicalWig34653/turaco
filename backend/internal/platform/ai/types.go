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
	"github.com/MagicalWig34653/turaco/backend/internal/platform/health"
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

// Risk is a tool's risk class (A2). Read tools run within a conversation; write tools only create an AI Proposal
// that the User confirms (A-C); high_impact operations are never executed by AI and cannot be registered.
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
	// Read tools only.
	Handler func(ctx context.Context, caller Caller, input json.RawMessage) (any, error)
	// Write is the two-phase contract of a write tool (A-C). Write tools have no Handler and no Output: the model
	// learns only that a proposal awaits the User's confirmation.
	Write *WriteSpec
}

// WriteEffect is who can see the result of a write tool. Only EffectInternal exists: writes that send data outside
// Turaco (requester-visible comments, email) need the full-content dialog review first (design open decision 7).
type WriteEffect string

// EffectInternal: the result is visible to Turaco staff only and nothing leaves Turaco.
const EffectInternal WriteEffect = "internal"

// WriteSpec is a write tool's contract with the module that owns the operation.
type WriteSpec struct {
	Effect WriteEffect
	// Prepare runs as the requesting User when the model calls the tool. It changes nothing: it checks that the User
	// may perform the operation on the target now and returns the target's current version and a display label.
	Prepare func(ctx context.Context, caller Caller, input json.RawMessage) (Preparation, error)
	// Replay checks whether a prior attempt committed its effect. It is read-only and runs before current
	// permission and target checks, so permission loss or a changed target cannot hide a committed outcome.
	Replay func(ctx context.Context, caller Caller, input json.RawMessage, x Execution) (ExecutionResult, bool, error)
	// Execute performs the operation through the module's public contract as the confirming User. It must be
	// idempotent per x.IdempotencyKey (a repeated call returns the first result without a second effect) and must
	// fail with ErrToolStale when the target no longer has x.TargetVersion. The context carries the audit marker
	// (via=ai, proposal id), so the module's own audit entries are marked AI-assisted.
	Execute func(ctx context.Context, caller Caller, input json.RawMessage, x Execution) (ExecutionResult, error)
}

// Preparation is the target of a proposal as Prepare saw it.
type Preparation struct {
	TargetVersion int
	// TargetLabel is shown to the creating User in the confirm dialog (for example the Ticket reference and title);
	// it is never sent to the model.
	TargetLabel string
}

// Execution identifies one execution of a confirmed proposal.
type Execution struct {
	ProposalID     string
	IdempotencyKey string
	TargetVersion  int
}

// ExecutionResult names the record the operation created or changed.
type ExecutionResult struct {
	Result ResourceRef
}

// ErrToolStale is returned by Execute when the target changed since the proposal was made.
var ErrToolStale = errors.New("ai: tool target changed")

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
	Mode() health.Mode
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
	// Proposal outcomes of Confirm and Dismiss.
	ErrProposalExpired      = errors.New("ai: proposal expired")
	ErrProposalHashMismatch = errors.New("ai: proposal parameters hash mismatch")
	ErrProposalNotPending   = errors.New("ai: proposal is not awaiting confirmation")
	ErrProposalInProgress   = errors.New("ai: proposal execution in progress")
	// ErrExecutionUnknown: the module call did not report an outcome; the proposal stays confirmed and the User may
	// confirm again once the lease expired (the call is idempotent), at most three attempts.
	ErrExecutionUnknown = errors.New("ai: proposal execution outcome unknown")
)

// InvalidInputError is a client-correctable validation failure.
type InvalidInputError struct{ Message string }

func (e *InvalidInputError) Error() string { return e.Message }

func invalid(format string, a ...any) error {
	return &InvalidInputError{Message: fmt.Sprintf(format, a...)}
}
