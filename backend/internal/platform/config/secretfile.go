package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// maxSecretFileSize bounds how much of a secret file is read; a larger file
// is a misconfiguration.
const maxSecretFileSize = 64 << 10

// ReadSecretFile reads a one-line secret from a deployment secret file (for
// example a Docker secret). One trailing "\n" or "\r\n" is removed; every
// other character belongs to the secret. An empty file is rejected. Errors
// name the operating-system failure, never the content.
func ReadSecretFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read secret file: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSecretFileSize+1))
	if err != nil {
		return "", fmt.Errorf("read secret file: %w", err)
	}
	if len(data) > maxSecretFileSize {
		return "", errors.New("secret file is too large")
	}
	secret := string(data)
	switch {
	case strings.HasSuffix(secret, "\r\n"):
		secret = strings.TrimSuffix(secret, "\r\n")
	case strings.HasSuffix(secret, "\n"):
		secret = strings.TrimSuffix(secret, "\n")
	}
	if secret == "" {
		return "", errors.New("secret file is empty")
	}
	return secret, nil
}
