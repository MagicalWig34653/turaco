package kerberos

import (
	"context"
	"crypto/rand"
	"log/slog"
	"testing"
	"time"

	"github.com/jcmturner/gokrb5/v8/client"
	"github.com/jcmturner/gokrb5/v8/credentials"
	"github.com/jcmturner/gokrb5/v8/iana/etypeID"
	"github.com/jcmturner/gokrb5/v8/keytab"
	"github.com/jcmturner/gokrb5/v8/messages"
	"github.com/jcmturner/gokrb5/v8/spnego"
	"github.com/jcmturner/gokrb5/v8/types"
)

// Security regression: a cross-realm ticket for alice@OTHER.TEST whose client
// writes EXAMPLE.TEST into its own (client-encrypted) authenticator must not be
// accepted as alice@EXAMPLE.TEST. Identity comes from the ticket.
func TestForgedAuthenticatorRealmIsRejected(t *testing.T) {
	e := newEnv(t)
	now := time.Now().UTC()
	cname := types.PrincipalName{NameType: 1, NameString: []string{"alice"}}
	sname := types.NewPrincipalName(2, testService)
	tkt, sk, err := messages.NewTicket(cname, "OTHER.TEST", sname, testRealm, types.NewKrbFlags(),
		e.kt, etypeID.AES256_CTS_HMAC_SHA1_96, testKVNO, now.Add(-time.Minute), now.Add(-time.Minute), now.Add(time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	auth, _ := types.NewAuthenticator(testRealm, cname) // forged realm
	var n [3]byte
	_, _ = rand.Read(n[:])
	auth.Cusec = int(n[0])<<16 | int(n[1])<<8 | int(n[2])
	apReq, err := messages.NewAPReq(tkt, sk, auth)
	if err != nil {
		t.Fatal(err)
	}
	cl := &client.Client{Credentials: credentials.NewFromPrincipalName(cname, testRealm)}
	mech, err := spnego.NewKRB5TokenAPREQ(cl, tkt, sk, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mech.APReq = apReq
	raw, err := mech.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.validate(raw)
	if err == nil {
		t.Fatalf("cross-realm ticket of alice@OTHER.TEST accepted as %+v", p)
	}
}

// Security regression: one token presented concurrently is accepted at most
// once (gokrb5's replay cache is not atomic; verification is serialized).
func TestConcurrentReplayAcceptedAtMostOnce(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 50; i++ {
		tok := e.token(ticketSpec{client: []string{"bob"}})
		res := make(chan error, 8)
		start := make(chan struct{})
		for j := 0; j < 8; j++ {
			go func() { <-start; _, err := e.validate(tok); res <- err }()
		}
		close(start)
		succ := 0
		for j := 0; j < 8; j++ {
			if <-res == nil {
				succ++
			}
		}
		if succ > 1 {
			t.Fatalf("round %d: the same token was accepted %d times", i, succ)
		}
	}
}

// Security regression: with a key of another realm in the keytab, a ticket
// minted by that realm's KDC claiming our realm must be rejected.
func TestKeytabKeyOfOtherRealmIsNotUsed(t *testing.T) {
	kt := keytab.New()
	addKey(t, kt, testService, testRealm, "service-key-password")
	otherKT := keytab.New()
	addKey(t, otherKT, testService, "OTHER.TEST", "other-realm-password")
	addKey(t, kt, testService, "OTHER.TEST", "other-realm-password")
	v, err := NewValidator(baseConfig(writeKeytab(t, kt)), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	cname := types.PrincipalName{NameType: 1, NameString: []string{"carol"}}
	sname := types.NewPrincipalName(2, testService)
	tkt, sk, err := messages.NewTicket(cname, testRealm, sname, "OTHER.TEST", types.NewKrbFlags(),
		otherKT, etypeID.AES256_CTS_HMAC_SHA1_96, testKVNO, now.Add(-time.Minute), now.Add(-time.Minute), now.Add(time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	auth, _ := types.NewAuthenticator(testRealm, cname)
	apReq, _ := messages.NewAPReq(tkt, sk, auth)
	cl := &client.Client{Credentials: credentials.NewFromPrincipalName(cname, testRealm)}
	mech, _ := spnego.NewKRB5TokenAPREQ(cl, tkt, sk, nil, nil)
	mech.APReq = apReq
	raw, _ := mech.Marshal()
	p, err := v.Validate(context.Background(), raw)
	if err == nil {
		t.Fatalf("ticket minted by the OTHER.TEST KDC accepted as %+v", p)
	}
}
