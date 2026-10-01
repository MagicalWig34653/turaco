package authentication

import (
	"context"
	"strings"
	"testing"
)

const goodPassword = "correct horse battery staple"

func TestHashPasswordRoundTrip(t *testing.T) {
	ctx := context.Background()
	h, err := HashPassword(ctx, goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("hash = %q", h)
	}
	parts := strings.Split(h, "$")
	if len(parts) != 6 || strings.ContainsAny(parts[4]+parts[5], "=") {
		t.Fatalf("unexpected PHC encoding %q", h)
	}
	if strings.Contains(h, goodPassword) {
		t.Fatal("hash contains the password")
	}
	ok, err := VerifyPasswordHash(ctx, h, goodPassword)
	if err != nil || !ok {
		t.Fatalf("verify = %v, %v", ok, err)
	}
	for _, wrong := range []string{"", goodPassword + "x", strings.ToUpper(goodPassword), strings.Repeat("a", MaxPasswordLength+1)} {
		ok, err := VerifyPasswordHash(ctx, h, wrong)
		if err != nil || ok {
			t.Fatalf("verify(%.20q) = %v, %v", wrong, ok, err)
		}
	}
}

func TestHashPasswordUsesFreshSalt(t *testing.T) {
	a, _ := HashPassword(context.Background(), goodPassword)
	b, _ := HashPassword(context.Background(), goodPassword)
	if a == b {
		t.Fatal("identical hashes: salt not random")
	}
}

func TestHashPasswordPolicy(t *testing.T) {
	ctx := context.Background()
	if _, err := HashPassword(ctx, "short"); err != ErrPasswordTooShort {
		t.Fatalf("err = %v", err)
	}
	if _, err := HashPassword(ctx, strings.Repeat("é", MinPasswordLength-1)); err != ErrPasswordTooShort {
		t.Fatalf("err = %v (length counts characters)", err)
	}
	if _, err := HashPassword(ctx, strings.Repeat("é", MinPasswordLength)); err != nil {
		t.Fatalf("16 characters must pass: %v", err)
	}
	if _, err := HashPassword(ctx, strings.Repeat("a", MaxPasswordLength+1)); err != ErrPasswordTooLong {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifyPasswordHashMalformed(t *testing.T) {
	good, _ := HashPassword(context.Background(), goodPassword)
	parts := strings.Split(good, "$")
	bad := []string{
		"", "plain", "$argon2i$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$" + parts[5],
		"$argon2id$v=18$m=65536,t=3,p=2$" + parts[4] + "$" + parts[5],
		"$argon2id$v=19$m=65536,t=3$" + parts[4] + "$" + parts[5],
		"$argon2id$v=19$m=0,t=3,p=2$" + parts[4] + "$" + parts[5],
		"$argon2id$v=19$m=999999999,t=3,p=2$" + parts[4] + "$" + parts[5],
		"$argon2id$v=19$m=65536,t=99,p=2$" + parts[4] + "$" + parts[5],
		"$argon2id$v=19$m=65536,t=3,p=200$" + parts[4] + "$" + parts[5],
		"$argon2id$v=19$m=65536,t=3,p=2$!!$" + parts[5],
		"$argon2id$v=19$m=65536,t=3,p=2$" + parts[4] + "$AA",
		good + "$extra",
	}
	for _, h := range bad {
		if ok, err := VerifyPasswordHash(context.Background(), h, goodPassword); ok || err == nil {
			t.Errorf("hash %.40q accepted or no error (ok=%v err=%v)", h, ok, err)
		}
	}
}

func TestDummyHashIsWellFormed(t *testing.T) {
	if _, err := parseArgonHash(dummyHash()); err != nil {
		t.Fatal(err)
	}
}
