package s3store

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/storage"
)

// TestSigV4AWSExample checks the signer against the worked GET Object example of the AWS documentation
// ("Signature Calculations for the Authorization Header").
func TestSigV4AWSExample(t *testing.T) {
	s := &Store{cfg: Config{Region: "us-east-1", AccessKey: "AKIAIOSFODNN7EXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}}
	u, _ := url.Parse("https://examplebucket.s3.amazonaws.com/test.txt")
	req, _ := http.NewRequest(http.MethodGet, u.String(), nil)
	req.Header.Set("Range", "bytes=0-9")
	s.sign(req, emptySHA256, time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("authorization\n got %s\nwant %s", got, want)
	}
}

// fakeS3 is a minimal in-memory S3 that insists on signed requests.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	signed  int
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AK/") {
		http.Error(w, "<Error><Code>AccessDenied</Code></Error>", http.StatusForbidden)
		return
	}
	f.signed++
	switch r.Method {
	case http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		f.objects[r.URL.Path] = b
	case http.MethodGet:
		b, ok := f.objects[r.URL.Path]
		if !ok {
			http.Error(w, "<Error><Code>NoSuchKey</Code></Error>", http.StatusNotFound)
			return
		}
		_, _ = w.Write(b)
	case http.MethodDelete:
		delete(f.objects, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}
}

func TestAgainstFakeS3WithVault(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	s, err := New(Config{Endpoint: srv.URL, Bucket: "b", AccessKey: "AK", SecretKey: "SK", PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	v, _ := storage.NewVault(s, bytes.Repeat([]byte{7}, 32))
	ctx := context.Background()
	id, _ := storage.NewID()
	plain := bytes.Repeat([]byte("abc"), 100000)
	if _, err := v.Put(ctx, id, bytes.NewReader(plain), 0); err != nil {
		t.Fatal(err)
	}
	stored := f.objects["/b/objects/"+id[:2]+"/"+id]
	if len(stored) == 0 || bytes.Contains(stored, []byte("abcabcabc")) {
		t.Fatal("object missing or stored in plaintext")
	}
	rc, err := v.Open(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("round trip: %v", err)
	}
	if err := v.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := v.Delete(ctx, id); err != nil {
		t.Fatalf("idempotent delete: %v", err)
	}
	if _, err := v.Open(ctx, id); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := s.Check(ctx); err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(f.objects) != 0 {
		t.Fatalf("probe or object left: %v", f.objects)
	}
}

func TestErrorsAndConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "<Error><Code>AccessDenied</Code></Error>", http.StatusForbidden)
	}))
	defer srv.Close()
	s, _ := New(Config{Endpoint: srv.URL, Bucket: "b", AccessKey: "AK", SecretKey: "SK", PathStyle: true})
	if err := s.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "AccessDenied") || strings.Contains(err.Error(), "SK") {
		t.Fatalf("check error: %v", err)
	}
	id, _ := storage.NewID()
	if err := s.Put(context.Background(), "../x", strings.NewReader("x")); !errors.Is(err, storage.ErrInvalidID) {
		t.Fatalf("invalid id: %v", err)
	}
	_ = id
	for _, c := range []Config{{Bucket: ""}, {Bucket: "b", AccessKey: "AK"}, {Bucket: "b", Endpoint: "ftp://x"}} {
		if _, err := New(c); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
	vh, _ := New(Config{Endpoint: "https://s3.example.org", Bucket: "bkt", PathStyle: false})
	if got := vh.target("objects/a").String(); got != "https://bkt.s3.example.org/objects/a" {
		t.Fatalf("virtual host target %s", got)
	}
}

// TestS3MockIntegration runs against the Adobe S3Mock dev container when it is reachable
// (S3_ENDPOINT, default http://localhost:9090, bucket turaco-dev).
func TestS3MockIntegration(t *testing.T) {
	endpoint := os.Getenv("S3_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:9090"
	}
	hc := &http.Client{Timeout: 2 * time.Second}
	if resp, err := hc.Get(endpoint + "/turaco-dev"); err != nil {
		t.Skipf("S3Mock not reachable: %v", err)
	} else {
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Skipf("S3Mock bucket turaco-dev unavailable: %d", resp.StatusCode)
		}
	}
	s, err := New(Config{Endpoint: endpoint, Bucket: "turaco-dev", Region: "us-east-1", AccessKey: os.Getenv("S3_ACCESS_KEY_ID"), SecretKey: os.Getenv("S3_SECRET_ACCESS_KEY"), PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Check(ctx); err != nil {
		t.Fatalf("check: %v", err)
	}
	v, _ := storage.NewVault(s, bytes.Repeat([]byte{9}, 32))
	id, _ := storage.NewID()
	plain := bytes.Repeat([]byte("s3mock"), 50000)
	if _, err := v.Put(ctx, id, bytes.NewReader(plain), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Delete(ctx, id) })
	rc, err := v.Open(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, plain) {
		t.Fatal("round trip differs")
	}
	if err := v.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Open(ctx, id); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}
