package microsoft

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
)

func verifyRS256(pub *rsa.PublicKey, signing, sig string) error {
	raw, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(signing))
	return rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], raw)
}
