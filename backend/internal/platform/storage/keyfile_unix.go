//go:build unix

package storage

import (
	"fmt"
	"os"
)

// checkKeyFileMode refuses a key file that group or others can read, write or execute.
func checkKeyFileMode(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("read master key file: %w", err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("master key file %s has mode %04o: it must not be accessible by group or others (use chmod 600)", path, perm)
	}
	return nil
}
