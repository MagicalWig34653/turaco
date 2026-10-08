package ai

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/permissions"
)

// ProhibitedPathFragments are name fragments no tool output path may contain (A13). They cover secrets and
// credentials, Audit records, Workforce Presence details and Remote Access session data. The registry rejects a
// tool whose declared output contains one, and a test enumerates every registered tool against this list.
var ProhibitedPathFragments = []string{
	"secret", "password", "passwd", "credential", "token", "apikey", "api_key", "privatekey", "private_key",
	"audit", "presence", "remoteaccess", "remote_access", "peer", "logon", "session", "hash", "rawpayload", "raw_payload",
}

var (
	toolNameRE  = regexp.MustCompile(`^[a-z][a-z0-9]*\.[a-z][a-z0-9_]*$`)
	fieldPathRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9]*(\[\])?(\.[a-zA-Z][a-zA-Z0-9]*(\[\])?)*$`)
	targetRE    = regexp.MustCompile(`^[a-z][a-z_]{1,30}$`)
)

type registered struct {
	tool   Tool
	schema *schemaNode
	allow  outputAllowlist
}

// Registry holds the AI Tools. Modules register at startup through the composition root, like the permission and
// notification registries; there is no other way to add a tool.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]registered
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{tools: map[string]registered{}} }

// Register runs the registry self-check and adds the tool. It never panics; callers fail startup on error.
func (r *Registry) Register(t Tool) error {
	if !toolNameRE.MatchString(t.Name) {
		return fmt.Errorf("ai tool %q: name must be module.operation in lower case", t.Name)
	}
	if t.Description == "" || len(t.Description) > 600 {
		return fmt.Errorf("ai tool %s: description is required (at most 600 characters)", t.Name)
	}
	if t.Handler == nil {
		return fmt.Errorf("ai tool %s: handler is required", t.Name)
	}
	switch t.Risk {
	case RiskRead:
	case RiskWrite, RiskHighImpact:
		// Write tools only create a Proposal (A-C); high_impact tools may never execute. Neither exists in A-A.
		return fmt.Errorf("ai tool %s: risk %q is not available (only read tools in this release)", t.Name, t.Risk)
	default:
		return fmt.Errorf("ai tool %s: unknown risk %q", t.Name, t.Risk)
	}
	if !permissionRegistered(t.Permission) {
		return fmt.Errorf("ai tool %s: permission %q is not registered", t.Name, t.Permission)
	}
	sch, err := compileSchema(t.InputSchema)
	if err != nil {
		return fmt.Errorf("ai tool %s: %w", t.Name, err)
	}
	if len(t.Output) == 0 {
		return fmt.Errorf("ai tool %s: output fields are required", t.Name)
	}
	allow := outputAllowlist{}
	for _, f := range t.Output {
		if !fieldPathRE.MatchString(f.Path) {
			return fmt.Errorf("ai tool %s: invalid output path %q", t.Name, f.Path)
		}
		if !f.Class.Valid() {
			return fmt.Errorf("ai tool %s: output %q has unknown data class %q", t.Name, f.Path, f.Class)
		}
		if frag := ProhibitedFragment(f.Path); frag != "" {
			return fmt.Errorf("ai tool %s: output %q contains prohibited name %q", t.Name, f.Path, frag)
		}
		if _, dup := allow[f.Path]; dup {
			return fmt.Errorf("ai tool %s: duplicate output path %q", t.Name, f.Path)
		}
		allow[f.Path] = f
	}
	if t.Target != nil {
		p, ok := sch.Properties[t.Target.Arg]
		if !targetRE.MatchString(t.Target.Type) || !ok || p.Type != "string" || p.Format != "uuid" {
			return fmt.Errorf("ai tool %s: target needs a type and a uuid string argument", t.Name)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.tools[t.Name]; dup {
		return fmt.Errorf("ai tool %s: already registered", t.Name)
	}
	r.tools[t.Name] = registered{tool: t, schema: sch, allow: allow}
	return nil
}

// RegisterAll registers tools in order and stops at the first error.
func (r *Registry) RegisterAll(tools ...Tool) error {
	for _, t := range tools {
		if err := r.Register(t); err != nil {
			return err
		}
	}
	return nil
}

// ProhibitedFragment returns the prohibited name fragment contained in a path, or "".
func ProhibitedFragment(path string) string {
	l := strings.ToLower(path)
	for _, f := range ProhibitedPathFragments {
		if strings.Contains(l, f) {
			return f
		}
	}
	return ""
}

func permissionRegistered(name string) bool {
	for _, p := range permissions.Registry {
		if p.Name == name {
			return true
		}
	}
	return false
}

// Tools returns all registered tools sorted by name.
func (r *Registry) Tools() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t.tool)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// TargetTypes returns the resource types tools can read (the types a conversation scope may hold).
func (r *Registry) TargetTypes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []string
	for _, t := range r.tools {
		if t.tool.Target != nil && !slices.Contains(out, t.tool.Target.Type) {
			out = append(out, t.tool.Target.Type)
		}
	}
	slices.Sort(out)
	return out
}

// Offered returns the tools a conversation gets (A5, A7): the caller holds the tool's permission and the provider
// is allowed every data class the tool's output fields declare. The set is computed on the server from the
// session's permissions; the model cannot widen it.
func (r *Registry) Offered(c Caller, p ProviderRecord) map[string]registered {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[string]registered{}
	for name, t := range r.tools {
		if !c.Has(t.tool.Permission) {
			continue
		}
		eligible := true
		for _, cl := range t.tool.Classes() {
			if !p.Allows(cl) {
				eligible = false
				break
			}
		}
		if eligible {
			out[name] = t
		}
	}
	return out
}

// CheckOutput runs a tool's egress allowlist (A13) over a DTO exactly as the runtime does. Module tests use it to
// prove their DTO carries only declared fields.
func CheckOutput(t Tool, dto any) (json.RawMessage, []DataClass, error) {
	allow := outputAllowlist{}
	for _, f := range t.Output {
		allow[f.Path] = f
	}
	out, classes, _, err := filterOutput(dto, allow, maxToolResultBytes)
	return out, classes, err
}
