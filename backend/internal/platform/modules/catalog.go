// Package modules is the platform module registry (ADR-0032): which Turaco modules exist, which of them an
// administrator may switch on and off at runtime, their dependencies, and the gates that make a switched-off
// module invisible (HTTP routes answer 404, background jobs skip). A switch never deletes data and never replaces a
// module's own preconditions (startup gates, privacy records, providers).
//
// The package imports no business module (make archcheck). The catalog below is plain data owned by the platform;
// the composition root adds the precondition checks that need module services.
package modules

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Categories group modules in the overview. The UI translates "modules.category.<category>".
const (
	CategoryCore            = "core"
	CategoryServiceMgmt     = "service_management"
	CategoryAssetsInventory = "assets_inventory"
	CategoryEndpoints       = "endpoints_security"
	CategoryInfrastructure  = "infrastructure_operations"
	CategoryWorkforce       = "workforce"
	CategoryInsight         = "insight"
)

// Module describes one module of the installation.
type Module struct {
	// Key is the stable identifier (API, audit, database). Lower case letters, digits and underscore.
	Key string
	// Category groups the module in the overview.
	Category string
	// Core modules are always on and cannot be switched.
	Core bool
	// DefaultEnabled is the state of an optional module that has no stored switch yet.
	DefaultEnabled bool
	// Requires lists optional modules this module cannot work without. A module can only be enabled while all of
	// them are enabled, and a module can only be disabled while no enabled module requires it.
	Requires []string
	// RoutePrefixes are the first path segments below /api/v1/ that the module owns.
	RoutePrefixes []string
	// OpenPaths are paths that stay reachable while the module is off: status probes and the administration
	// routes needed to configure the module before it can be enabled (a trailing * matches that path and
	// everything below it). Authentication and the module's own admin permissions still apply.
	OpenPaths []string
	// AlwaysRunJobs are job types that keep running while the module is off because they are safety or data
	// protection paths: retention clean-up, session expiry, the Deployment kill-switch sweep. The job handler itself
	// must do only the safe part when the module is off.
	AlwaysRunJobs []string
	// AlwaysRunConsumers are outbox consumer names that keep running while the module is off (safety or cleanup
	// reactions). All other consumers "<key>.*" are deferred. None exists yet.
	AlwaysRunConsumers []string
	// DisposableJobs are recurring scheduled ticks that are simply dropped while the module is off; the schedule
	// enqueues the next one. Every other job type "<key>.*" is durable work: it stays pending until the module is
	// enabled again.
	DisposableJobs []string
	// StartupGates names the environment variables that gate the module at startup. They are read-only
	// information in the overview; the switch never overrides them.
	StartupGates []string
}

// NameKey is the i18n resource key of the module's display name.
func (m Module) NameKey() string { return "modules." + m.Key + ".name" }

// DescriptionKey is the i18n resource key of the module's short description.
func (m Module) DescriptionKey() string { return "modules." + m.Key + ".description" }

func opt(key, category string, requires []string, prefixes ...string) Module {
	return Module{Key: key, Category: category, DefaultEnabled: true, Requires: requires, RoutePrefixes: prefixes}
}

func core(key string, prefixes ...string) Module {
	return Module{Key: key, Category: CategoryCore, Core: true, DefaultEnabled: true, RoutePrefixes: prefixes}
}

// Catalog returns every module in display order. Dependencies follow the synchronous public contracts that
// internal/wiring connects; they are deliberately coarse and may be relaxed by a later ADR when a dependency
// becomes optional.
func Catalog() []Module {
	return []Module{
		core("platform", "meta"),
		core("access", "auth", "roles", "role-assignments", "role-templates", "permissions", "modules", "access"),
		core("organization", "users", "teams", "locations", "departments", "directory-groups", "directory-sync-runs", "import-batches"),
		core("audit", "audit-events"),
		core("tasks", "tasks", "recurring-task-definitions", "my-work"),
		core("approvals", "approvals"),
		core("notifications", "notifications"),

		opt("servicedesk", CategoryServiceMgmt, []string{"assets"}, "tickets", "major-incidents", "problems", "service-desk"),
		opt("knowledge", CategoryServiceMgmt, []string{"servicedesk"}, "knowledge-articles", "runbooks", "runbook-executions"),
		opt("catalog", CategoryServiceMgmt, []string{"products"}, "catalog-items"),
		opt("requests", CategoryServiceMgmt, []string{"catalog"}, "service-requests"),

		opt("products", CategoryAssetsInventory, nil, "products", "product-categories", "manufacturers"),
		opt("assets", CategoryAssetsInventory, []string{"products"}, "assets", "my-assets"),
		opt("procurement", CategoryAssetsInventory, []string{"products"}, "procurement-requests", "purchase-orders", "suppliers"),
		opt("inventory", CategoryAssetsInventory, []string{"assets", "procurement", "products"},
			"warehouses", "storage-locations", "stock", "reservations", "goods-receipts", "inventory-transactions"),

		withGates(opt("endpoints", CategoryEndpoints, []string{"assets", "changes"},
			"devices", "deployments", "target-sets", "software", "management-artifacts", "management-filters", "endpoint-findings", "endpoint-sync"),
			"SOFTWARE_PROVIDER_SYNC", "SOFTWARE_DEPLOY_WRITE").jobs(
			[]string{"endpoints.deployment_tick"}, []string{"endpoints.software_package_sync", "endpoints.deployment_correlation"}),
		opt("security", CategoryEndpoints, []string{"endpoints", "changes"}, "security").jobs(
			nil, []string{"security.advisory_sync", "security.match_all", "security.risk_review_reminders"}),
		withGates(opt("remoteaccess", CategoryEndpoints, []string{"endpoints", "assets", "servicedesk"}, "remote-access"), "REMOTE_ACCESS_PROVIDERS").jobs(
			[]string{"remoteaccess.expire_sessions"}, []string{"remoteaccess.observe"}),

		opt("infrastructure", CategoryInfrastructure, []string{"assets"}, "buildings", "rooms", "racks", "rack-placements", "virtual-machines", "infrastructure"),
		opt("services", CategoryInfrastructure, []string{"assets", "infrastructure"}, "services", "impact"),
		opt("changes", CategoryInfrastructure, []string{"assets", "infrastructure", "services"}, "changes").jobs(nil, []string{"changes.reminders"}),
		opt("planning", CategoryInfrastructure, []string{"changes", "procurement", "services"}, "initiatives", "maintenance-calendar"),

		{Key: "presence", Category: CategoryWorkforce, DefaultEnabled: false, RoutePrefixes: []string{"presence"},
			OpenPaths: []string{"/api/v1/presence/status", "/api/v1/presence/settings*"}, AlwaysRunJobs: []string{"presence.purge"}, StartupGates: []string{"PRESENCE_ENABLED"}},

		opt("briefing", CategoryInsight, nil, "briefing", "briefing-items"),
		{Key: "ai", Category: CategoryInsight, DefaultEnabled: false, RoutePrefixes: []string{"ai"},
			OpenPaths: []string{"/api/v1/ai/status", "/api/v1/ai/settings*", "/api/v1/ai/providers*", "/api/v1/ai/usage*"}, AlwaysRunJobs: []string{"ai.sessions.expire", "ai.retention.purge"}, StartupGates: []string{"AI_ENABLED"}},
	}
}

func (m Module) jobs(always, disposable []string) Module {
	m.AlwaysRunJobs, m.DisposableJobs = always, disposable
	return m
}

func withGates(m Module, gates ...string) Module {
	m.StartupGates = gates
	return m
}

// Index is the validated catalog with lookups.
type Index struct {
	list       []Module
	byKey      map[string]Module
	byPrefix   map[string]string
	open       map[string]string
	dependents map[string][]string
}

// NewIndex validates a catalog: unique keys and route prefixes, known and acyclic dependencies, core modules
// without dependencies, no optional module required by nothing but itself.
func NewIndex(list []Module) (*Index, error) {
	ix := &Index{list: append([]Module(nil), list...), byKey: map[string]Module{}, byPrefix: map[string]string{},
		open: map[string]string{}, dependents: map[string][]string{}}
	for _, m := range ix.list {
		if m.Key == "" || strings.ToLower(m.Key) != m.Key || strings.ContainsAny(m.Key, " -.") {
			return nil, fmt.Errorf("modules: invalid key %q", m.Key)
		}
		if _, dup := ix.byKey[m.Key]; dup {
			return nil, fmt.Errorf("modules: duplicate key %q", m.Key)
		}
		if m.Core && (len(m.Requires) > 0 || !m.DefaultEnabled || len(m.OpenPaths) > 0) {
			return nil, fmt.Errorf("modules: core module %q must be on by default and have no dependencies", m.Key)
		}
		ix.byKey[m.Key] = m
		for _, p := range m.RoutePrefixes {
			if owner, dup := ix.byPrefix[p]; dup {
				return nil, fmt.Errorf("modules: route prefix %q claimed by %q and %q", p, owner, m.Key)
			}
			ix.byPrefix[p] = m.Key
		}
		for _, p := range m.OpenPaths {
			ix.open[p] = m.Key
		}
	}
	for _, m := range ix.list {
		for _, r := range m.Requires {
			dep, ok := ix.byKey[r]
			if !ok {
				return nil, fmt.Errorf("modules: %q requires unknown module %q", m.Key, r)
			}
			if dep.Core {
				return nil, fmt.Errorf("modules: %q lists core module %q as a dependency; core modules are always on", m.Key, r)
			}
			ix.dependents[r] = append(ix.dependents[r], m.Key)
		}
	}
	for _, m := range ix.list {
		if err := ix.checkAcyclic(m.Key, map[string]bool{}); err != nil {
			return nil, err
		}
	}
	return ix, nil
}

func (ix *Index) checkAcyclic(key string, path map[string]bool) error {
	if path[key] {
		return fmt.Errorf("modules: dependency cycle through %q", key)
	}
	path[key] = true
	defer delete(path, key)
	for _, r := range ix.byKey[key].Requires {
		if err := ix.checkAcyclic(r, path); err != nil {
			return err
		}
	}
	return nil
}

// DefaultIndex is the validated built-in catalog. A broken catalog is a programming error caught by tests.
func DefaultIndex() *Index {
	ix, err := NewIndex(Catalog())
	if err != nil {
		panic(err)
	}
	return ix
}

// All returns the modules in display order.
func (ix *Index) All() []Module { return append([]Module(nil), ix.list...) }

// Get returns a module by key.
func (ix *Index) Get(key string) (Module, bool) { m, ok := ix.byKey[key]; return m, ok }

// RequiredBy returns the keys of the modules that require key, sorted.
func (ix *Index) RequiredBy(key string) []string {
	out := append([]string(nil), ix.dependents[key]...)
	sort.Strings(out)
	return out
}

const apiPrefix = "/api/v1/"

// ForPath returns the optional module that owns an API path. Core modules and unknown paths return ok=false.
// Open (status probe) paths return ok=false as well.
func (ix *Index) ForPath(path string) (key string, ok bool) {
	if !strings.HasPrefix(path, apiPrefix) {
		return "", false
	}
	if ix.isOpen(strings.TrimRight(path, "/")) {
		return "", false
	}
	seg := strings.TrimPrefix(path, apiPrefix)
	if i := strings.IndexByte(seg, '/'); i >= 0 {
		seg = seg[:i]
	}
	owner, found := ix.byPrefix[seg]
	if !found || ix.byKey[owner].Core {
		return "", false
	}
	return owner, true
}

// JobPolicy says what happens to a claimed job while its module is off.
type JobPolicy int

const (
	// JobAlways: the job runs (core jobs, safety and retention paths).
	JobAlways JobPolicy = iota
	// JobDrop: a disposable scheduled tick is completed without running.
	JobDrop
	// JobDefer: durable work stays pending until the module is enabled again.
	JobDefer
)

// ForJob returns the optional module that owns a background job type ("<module key>.<name>") and the policy that
// applies while it is off. Core jobs and always-run jobs return ok=false.
func (ix *Index) ForJob(jobType string) (key string, policy JobPolicy, ok bool) {
	prefix, _, found := strings.Cut(jobType, ".")
	if !found {
		return "", JobAlways, false
	}
	m, known := ix.byKey[prefix]
	if !known || m.Core || slices.Contains(m.AlwaysRunJobs, jobType) {
		return "", JobAlways, false
	}
	if slices.Contains(m.DisposableJobs, jobType) {
		return m.Key, JobDrop, true
	}
	return m.Key, JobDefer, true
}

// ForConsumer returns the optional module that owns an outbox consumer ("<module key>.<name>"). Core consumers and
// always-run consumers return ok=false.
func (ix *Index) ForConsumer(name string) (key string, ok bool) {
	prefix, _, found := strings.Cut(name, ".")
	if !found {
		return "", false
	}
	m, known := ix.byKey[prefix]
	if !known || m.Core || slices.Contains(m.AlwaysRunConsumers, name) {
		return "", false
	}
	return m.Key, true
}

func (ix *Index) isOpen(path string) bool {
	if _, ok := ix.open[path]; ok {
		return true
	}
	for p := range ix.open {
		if base, wild := strings.CutSuffix(p, "*"); wild && (path == base || strings.HasPrefix(path, base+"/")) {
			return true
		}
	}
	return false
}
