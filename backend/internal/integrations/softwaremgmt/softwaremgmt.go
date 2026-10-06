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

// PackageRecord is the provider's report about one package. InstallerSHA256 is the hash of the installer the
// provider actually packaged (lower-case hex), which Turaco compares with the approved hash.
// ManagementArtifactExternalID is the Management Provider's id of the published object (the Intune app),
// known once published.
type PackageRecord struct {
	ProviderPackageID            string
	ProductKey                   string
	Version                      string
	InstallerSHA256              string
	InstallerURL                 string
	Publisher                    string
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
	// Publish publishes or updates a package into a Management Provider. It is idempotent per opKey.
	Publish(ctx context.Context, providerPackageID string, target ProviderTarget, opKey string) (PackageRecord, error)
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

func (NotConfigured) Publish(context.Context, string, ProviderTarget, string) (PackageRecord, error) {
	return PackageRecord{}, ErrNotConfigured
}

func (NotConfigured) PackageStatus(context.Context, []string) ([]PackageRecord, error) {
	return nil, ErrNotConfigured
}

// MaxCatalogResults bounds a catalog search of the Fake.
const MaxCatalogResults = 50

// Fake is a deterministic in-memory provider for tests. Package ids are pkg-1, pkg-2, ...; the published
// artifact of pkg-N is app-pkg-N unless overridden.
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
	initial   string
	now       func() time.Time
}

// NewFake creates an empty Fake whose packages start as packaged.
func NewFake() *Fake {
	return &Fake{packages: map[string]PackageRecord{}, packageOp: map[string]string{}, publishOp: map[string]bool{},
		calls: map[string]int{}, failOp: map[string]error{}, hashOver: map[string]string{}, artifact: map[string]string{},
		initial: StatusPackaged, now: func() time.Time { return time.Now().UTC() }}
}

// SetCatalog replaces the catalog.
func (f *Fake) SetCatalog(entries ...CatalogEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.catalog = append([]CatalogEntry(nil), entries...)
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
	id := fmt.Sprintf("pkg-%d", len(f.order)+1)
	hash := req.InstallerSHA256
	if h, ok := f.hashOver[id]; ok {
		hash = h
	}
	rec := PackageRecord{ProviderPackageID: id, ProductKey: req.ProductKey, Version: req.Version, InstallerSHA256: hash,
		InstallerURL: req.InstallerURL, Publisher: req.Publisher, Status: f.initial, ObservedAt: f.now()}
	f.packages[id] = rec
	f.order = append(f.order, id)
	f.packageOp[opKey] = id
	return rec, nil
}

func (f *Fake) Publish(_ context.Context, providerPackageID string, target ProviderTarget, opKey string) (PackageRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("publish"); err != nil {
		return PackageRecord{}, err
	}
	if opKey == "" || target.ManagementProvider == "" {
		return PackageRecord{}, errors.New("softwaremgmt: operation key and target are required")
	}
	p, ok := f.packages[providerPackageID]
	if !ok {
		return PackageRecord{}, ErrNotFound
	}
	if f.publishOp[opKey] {
		return p, nil
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
