// Package s3store is the S3 adapter of the storage port (ADR-0037): PUT, GET and DELETE object with AWS
// Signature Version 4 implemented on the standard library, so no SDK dependency is added. Objects are stored under
// a fixed prefix by generated id; the key never contains user input.
package s3store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/storage"
)

// Config holds the S3 settings (the S3_* configuration).
type Config struct {
	// Endpoint is the base URL of the S3-compatible service; empty means AWS (https://s3.<region>.amazonaws.com).
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	PathStyle bool
	// HTTPClient defaults to a client with a 5 minute total timeout.
	HTTPClient *http.Client
}

// Store is the S3 Backend.
type Store struct {
	cfg  Config
	base *url.URL
	hc   *http.Client
	now  func() time.Time
}

// New validates the configuration.
func New(cfg Config) (*Store, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("s3store: S3_BUCKET is required")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if (cfg.AccessKey == "") != (cfg.SecretKey == "") {
		return nil, errors.New("s3store: S3_ACCESS_KEY_ID and S3_SECRET_ACCESS_KEY must be set together")
	}
	ep := cfg.Endpoint
	if ep == "" {
		ep = "https://s3." + cfg.Region + ".amazonaws.com"
		if cfg.Region == "us-east-1" {
			ep = "https://s3.amazonaws.com"
		}
	}
	base, err := url.Parse(ep)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, errors.New("s3store: S3_ENDPOINT must be an http or https URL")
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Minute}
	}
	return &Store{cfg: cfg, base: base, hc: hc, now: time.Now}, nil
}

// Driver implements storage.Backend.
func (s *Store) Driver() string { return "s3" }

func key(id string) string { return "objects/" + id[:2] + "/" + id }

// target returns the request URL for a key: path style (/bucket/key) or virtual host (bucket.host/key).
func (s *Store) target(k string) *url.URL {
	u := *s.base
	if s.cfg.PathStyle {
		u.Path = "/" + s.cfg.Bucket + "/" + k
	} else {
		u.Host = s.cfg.Bucket + "." + u.Host
		u.Path = "/" + k
	}
	u.RawPath = ""
	return &u
}

// Put implements storage.Backend. The stream is spooled to a temporary file first because the request needs its
// length and signed payload hash; the spooled bytes are already encrypted by the Vault.
func (s *Store) Put(ctx context.Context, id string, r io.Reader) error {
	if !storage.ValidID(id) {
		return storage.ErrInvalidID
	}
	f, err := os.CreateTemp("", "turaco-s3-*")
	if err != nil {
		return fmt.Errorf("s3store: spool: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()); _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), r)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.target(key(id)).String(), f)
	if err != nil {
		return err
	}
	req.ContentLength = n
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := s.do(req, hex.EncodeToString(h.Sum(nil)))
	if err != nil {
		return err
	}
	return closeOK(resp, "put")
}

// Open implements storage.Backend.
func (s *Store) Open(ctx context.Context, id string) (io.ReadCloser, error) {
	if !storage.ValidID(id) {
		return nil, storage.ErrInvalidID
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.target(key(id)).String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.do(req, emptySHA256)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, storage.ErrNotFound
	}
	if resp.StatusCode/100 != 2 {
		return nil, statusError(resp, "get")
	}
	return resp.Body, nil
}

// Delete implements storage.Backend.
func (s *Store) Delete(ctx context.Context, id string) error {
	if !storage.ValidID(id) {
		return storage.ErrInvalidID
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.target(key(id)).String(), nil)
	if err != nil {
		return err
	}
	resp, err := s.do(req, emptySHA256)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil
	}
	return closeOK(resp, "delete")
}

// Check writes and deletes a small probe object, which proves reachability, credentials and write permission.
func (s *Store) Check(ctx context.Context) error {
	id, err := storage.NewID()
	if err != nil {
		return err
	}
	k := "probe/" + id
	body := []byte("ok")
	sum := sha256.Sum256(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.target(k).String(), strings.NewReader("ok"))
	if err != nil {
		return err
	}
	resp, err := s.do(req, hex.EncodeToString(sum[:]))
	if err != nil {
		return err
	}
	if err := closeOK(resp, "probe"); err != nil {
		return err
	}
	del, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.target(k).String(), nil)
	if err != nil {
		return err
	}
	resp, err = s.do(del, emptySHA256)
	if err != nil {
		return err
	}
	return closeOK(resp, "probe delete")
}

const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func (s *Store) do(req *http.Request, payloadHash string) (*http.Response, error) {
	if s.cfg.AccessKey != "" {
		s.sign(req, payloadHash, s.now())
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("s3store: %s: %w", req.Method, redact(err))
	}
	return resp, nil
}

// redact drops the *url.Error wrapper: the URL adds nothing, credentials travel in headers only.
func redact(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func closeOK(resp *http.Response, op string) error {
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return statusError(resp, op)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return nil
}

func statusError(resp *http.Response, op string) error {
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	code := ""
	if i := strings.Index(string(b), "<Code>"); i >= 0 {
		if j := strings.Index(string(b[i:]), "</Code>"); j > 0 {
			code = string(b[i+6 : i+j])
		}
	}
	return fmt.Errorf("s3store: %s: status %d %s", op, resp.StatusCode, code)
}

// sign adds AWS Signature Version 4 headers (service s3). Every header present on the request, plus host, is signed.
func (s *Store) sign(req *http.Request, payloadHash string, now time.Time) {
	t := now.UTC()
	amzDate := t.Format("20060102T150405Z")
	date := t.Format("20060102")
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)

	names := []string{"host"}
	for k := range req.Header {
		l := strings.ToLower(k)
		if l != "host" && l != "authorization" {
			names = append(names, l)
		}
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, n := range names {
		v := req.Host
		if n != "host" {
			v = strings.Join(req.Header.Values(n), ",")
		} else if v == "" {
			v = req.URL.Host
		}
		canonHeaders.WriteString(n + ":" + strings.Join(strings.Fields(v), " ") + "\n")
	}
	signed := strings.Join(names, ";")
	canonical := strings.Join([]string{req.Method, canonicalPath(req.URL), req.URL.Query().Encode(), canonHeaders.String(), signed, payloadHash}, "\n")
	scope := date + "/" + s.cfg.Region + "/s3/aws4_request"
	cr := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(cr[:])
	k := hmacSHA256([]byte("AWS4"+s.cfg.SecretKey), date)
	k = hmacSHA256(k, s.cfg.Region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.cfg.AccessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

// canonicalPath percent-encodes each segment once (S3 does not double-encode).
func canonicalPath(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		return "/"
	}
	return p
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}
