package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testKey(b byte) []byte { return bytes.Repeat([]byte{b}, MasterKeyLen) }

func newVault(t *testing.T) (*Vault, *memBackend) {
	t.Helper()
	mem := newMem()
	v, err := NewVault(mem, testKey(1))
	if err != nil {
		t.Fatal(err)
	}
	return v, mem
}

func roundTrip(t *testing.T, v *Vault, plain []byte) string {
	t.Helper()
	id, _ := NewID()
	n, err := v.Put(context.Background(), id, bytes.NewReader(plain), 0)
	if err != nil || n != int64(len(plain)) {
		t.Fatalf("put %d bytes: n=%d err=%v", len(plain), n, err)
	}
	rc, err := v.Open(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %d bytes: %v", len(plain), err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round trip of %d bytes differs", len(plain))
	}
	return id
}

func TestRoundTripAroundChunkBoundaries(t *testing.T) {
	v, mem := newVault(t)
	for _, n := range []int{0, 1, 100, chunkSize - 1, chunkSize, chunkSize + 1, 2 * chunkSize, 2*chunkSize + 7, 5*chunkSize + 3} {
		p := make([]byte, n)
		_, _ = rand.Read(p)
		id := roundTrip(t, v, p)
		if n >= 16 && bytes.Contains(mem.m[id], p[:min(n, 32)]) {
			t.Fatalf("plaintext found in stored object of %d bytes", n)
		}
	}
}

func TestPlaintextNeverStoredAndKeysDiffer(t *testing.T) {
	v, mem := newVault(t)
	p := []byte(strings.Repeat("patient record ", 100))
	a, b := roundTrip(t, v, p), roundTrip(t, v, p)
	if bytes.Equal(mem.m[a], mem.m[b]) {
		t.Fatal("identical plaintext produced identical objects: data keys or nonces are reused")
	}
	if bytes.Contains(mem.m[a], []byte("patient record")) {
		t.Fatal("plaintext in object")
	}
}

func TestTamperDetection(t *testing.T) {
	p := make([]byte, 3*chunkSize+10)
	_, _ = rand.Read(p)
	cases := map[string]func(o []byte) []byte{
		"flip header key id":   func(o []byte) []byte { o[6] ^= 1; return o },
		"flip wrapped key":     func(o []byte) []byte { o[30] ^= 1; return o },
		"flip first chunk":     func(o []byte) []byte { o[headerSize+5] ^= 1; return o },
		"flip middle chunk":    func(o []byte) []byte { o[headerSize+chunkSize+gcmOverhead+9] ^= 1; return o },
		"flip last byte":       func(o []byte) []byte { o[len(o)-1] ^= 1; return o },
		"truncate at boundary": func(o []byte) []byte { return o[:headerSize+2*(chunkSize+gcmOverhead)] },
		"truncate mid chunk":   func(o []byte) []byte { return o[:len(o)-5] },
		"append garbage":       func(o []byte) []byte { return append(o, 1, 2, 3) },
		"drop first chunk": func(o []byte) []byte {
			return append(append([]byte(nil), o[:headerSize]...), o[headerSize+chunkSize+gcmOverhead:]...)
		},
		"swap two chunks": func(o []byte) []byte { return swapChunks(o) },
		"empty object":    func(o []byte) []byte { return nil },
		"header only":     func(o []byte) []byte { return o[:headerSize] },
		"bad magic":       func(o []byte) []byte { o[0] = 'X'; return o },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			v, mem := newVault(t)
			id := roundTrip(t, v, p)
			mem.m[id] = mut(append([]byte(nil), mem.m[id]...))
			rc, err := v.Open(context.Background(), id)
			if err == nil {
				_, err = io.Copy(io.Discard, rc)
				rc.Close()
			}
			if !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrWrongKey) {
				t.Fatalf("tampering not detected: %v", err)
			}
		})
	}
}

func swapChunks(o []byte) []byte {
	out := append([]byte(nil), o...)
	sz := chunkSize + gcmOverhead
	a, b := out[headerSize:headerSize+sz], out[headerSize+sz:headerSize+2*sz]
	tmp := append([]byte(nil), a...)
	copy(a, b)
	copy(b, tmp)
	return out
}

func TestObjectCannotBeMovedToAnotherID(t *testing.T) {
	v, mem := newVault(t)
	a := roundTrip(t, v, []byte("secret"))
	b, _ := NewID()
	mem.m[b] = mem.m[a]
	rc, err := v.Open(context.Background(), b)
	if err == nil {
		_, err = io.Copy(io.Discard, rc)
		rc.Close()
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("copied object opened under a different id: %v", err)
	}
}

func TestWrongMasterKey(t *testing.T) {
	v, mem := newVault(t)
	id := roundTrip(t, v, []byte("secret"))
	other, _ := NewVault(mem, testKey(2))
	if _, err := other.Open(context.Background(), id); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("want ErrWrongKey, got %v", err)
	}
}

func TestSizeLimit(t *testing.T) {
	v, mem := newVault(t)
	id, _ := NewID()
	_, err := v.Put(context.Background(), id, bytes.NewReader(make([]byte, 3*chunkSize)), chunkSize+5)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
	if _, ok := mem.m[id]; ok {
		t.Fatal("object stored despite the limit")
	}
	if _, err := v.Put(context.Background(), id, bytes.NewReader(make([]byte, chunkSize)), chunkSize); err != nil {
		t.Fatalf("exactly at the limit: %v", err)
	}
}

func TestInvalidIDs(t *testing.T) {
	v, _ := newVault(t)
	for _, id := range []string{"", "../etc/passwd", strings.Repeat("A", IDLength), strings.Repeat("a", IDLength-1), strings.Repeat("a", IDLength+1), "../" + strings.Repeat("a", IDLength-3), strings.Repeat("g", IDLength)} {
		if _, err := v.Put(context.Background(), id, strings.NewReader("x"), 0); !errors.Is(err, ErrInvalidID) {
			t.Errorf("put %q: %v", id, err)
		}
		if _, err := v.Open(context.Background(), id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("open %q: %v", id, err)
		}
		if err := v.Delete(context.Background(), id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("delete %q: %v", id, err)
		}
	}
}

func TestLoadMasterKey(t *testing.T) {
	dir := t.TempDir()
	write := func(content string) string {
		p := filepath.Join(dir, "k")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	hex64 := strings.Repeat("ab", 32)
	if k, err := LoadMasterKey(write(hex64 + "\n")); err != nil || len(k) != MasterKeyLen {
		t.Fatalf("hex: %v", err)
	}
	for _, bad := range []string{"", "short", hex64[:62], hex64 + "00", strings.Repeat("zz", 32)} {
		if _, err := LoadMasterKey(write(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := LoadMasterKey(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing file accepted")
	}
	if _, err := NewVault(newMem(), []byte("short")); err == nil {
		t.Error("short key accepted")
	}
}
