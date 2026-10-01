package ldap

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// maxPasswordFileSize bounds how much of a secret file is read. Real bind
// passwords are far shorter; a larger file is a misconfiguration.
const maxPasswordFileSize = 64 << 10

// ReadPasswordFile reads the bind password from a deployment secret file (for
// example a Docker secret). One trailing "\n" or "\r\n" is removed because
// secret files commonly end with a newline; every other character, including
// other whitespace, belongs to the password. An empty password is rejected.
//
// Errors name the file path and the operating-system failure but never the
// file content.
func ReadPasswordFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("ldap: read bind password file: %w", err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxPasswordFileSize+1))
	if err != nil {
		return "", fmt.Errorf("ldap: read bind password file: %w", err)
	}
	if len(data) > maxPasswordFileSize {
		return "", errors.New("ldap: bind password file is too large")
	}
	password := string(data)
	switch {
	case strings.HasSuffix(password, "\r\n"):
		password = strings.TrimSuffix(password, "\r\n")
	case strings.HasSuffix(password, "\n"):
		password = strings.TrimSuffix(password, "\n")
	}
	if password == "" {
		return "", errors.New("ldap: bind password file is empty")
	}
	return password, nil
}
