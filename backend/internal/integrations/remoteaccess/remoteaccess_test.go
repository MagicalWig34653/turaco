package remoteaccess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestBuildLaunchTemplates(t *testing.T) {
	for _, tc := range []struct{ key, id, want string }{
		{KeyRustDesk, "123456789", "rustdesk://connection/new/123456789"},
		{KeyRustDesk, "my-pc_01", "rustdesk://connection/new/my-pc_01"},
		{KeyAnyDesk, "1234567890", "anydesk:1234567890"},
		{KeyAnyDesk, "alias.one@ad", "anydesk:alias.one@ad"},
		{KeyHopToDesk, "abc123XYZ", "hoptodesk://abc123XYZ"},
	} {
		p, err := NewConnector(tc.key)
		if err != nil {
			t.Fatal(err)
		}
		u, err := p.BuildLaunch(PeerRef{Provider: tc.key, ID: tc.id}, ModeAttended)
		if err != nil || u.Reveal() != tc.want {
			t.Fatalf("%s %s: %v %q", tc.key, tc.id, err, u.Reveal())
		}
	}
}

func TestPeerIDValidatorsRefuseInjection(t *testing.T) {
	bad := []string{"", " ", "123456789 ", "123456789\n", "../etc", "a;rm -rf /", "$(id)abcdef", "`id`abcdef",
		"abc def ghi", "a&b=c&d=eeee", "id?password=x", "id#frag12", "id%2Fabc1", "\"quoted12\"", "ünïcode123", "１２３４５６７８９", "abc\x00defgh",
		"rustdesk://x", "a/b/c/d/e/f", "x@y@zzzz", "<script>abc", "id\tvalue1", "abcdefghijklmnopqrstuvwxyz0123456789ABC"}
	for _, key := range []string{KeyRustDesk, KeyAnyDesk, KeyHopToDesk} {
		p, _ := NewConnector(key)
		for _, id := range bad {
			if u, err := p.BuildLaunch(PeerRef{Provider: key, ID: id}, ModeAttended); !errors.Is(err, ErrInvalidPeerID) || u.Reveal() != "" {
				t.Errorf("%s accepted %q (%v)", key, id, err)
			}
		}
	}
	if ValidPeerID("nope", "123456789") || !ValidPeerID(KeyRustDesk, "123456789") {
		t.Fatal("ValidPeerID")
	}
	p := NewAnyDesk()
	for _, id := range []string{"12345678", "12345678901"} {
		if _, err := p.BuildLaunch(PeerRef{Provider: KeyAnyDesk, ID: id}, ModeAttended); !errors.Is(err, ErrInvalidPeerID) {
			t.Errorf("anydesk accepted %q", id)
		}
	}
	if _, err := p.BuildLaunch(PeerRef{Provider: KeyAnyDesk, ID: "123456789"}, Mode("unattended")); !errors.Is(err, ErrUnsupportedMode) {
		t.Fatal("unattended must be refused")
	}
	if _, err := p.BuildLaunch(PeerRef{Provider: KeyRustDesk, ID: "123456789"}, ModeAttended); err == nil {
		t.Fatal("a peer of another provider must be refused")
	}
}

func TestLaunchURIIsRedacted(t *testing.T) {
	u, _ := NewRustDesk().BuildLaunch(PeerRef{Provider: KeyRustDesk, ID: "123456789"}, ModeAttended)
	b, _ := json.Marshal(map[string]any{"u": u})
	for _, s := range []string{fmt.Sprintf("%v %+v %#v %s", u, u, u, u), string(b)} {
		if strings.Contains(s, "123456789") || strings.Contains(s, "rustdesk://") {
			t.Fatalf("launch uri leaked: %s", s)
		}
	}
}

func TestRegistry(t *testing.T) {
	r, err := NewRegistry([]string{"RustDesk", " anydesk ", ""})
	if err != nil || len(r.Keys()) != 2 {
		t.Fatalf("%v %v", err, r.Keys())
	}
	if _, ok := r.Get("hoptodesk"); ok {
		t.Fatal("not enabled")
	}
	if _, err := NewRegistry([]string{"teamviewer"}); !errors.Is(err, ErrUnknownProvider) {
		t.Fatal("unknown key must fail")
	}
	if _, err := NewRegistry([]string{"anydesk", "anydesk"}); err == nil {
		t.Fatal("duplicate key must fail")
	}
	empty, _ := NewRegistry(nil)
	if len(empty.Keys()) != 0 {
		t.Fatal("empty means off")
	}
	if _, err := (NotConfigured{ProviderKey: "x"}).BuildLaunch(PeerRef{}, ModeAttended); !errors.Is(err, ErrNotConfigured) {
		t.Fatal("not configured")
	}
}

func TestFake(t *testing.T) {
	f := NewFake("fake")
	now := time.Now()
	f.Observed = []ObservedSession{{ProviderSessionID: "a", PeerID: "123456789", StartedAt: now}, {ProviderSessionID: "b", StartedAt: now.Add(-48 * time.Hour)}}
	got, _ := f.Sessions(context.Background(), now.Add(-time.Hour))
	if len(got) != 1 || f.SessionCall != 1 {
		t.Fatalf("%v", got)
	}
	f.FailBuild = errors.New("boom")
	if _, err := f.BuildLaunch(PeerRef{ID: "123456789"}, ModeAttended); err == nil || f.BuildCalls != 1 {
		t.Fatal("failure injection")
	}
}
