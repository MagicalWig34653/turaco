// Package softwaremgmt is the adapter boundary to Software Management Providers (ADR-0027,
// docs/product/f9-software-lifecycle-design.md decision P1, docs/integrations/intuneget.md).
//
// A Software Management Provider (IntuneGet is the intended first one) searches a software catalog,
// packages a Software Version and publishes the package into a Management Provider (Intune). It manages
// no devices. Only the internal side exists so far: the normalized records the Endpoints module consumes,
// the Provider contract, an in-memory Fake for tests and a placeholder that reports "not configured".
// Provider DTOs never cross this boundary.
//
// Every field a provider returns is untrusted input: the consumer validates lengths, characters, hashes and
// enumerations. Package and Publish are idempotent per operation key: a retry with the same key returns
// the result of the first call and never creates a second package or publication.
package softwaremgmt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// ProviderKey is the key under which packages of the intended first provider (IntuneGet) are stored.
const ProviderKey = "intuneget"

// ErrNotConfigured is returned while no provider client is implemented or configured.
var ErrNotConfigured = errors.New("softwaremgmt: no software management provider is configured (not implemented yet)")

// ErrNotFound means the provider does not know the package.
var ErrNotFound = errors.New("softwaremgmt: package not found")

// ErrHashDiffers means the provider refused a publication because the packaged installer hash differs from the
// expected one.
var ErrHashDiffers = errors.New("softwaremgmt: packaged installer hash differs from the expected hash")

// Package statuses as reported by a provider. Anything else is treated as failed by the consumer.
const (
	StatusBuilding  = "building"
	StatusPackaged  = "packaged"
	StatusPublished = "published"
	StatusFailed    = "failed"
)

// Statuses lists every package status a provider may report.
var Statuses = []string{StatusBuilding, StatusPackaged, StatusPublished, StatusFailed}

// CatalogEntry is one catalog hit. ProviderID is the provider's catalog id (for example a WinGet id).
type CatalogEntry struct {
	ProviderID    string
	Name          string
	Publisher     string
	LatestVersion string
	SourceURL     string
}

// PackageRequest asks the provider to package exactly the approved binding of a Software Version.
// ProductKey is Turaco's Software Product id; the provider echoes it back.
type PackageRequest struct {
	ProductKey      string
	ProductName     string
	Publisher       string
	Version         string
	InstallerURL    string
	InstallerSHA256 string
	InstallCommand  string
	DetectionRule   string
}

// ProviderTarget names the Management Provider a package is published into (for example "intune").
type ProviderTarget struct {
	ManagementProvider string
}

// PublishRequest asks the provider to publish one package. ExpectedInstallerSHA256 is the approved installer
// hash: the provider must refuse the publication when the installer it packaged has another hash, so a package
// changed between Turaco's check and the publication is never published.
type PublishRequest struct {
	ProviderPackageID       string
	Target                  ProviderTarget
	ExpectedInstallerSHA256 string
}

// PackageRecord is the provider's report about one package. InstallerSHA256 is the hash of the installer the
// provider actually packaged (lower-case hex), which Turaco compares with the approved hash.
// ManagementArtifactExternalID is the Management Provider's id of the published object (the Intune app),
// known once published. ProductKey, Version, Publisher, InstallCommandSHA256 and DetectionRuleSHA256 are
// optional: when reported, Turaco compares them with the approved binding.
type PackageRecord struct {
	ProviderPackageID            string
	ProductKey                   string
	Version                      string
	InstallerSHA256              string
	InstallerURL                 string
	Publisher                    string
	InstallCommandSHA256         string
	DetectionRuleSHA256          string
	Status                       string
	ManagementArtifactExternalID string
	ObservedAt                   time.Time
}

// Provider is the Software Management Provider port.
type Provider interface {
	// SearchCatalog returns at most a bounded number of catalog entries matching query.
	SearchCatalog(ctx context.Context, query string) ([]CatalogEntry, error)
	// Package packages a Software Version. It is idempotent per opKey.
	Package(ctx context.Context, req PackageRequest, opKey string) (PackageRecord, error)
	// Publish publishes or updates a package into a Management Provider. It is idempotent per opKey and refuses
	// when the packaged installer hash differs from req.ExpectedInstallerSHA256.
	Publish(ctx context.Context, req PublishRequest, opKey string) (PackageRecord, error)
	// PackageStatus reports the current state of the given packages; unknown ids are left out.
	PackageStatus(ctx context.Context, providerPackageIDs []string) ([]PackageRecord, error)
}

// NotConfigured is the placeholder provider: every call fails with ErrNotConfigured.
type NotConfigured struct{}

func (NotConfigured) SearchCatalog(context.Context, string) ([]CatalogEntry, error) {
	return nil, ErrNotConfigured
}

func (NotConfigured) Package(context.Context, PackageRequest, string) (PackageRecord, error) {
	return PackageRecord{}, ErrNotConfigured
}

func (NotConfigured) Publish(context.Context, PublishRequest, string) (PackageRecord, error) {
	return PackageRecord{}, ErrNotConfigured
}

func (NotConfigured) PackageStatus(context.Context, []string) ([]PackageRecord, error) {
	return nil, ErrNotConfigured
}

// MaxCatalogResults bounds a catalog search of the Fake.
const MaxCatalogResults = 50

// Fake is a deterministic in-memory provider for tests. Package ids are pkg-1, pkg-2, ... (after an optional
// prefix); the published artifact of a package is "app-" plus its id unless overridden.
type Fake struct {
	mu        sync.Mutex
	catalog   []CatalogEntry
	packages  map[string]PackageRecord
	order     []string
	packageOp map[string]string
	publishOp map[string]bool
	calls     map[string]int
	failAll   error
	failOp    map[string]error
	hashOver  map[string]string
	artifact  map[string]string
	binding   map[string]func(*PackageRecord)
	beforePub func()
	initial   string
	prefix    string
	now       func() time.Time
}

// NewFake creates an empty Fake whose packages start as packaged.
func NewFake() *Fake {
	return &Fake{packages: map[string]PackageRecord{}, packageOp: map[string]string{}, publishOp: map[string]bool{},
		calls: map[string]int{}, failOp: map[string]error{}, hashOver: map[string]string{}, artifact: map[string]string{},
		binding: map[string]func(*PackageRecord){},
		initial: StatusPackaged, now: func() time.Time { return time.Now().UTC() }}
}

// SetCatalog replaces the catalog.
func (f *Fake) SetCatalog(entries ...CatalogEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.catalog = append([]CatalogEntry(nil), entries...)
}

// SetIDPrefix prefixes the package ids the Fake hands out (tests that share a database).
func (f *Fake) SetIDPrefix(prefix string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prefix = prefix
}

// SetInitialStatus sets the status a new package starts with (default packaged).
func (f *Fake) SetInitialStatus(status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.initial = status
}

// FailWith makes every following call fail with err; nil restores normal behavior.
func (f *Fake) FailWith(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAll = err
}

// FailOperation makes one operation (search, package, publish, status) fail with err; nil restores it.
func (f *Fake) FailOperation(op string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.failOp, op)
		return
	}
	f.failOp[op] = err
}

// ReportHash makes the provider report hash as the installer hash of the package (now and later), as if the
// provider had packaged a different installer.
func (f *Fake) ReportHash(providerPackageID, hash string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hashOver[providerPackageID] = hash
	if p, ok := f.packages[providerPackageID]; ok {
		p.InstallerSHA256 = hash
		p.ObservedAt = f.now()
		f.packages[providerPackageID] = p
	}
}

// SetStatus changes the reported status of a package.
func (f *Fake) SetStatus(providerPackageID, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.packages[providerPackageID]; ok {
		p.Status = status
		p.ObservedAt = f.now()
		f.packages[providerPackageID] = p
	}
}

// SetObservedAt sets the observation time the provider reports for a package (out-of-order reports).
func (f *Fake) SetObservedAt(providerPackageID string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.packages[providerPackageID]; ok {
		p.ObservedAt = at
		f.packages[providerPackageID] = p
	}
}

// ReportBinding changes the bound values the provider reports for a package (now and in later reports).
func (f *Fake) ReportBinding(providerPackageID string, change func(*PackageRecord)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.binding[providerPackageID] = change
	if p, ok := f.packages[providerPackageID]; ok {
		change(&p)
		f.packages[providerPackageID] = p
	}
}

// BeforePublish runs hook (without the Fake's lock) at the start of every Publish call, to simulate something
// happening while a publication is in flight; nil removes it.
func (f *Fake) BeforePublish(hook func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.beforePub = hook
}

// SetArtifact sets the Management Artifact external id a publication of the package yields.
func (f *Fake) SetArtifact(providerPackageID, externalID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.artifact[providerPackageID] = externalID
}

// Calls reports how often an operation was called (including failed and idempotent calls).
func (f *Fake) Calls(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[op]
}

// Packages reports how many distinct packages exist.
func (f *Fake) Packages() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.packages)
}

func (f *Fake) begin(op string) error {
	f.calls[op]++
	if f.failAll != nil {
		return f.failAll
	}
	return f.failOp[op]
}

func (f *Fake) SearchCatalog(_ context.Context, query string) ([]CatalogEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("search"); err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	out := []CatalogEntry{}
	for _, e := range f.catalog {
		if q == "" || strings.Contains(strings.ToLower(e.Name), q) || strings.Contains(strings.ToLower(e.ProviderID), q) {
			out = append(out, e)
			if len(out) == MaxCatalogResults {
				break
			}
		}
	}
	return out, nil
}

func (f *Fake) Package(_ context.Context, req PackageRequest, opKey string) (PackageRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("package"); err != nil {
		return PackageRecord{}, err
	}
	if opKey == "" {
		return PackageRecord{}, errors.New("softwaremgmt: operation key is required")
	}
	if id, ok := f.packageOp[opKey]; ok {
		return f.packages[id], nil
	}
	id := fmt.Sprintf("%spkg-%d", f.prefix, len(f.order)+1)
	hash := req.InstallerSHA256
	if h, ok := f.hashOver[id]; ok {
		hash = h
	}
	rec := PackageRecord{ProviderPackageID: id, ProductKey: req.ProductKey, Version: req.Version, InstallerSHA256: hash,
		InstallerURL: req.InstallerURL, Publisher: req.Publisher, InstallCommandSHA256: sha256Hex(req.InstallCommand),
		DetectionRuleSHA256: sha256Hex(req.DetectionRule), Status: f.initial, ObservedAt: f.now()}
	if change, ok := f.binding[id]; ok {
		change(&rec)
	}
	f.packages[id] = rec
	f.order = append(f.order, id)
	f.packageOp[opKey] = id
	return rec, nil
}

func (f *Fake) Publish(_ context.Context, req PublishRequest, opKey string) (PackageRecord, error) {
	f.mu.Lock()
	hook := f.beforePub
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("publish"); err != nil {
		return PackageRecord{}, err
	}
	providerPackageID := req.ProviderPackageID
	if opKey == "" || req.Target.ManagementProvider == "" || req.ExpectedInstallerSHA256 == "" {
		return PackageRecord{}, errors.New("softwaremgmt: operation key, target and expected hash are required")
	}
	p, ok := f.packages[providerPackageID]
	if !ok {
		return PackageRecord{}, ErrNotFound
	}
	if f.publishOp[opKey] {
		return p, nil
	}
	if p.InstallerSHA256 != req.ExpectedInstallerSHA256 {
		return PackageRecord{}, ErrHashDiffers
	}
	if p.Status != StatusPackaged && p.Status != StatusPublished {
		return PackageRecord{}, fmt.Errorf("softwaremgmt: package %s is %s and cannot be published", providerPackageID, p.Status)
	}
	art := f.artifact[providerPackageID]
	if art == "" {
		art = "app-" + providerPackageID
	}
	p.Status, p.ManagementArtifactExternalID, p.ObservedAt = StatusPublished, art, f.now()
	f.packages[providerPackageID] = p
	f.publishOp[opKey] = true
	return p, nil
}

func (f *Fake) PackageStatus(_ context.Context, ids []string) ([]PackageRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("status"); err != nil {
		return nil, err
	}
	out := []PackageRecord{}
	for _, id := range f.order {
		if slices.Contains(ids, id) {
			out = append(out, f.packages[id])
		}
	}
	return out, nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
