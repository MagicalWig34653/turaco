package remoteaccess

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Fake is an in-memory provider for tests: it counts calls, injects failures and replays scripted observed
// sessions. It validates peer ids like the RustDesk connector and builds links of a fixed test template.
type Fake struct {
	mu          sync.Mutex
	key         string
	BuildCalls  int
	SessionCall int
	CloseCalls  []string
	FailBuild   error
	FailSession error
	Observed    []ObservedSession
	PeerList    []PeerRef
}

// NewFake creates a Fake under a provider key.
func NewFake(key string) *Fake { return &Fake{key: key} }

func (f *Fake) Key() string { return f.key }
func (f *Fake) Capabilities() Capabilities {
	return Capabilities{Attended: true, ListPeers: true, ObserveSessions: true, CloseSessions: true}
}

// BuildLaunch builds fake://<key>/<id> for a valid peer id.
func (f *Fake) BuildLaunch(peer PeerRef, mode Mode) (LaunchURI, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.BuildCalls++
	if f.FailBuild != nil {
		return LaunchURI{}, f.FailBuild
	}
	if mode != ModeAttended {
		return LaunchURI{}, ErrUnsupportedMode
	}
	if !numericID.MatchString(peer.ID) && !rustdeskCustomID.MatchString(peer.ID) {
		return LaunchURI{}, ErrInvalidPeerID
	}
	return LaunchURI{value: "fake://" + f.key + "/" + peer.ID}, nil
}

func (f *Fake) Peers(context.Context) ([]PeerRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]PeerRef(nil), f.PeerList...), nil
}

// Sessions returns the scripted sessions that started at or after since.
func (f *Fake) Sessions(_ context.Context, since time.Time) ([]ObservedSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SessionCall++
	if f.FailSession != nil {
		return nil, f.FailSession
	}
	var out []ObservedSession
	for _, s := range f.Observed {
		if !s.StartedAt.Before(since) {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *Fake) Close(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CloseCalls = append(f.CloseCalls, id)
	if id == "" {
		return errors.New("empty provider session id")
	}
	return nil
}
