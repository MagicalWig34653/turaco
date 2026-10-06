package config

import (
	"reflect"
	"testing"
	"time"
)

func TestLoadAdvisoryFeedsDefaults(t *testing.T) {
	for _, k := range []string{"ADVISORY_SYNC", "ADVISORY_SOURCES", "NVD_API_KEY_FILE", "ADVISORY_SYNC_INTERVAL"} {
		t.Setenv(k, "")
	}
	c, err := LoadAdvisoryFeeds()
	if err != nil {
		t.Fatal(err)
	}
	if c.Enabled || !reflect.DeepEqual(c.Sources, []string{"nvd", "cisa_kev"}) || c.Interval != 6*time.Hour || c.NVDAPIKeyFile != "" {
		t.Fatalf("%+v", c)
	}
}

func TestLoadAdvisoryFeedsValues(t *testing.T) {
	t.Setenv("ADVISORY_SYNC", "true")
	t.Setenv("ADVISORY_SOURCES", " CISA_KEV , nvd, nvd ")
	t.Setenv("NVD_API_KEY_FILE", "/run/secrets/nvd")
	t.Setenv("ADVISORY_SYNC_INTERVAL", "90m")
	c, err := LoadAdvisoryFeeds()
	if err != nil {
		t.Fatal(err)
	}
	if !c.Enabled || !reflect.DeepEqual(c.Sources, []string{"nvd", "cisa_kev"}) || c.Interval != 90*time.Minute || c.NVDAPIKeyFile != "/run/secrets/nvd" {
		t.Fatalf("%+v", c)
	}
}

func TestLoadAdvisoryFeedsRejectsInvalid(t *testing.T) {
	cases := map[string]map[string]string{
		"unknown source":    {"ADVISORY_SOURCES": "nvd,osv"},
		"only separators":   {"ADVISORY_SOURCES": " , "},
		"interval too low":  {"ADVISORY_SYNC_INTERVAL": "30m"},
		"interval invalid":  {"ADVISORY_SYNC_INTERVAL": "often"},
		"interval negative": {"ADVISORY_SYNC_INTERVAL": "-1h"},
		"bad switch":        {"ADVISORY_SYNC": "maybe"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			for _, k := range []string{"ADVISORY_SYNC", "ADVISORY_SOURCES", "ADVISORY_SYNC_INTERVAL"} {
				t.Setenv(k, "")
			}
			for k, v := range env {
				t.Setenv(k, v)
			}
			if _, err := LoadAdvisoryFeeds(); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
