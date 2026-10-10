package config

import (
	"strings"
	"testing"
)

func setStorageEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range []string{"STORAGE_DRIVER", "STORAGE_PATH", "STORAGE_MASTER_KEY_FILE", "CLAMAV_ADDRESS", "ATTACHMENT_MAX_BYTES", "ATTACHMENT_ALLOWED_TYPES"} {
		t.Setenv(k, "")
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func TestLoadStorage(t *testing.T) {
	base := map[string]string{"STORAGE_DRIVER": "filesystem", "STORAGE_PATH": "/var/lib/turaco", "STORAGE_MASTER_KEY_FILE": "/run/secrets/k", "CLAMAV_ADDRESS": "clamav:3310"}
	with := func(over map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range over {
			m[k] = v
		}
		return m
	}
	cases := []struct {
		name string
		env  map[string]string
		err  string
	}{
		{"disabled ignores the rest", map[string]string{"STORAGE_PATH": "relative", "ATTACHMENT_MAX_BYTES": "x"}, ""},
		{"filesystem", base, ""},
		{"s3", with(map[string]string{"STORAGE_DRIVER": "s3", "STORAGE_PATH": ""}), ""},
		{"unknown driver", with(map[string]string{"STORAGE_DRIVER": "ftp"}), "STORAGE_DRIVER"},
		{"relative path", with(map[string]string{"STORAGE_PATH": "data"}), "STORAGE_PATH"},
		{"missing path", with(map[string]string{"STORAGE_PATH": ""}), "STORAGE_PATH"},
		{"missing key", with(map[string]string{"STORAGE_MASTER_KEY_FILE": ""}), "STORAGE_MASTER_KEY_FILE"},
		{"missing scanner", with(map[string]string{"CLAMAV_ADDRESS": ""}), "CLAMAV_ADDRESS"},
		{"scanner without port", with(map[string]string{"CLAMAV_ADDRESS": "clamav"}), "CLAMAV_ADDRESS"},
		{"size too small", with(map[string]string{"ATTACHMENT_MAX_BYTES": "10"}), "ATTACHMENT_MAX_BYTES"},
		{"size too large", with(map[string]string{"ATTACHMENT_MAX_BYTES": "999999999999"}), "ATTACHMENT_MAX_BYTES"},
		{"size not a number", with(map[string]string{"ATTACHMENT_MAX_BYTES": "big"}), "ATTACHMENT_MAX_BYTES"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setStorageEnv(t, c.env)
			_, err := LoadStorage()
			if (err == nil) != (c.err == "") || (err != nil && !strings.Contains(err.Error(), c.err)) {
				t.Fatalf("err = %v, want %q", err, c.err)
			}
		})
	}
	setStorageEnv(t, with(map[string]string{"ATTACHMENT_MAX_BYTES": "1048576", "ATTACHMENT_ALLOWED_TYPES": "application/pdf, image/png"}))
	c, err := LoadStorage()
	if err != nil || !c.Enabled() || c.MaxBytes != 1<<20 || len(c.AllowedTypes) != 2 {
		t.Fatalf("%+v %v", c, err)
	}
	setStorageEnv(t, nil)
	if c, _ := LoadStorage(); c.Enabled() || c.MaxBytes != 0 {
		t.Fatalf("disabled config %+v", c)
	}
}
