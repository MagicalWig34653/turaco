// Package remoteaccess is the adapter boundary to Remote Access Providers (ADR-0026,
// docs/product/f10-remote-access-design.md decisions R2/R3).
//
// The port models what Turaco needs, not a plugin framework: a provider builds the launch link that opens its
// client on the technician's machine for a validated peer id (Provider), and optionally lists peers (PeerLister),
// reports session records (SessionObserver) and ends a session (SessionCloser). There is no "execute command"
// operation. Launch links carry no secrets and are built only from a validated peer id and a fixed per-provider
// template; peer ids are untrusted input. Provider DTOs never cross this boundary.
package remoteaccess

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Mode is the session mode. Only attended exists (F10 decision R1).
type Mode string

const ModeAttended Mode = "attended"

// Errors of the port.
var (
	// ErrNotConfigured is returned while no provider client is configured.
	ErrNotConfigured = errors.New("remoteaccess: no remote access provider is configured")
	// ErrInvalidPeerID means the peer id does not match the provider's id format.
	ErrInvalidPeerID = errors.New("remoteaccess: invalid peer id")
	// ErrUnsupportedMode means the provider does not offer the mode.
	ErrUnsupportedMode = errors.New("remoteaccess: unsupported mode")
	// ErrUnsupported means the provider does not implement an optional capability.
	ErrUnsupported = errors.New("remoteaccess: operation not supported by the provider")
	// ErrUnknownProvider is returned for a provider key that has no connector.
	ErrUnknownProvider = errors.New("remoteaccess: unknown provider")
)

// Capabilities declare what a provider connector supports.
type Capabilities struct {
	Attended bool
	// ListPeers, ObserveSessions and CloseSessions mirror the optional interfaces.
	ListPeers       bool
	ObserveSessions bool
	CloseSessions   bool
}

// PeerRef is the provider's identity of a Device (untrusted until validated by the connector).
type PeerRef struct {
	Provider string
	ID       string
}

// LaunchURI is the link that opens the provider client. It is never persisted, logged or audited, so its
// formatting verbs print a placeholder; only Reveal returns the value.
type LaunchURI struct{ value string }

// Reveal returns the link. Call it only to put it in the one response that hands it to the technician.
func (u LaunchURI) Reveal() string { return u.value }

func (u LaunchURI) String() string               { return "[launch-uri redacted]" }
func (u LaunchURI) GoString() string             { return "remoteaccess.LaunchURI{redacted}" }
func (u LaunchURI) MarshalText() ([]byte, error) { return []byte("[launch-uri redacted]"), nil }

// Provider is the launch port every connector implements.
type Provider interface {
	Key() string
	Capabilities() Capabilities
	// BuildLaunch builds the launch link from the validated peer id and the fixed template of the provider.
	BuildLaunch(peer PeerRef, mode Mode) (LaunchURI, error)
}

// PeerLister is implemented by providers with a device list API.
type PeerLister interface {
	Peers(ctx context.Context) ([]PeerRef, error)
}

// ObservedSession is a provider's record of a connection, with its source and the time it was read.
type ObservedSession struct {
	ProviderSessionID string
	PeerID            string
	StartedAt         time.Time
	EndedAt           *time.Time
	// Operator is the operator identity as the provider reports it (bounded plain text; may be empty).
	Operator   string
	Source     string
	ObservedAt time.Time
}

// SessionObserver is implemented by providers with a session history API.
type SessionObserver interface {
	Sessions(ctx context.Context, since time.Time) ([]ObservedSession, error)
}

// SessionCloser is implemented by providers that can end a session.
type SessionCloser interface {
	Close(ctx context.Context, providerSessionID string) error
}

// launchSpec is one row of the launch table: strict peer id validation and the fixed link template.
type launchSpec struct {
	valid    func(string) bool
	template string // %s is the validated peer id
}

// NotConfigured is the provider used when a key has no usable client; every operation reports it.
type NotConfigured struct{ ProviderKey string }

func (n NotConfigured) Key() string                { return n.ProviderKey }
func (n NotConfigured) Capabilities() Capabilities { return Capabilities{} }
func (n NotConfigured) BuildLaunch(PeerRef, Mode) (LaunchURI, error) {
	return LaunchURI{}, ErrNotConfigured
}

// Registry maps the enabled provider keys to their connectors. An empty registry means the feature is off.
type Registry struct{ byKey map[string]Provider }

// NewRegistry builds the connectors for the keys of REMOTE_ACCESS_PROVIDERS. Unknown or duplicate keys are errors.
func NewRegistry(keys []string) (*Registry, error) {
	r := &Registry{byKey: map[string]Provider{}}
	for _, k := range keys {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}
		if _, dup := r.byKey[k]; dup {
			return nil, fmt.Errorf("remoteaccess: provider %q is listed twice", k)
		}
		p, err := NewConnector(k)
		if err != nil {
			return nil, err
		}
		r.byKey[k] = p
	}
	return r, nil
}

// NewRegistryOf builds a registry from ready providers (tests, the Fake).
func NewRegistryOf(ps ...Provider) *Registry {
	r := &Registry{byKey: map[string]Provider{}}
	for _, p := range ps {
		r.byKey[p.Key()] = p
	}
	return r
}

// Get returns the connector of an enabled provider.
func (r *Registry) Get(key string) (Provider, bool) {
	if r == nil {
		return nil, false
	}
	p, ok := r.byKey[key]
	return p, ok
}

// Keys lists the enabled provider keys, sorted.
func (r *Registry) Keys() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.byKey))
	for k := range r.byKey {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
