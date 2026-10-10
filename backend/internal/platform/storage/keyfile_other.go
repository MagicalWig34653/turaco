//go:build !unix

package storage

// checkKeyFileMode is a no-op where unix permission bits do not exist.
func checkKeyFileMode(string) error { return nil }
