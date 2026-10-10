package storage

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Object format (all integers big endian):
//
//	magic "TRCO" | version 1 | key id (8) | wrapped-key nonce (12) | wrapped data key (32+16) | nonce prefix (8) |
//	chunks: each plaintext chunk of up to chunkSize bytes sealed with AES-256-GCM; the nonce is the prefix plus the
//	        4-byte chunk counter, the additional data is the object id, the counter and a final flag.
//
// The last chunk has the final flag set (it may be empty, so an empty object is one empty final chunk).
const (
	chunkSize   = 64 << 10
	headerSize  = 4 + 1 + 8 + 12 + 32 + 16 + 8
	magic       = "TRCO"
	formatV1    = 1
	gcmOverhead = 16
)

// MasterKeyLen is the length of the master key in bytes.
const MasterKeyLen = 32

// Vault encrypts objects before they reach a Backend and decrypts them on the way out.
type Vault struct {
	backend Backend
	master  cipher.AEAD
	keyID   [8]byte
}

// NewVault builds a Vault over a backend with a 32-byte master key.
func NewVault(b Backend, masterKey []byte) (*Vault, error) {
	if len(masterKey) != MasterKeyLen {
		return nil, fmt.Errorf("storage: master key must be %d bytes", MasterKeyLen)
	}
	blk, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	v := &Vault{backend: b, master: g}
	sum := sha256.Sum256(append([]byte("turaco-storage-key-id\x00"), masterKey...))
	copy(v.keyID[:], sum[:8])
	return v, nil
}

// LoadMasterKey reads a master key file (refused on unix when group or others can access it, so mode 0600 or 0400): 64 hexadecimal characters (surrounding whitespace is ignored). Raw 32
// byte files are accepted too.
func LoadMasterKey(path string) ([]byte, error) {
	if err := checkKeyFileMode(path); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read master key file: %w", err)
	}
	if len(raw) == MasterKeyLen {
		return raw, nil
	}
	k, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(k) != MasterKeyLen {
		return nil, errors.New("master key file must contain 64 hexadecimal characters (32 random bytes)")
	}
	return k, nil
}

// Backend returns the underlying blob port (health checks and deletes).
func (v *Vault) Backend() Backend { return v.backend }

// Put encrypts r and stores it under id. maxBytes bounds the plaintext: more data fails with ErrTooLarge and
// leaves no object. It returns the plaintext size.
func (v *Vault) Put(ctx context.Context, id string, r io.Reader, maxBytes int64) (int64, error) {
	if !ValidID(id) {
		return 0, ErrInvalidID
	}
	dk := make([]byte, 32)
	if _, err := rand.Read(dk); err != nil {
		return 0, err
	}
	hdr := make([]byte, 0, headerSize)
	hdr = append(hdr, magic...)
	hdr = append(hdr, formatV1)
	hdr = append(hdr, v.keyID[:]...)
	wnonce := make([]byte, 12)
	if _, err := rand.Read(wnonce); err != nil {
		return 0, err
	}
	hdr = append(hdr, wnonce...)
	hdr = v.master.Seal(hdr, wnonce, dk, wrapAAD(id))
	prefix := make([]byte, 8)
	if _, err := rand.Read(prefix); err != nil {
		return 0, err
	}
	hdr = append(hdr, prefix...)

	blk, err := aes.NewCipher(dk)
	if err != nil {
		return 0, err
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		return 0, err
	}
	enc := &encryptReader{src: r, aead: aead, id: id, prefix: prefix, max: maxBytes, buf: bytes.NewBuffer(hdr)}
	if err := v.backend.Put(ctx, id, enc); err != nil {
		// A size or read failure surfaces through the backend as a wrapped read error.
		if enc.err != nil {
			return 0, enc.err
		}
		return 0, err
	}
	return enc.n, nil
}

// Open decrypts the object as a stream. Authentication failures surface as ErrCorrupt from Read, after the
// bytes of the preceding intact chunks; callers must treat any read error as a failed download.
func (v *Vault) Open(ctx context.Context, id string) (io.ReadCloser, error) {
	if !ValidID(id) {
		return nil, ErrInvalidID
	}
	rc, err := v.backend.Open(ctx, id)
	if err != nil {
		return nil, err
	}
	hdr := make([]byte, headerSize)
	if _, err := io.ReadFull(rc, hdr); err != nil {
		_ = rc.Close()
		return nil, ErrCorrupt
	}
	if string(hdr[:4]) != magic || hdr[4] != formatV1 {
		_ = rc.Close()
		return nil, ErrCorrupt
	}
	if !bytes.Equal(hdr[5:13], v.keyID[:]) {
		_ = rc.Close()
		return nil, ErrWrongKey
	}
	dk, err := v.master.Open(nil, hdr[13:25], hdr[25:25+32+gcmOverhead], wrapAAD(id))
	if err != nil {
		_ = rc.Close()
		return nil, ErrCorrupt
	}
	blk, err := aes.NewCipher(dk)
	if err != nil {
		_ = rc.Close()
		return nil, err
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		_ = rc.Close()
		return nil, err
	}
	return &decryptReader{rc: rc, aead: aead, id: id, prefix: hdr[headerSize-8:], in: make([]byte, chunkSize+gcmOverhead)}, nil
}

// Check proves the backend is reachable and writable.
func (v *Vault) Check(ctx context.Context) error { return v.backend.Check(ctx) }

// Driver names the backend adapter.
func (v *Vault) Driver() string { return v.backend.Driver() }

// Delete removes the object.
func (v *Vault) Delete(ctx context.Context, id string) error {
	if !ValidID(id) {
		return ErrInvalidID
	}
	return v.backend.Delete(ctx, id)
}

func wrapAAD(id string) []byte { return []byte("turaco-wrap\x00" + id) }

func chunkAAD(id string, counter uint32, final bool) []byte {
	a := make([]byte, 0, len(id)+6)
	a = append(a, id...)
	a = binary.BigEndian.AppendUint32(a, counter)
	if final {
		return append(a, 1)
	}
	return append(a, 0)
}

func chunkNonce(prefix []byte, counter uint32) []byte {
	n := make([]byte, 12)
	copy(n, prefix)
	binary.BigEndian.PutUint32(n[8:], counter)
	return n
}

type encryptReader struct {
	src     io.Reader
	aead    cipher.AEAD
	id      string
	prefix  []byte
	max     int64
	n       int64
	counter uint32
	buf     *bytes.Buffer
	done    bool
	err     error
	plain   []byte
	carry   []byte
}

func (e *encryptReader) Read(p []byte) (int, error) {
	for e.buf.Len() == 0 {
		if e.err != nil {
			return 0, e.err
		}
		if e.done {
			return 0, io.EOF
		}
		if err := e.fill(); err != nil {
			e.err = err
			return 0, err
		}
	}
	return e.buf.Read(p)
}

// fill seals the next chunk. One byte of look-ahead decides whether the chunk is the final one.
func (e *encryptReader) fill() error {
	if e.plain == nil {
		e.plain = make([]byte, chunkSize+1)
	}
	n := copy(e.plain, e.carry)
	e.carry = e.carry[:0]
	m, err := io.ReadFull(e.src, e.plain[n:])
	n += m
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	final := n <= chunkSize
	body := e.plain[:min(n, chunkSize)]
	if !final {
		e.carry = append(e.carry, e.plain[chunkSize])
	}
	e.n += int64(len(body))
	if e.max > 0 && e.n > e.max {
		return ErrTooLarge
	}
	e.buf.Reset()
	e.buf.Write(e.aead.Seal(nil, chunkNonce(e.prefix, e.counter), body, chunkAAD(e.id, e.counter, final)))
	e.counter++
	e.done = final
	return nil
}

type decryptReader struct {
	rc      io.ReadCloser
	aead    cipher.AEAD
	id      string
	prefix  []byte
	in      []byte
	counter uint32
	out     []byte
	pending []byte // one sealed chunk read ahead to know whether the current one is final
	havePen bool
	eof     bool
	done    bool
	err     error
}

func (d *decryptReader) Read(p []byte) (int, error) {
	for len(d.out) == 0 {
		if d.err != nil {
			return 0, d.err
		}
		if d.done {
			return 0, io.EOF
		}
		if err := d.next(); err != nil {
			d.err = err
			return 0, err
		}
	}
	n := copy(p, d.out)
	d.out = d.out[n:]
	return n, nil
}

// readSealed reads one sealed chunk (up to chunkSize+overhead bytes).
func (d *decryptReader) readSealed() ([]byte, error) {
	n, err := io.ReadFull(d.rc, d.in)
	switch {
	case err == nil:
		return append([]byte(nil), d.in[:n]...), nil
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		d.eof = true
		return append([]byte(nil), d.in[:n]...), nil
	}
	return nil, err
}

func (d *decryptReader) next() error {
	if !d.havePen {
		c, err := d.readSealed()
		if err != nil {
			return err
		}
		d.pending, d.havePen = c, true
	}
	cur := d.pending
	final := d.eof // the chunk just read was short or the stream ended with it
	if !d.eof {
		nxt, err := d.readSealed()
		if err != nil {
			return err
		}
		if len(nxt) == 0 {
			final = true // the full-size chunk was the last bytes of the stream
		}
		d.pending, d.havePen = nxt, true
	}
	if len(cur) < gcmOverhead {
		return ErrCorrupt
	}
	plain, err := d.aead.Open(nil, chunkNonce(d.prefix, d.counter), cur, chunkAAD(d.id, d.counter, final))
	if err != nil {
		return ErrCorrupt
	}
	d.counter++
	d.out = plain
	d.done = final
	return nil
}

func (d *decryptReader) Close() error { return d.rc.Close() }
