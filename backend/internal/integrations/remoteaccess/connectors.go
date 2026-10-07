package remoteaccess

import (
	"fmt"
	"regexp"
)

// Provider keys of the connectors.
const (
	KeyRustDesk  = "rustdesk"
	KeyAnyDesk   = "anydesk"
	KeyHopToDesk = "hoptodesk"
)

// Peer id validators. They accept ASCII only and no whitespace, quotes, slashes, colons, query characters or shell
// metacharacters, so a validated id can be placed into the fixed templates below without escaping.
var (
	numericID = regexp.MustCompile(`^[0-9]{9,10}$`)
	// RustDesk: numeric ids, or custom ids of an own server (letters, digits, underscore, hyphen).
	rustdeskCustomID = regexp.MustCompile(`^[A-Za-z0-9_-]{6,32}$`)
	// AnyDesk: numeric ids, or alias@namespace.
	anydeskAliasID = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,32}@[A-Za-z0-9_-]{2,32}$`)
	// HopToDesk: letters and digits.
	hoptodeskID = regexp.MustCompile(`^[A-Za-z0-9]{6,32}$`)
)

// launchTable is the single place where provider launch links are defined.
// Verification state (2026-10-07):
//   - RustDesk: verified in the client source. flutter/lib/common.dart handleUriLink accepts
//     rustdesk://connection/new/<id> as a legacy "for compatibility" form (current forms: rustdesk://<id>,
//     rustdesk://connect/<id>); query parameters such as password exist and are never used here.
//     Source: https://github.com/rustdesk/rustdesk/blob/master/flutter/lib/common.dart
//   - AnyDesk: NOT verified. The official CLI documentation only covers "anydesk.exe <ID/Alias>"
//     (https://support.anydesk.com/docs/command-line-interface-for-windows.md); the anydesk:<id> URL handler is not
//     documented there. Check against a real client before enabling.
//   - HopToDesk: NOT verified. No public documentation or reachable source for hoptodesk://<id> was found.
//
// No link ever contains a password or token (docs/security/remote-access-threat-models.md).
var launchTable = map[string]launchSpec{
	KeyRustDesk:  {valid: func(s string) bool { return numericID.MatchString(s) || rustdeskCustomID.MatchString(s) }, template: "rustdesk://connection/new/%s"},
	KeyAnyDesk:   {valid: func(s string) bool { return numericID.MatchString(s) || anydeskAliasID.MatchString(s) }, template: "anydesk:%s"},
	KeyHopToDesk: {valid: hoptodeskID.MatchString, template: "hoptodesk://%s"},
}

// ValidPeerID reports whether the id is acceptable for the provider.
func ValidPeerID(provider, id string) bool {
	spec, ok := launchTable[provider]
	return ok && spec.valid(id)
}

// launchOnly is a connector that implements launching only (observations unsupported).
type launchOnly struct{ key string }

func (l launchOnly) Key() string                { return l.key }
func (l launchOnly) Capabilities() Capabilities { return Capabilities{Attended: true} }

func (l launchOnly) BuildLaunch(peer PeerRef, mode Mode) (LaunchURI, error) {
	if mode != ModeAttended {
		return LaunchURI{}, ErrUnsupportedMode
	}
	spec, ok := launchTable[l.key]
	if !ok || peer.Provider != l.key {
		return LaunchURI{}, ErrUnknownProvider
	}
	if !spec.valid(peer.ID) {
		return LaunchURI{}, ErrInvalidPeerID
	}
	return LaunchURI{value: fmt.Sprintf(spec.template, peer.ID)}, nil
}

// NewRustDesk, NewAnyDesk and NewHopToDesk are the launch-only connectors.
func NewRustDesk() Provider  { return launchOnly{key: KeyRustDesk} }
func NewAnyDesk() Provider   { return launchOnly{key: KeyAnyDesk} }
func NewHopToDesk() Provider { return launchOnly{key: KeyHopToDesk} }

// NewConnector returns the connector for a provider key.
func NewConnector(key string) (Provider, error) {
	switch key {
	case KeyRustDesk:
		return NewRustDesk(), nil
	case KeyAnyDesk:
		return NewAnyDesk(), nil
	case KeyHopToDesk:
		return NewHopToDesk(), nil
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, key)
}
