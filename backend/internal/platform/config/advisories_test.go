package config

import (
	"reflect"
	"testing"
	"time"
)

func TestLoadAdvisoryFeedsDefaults(t *testing.T) {
	for _, k := range []string{"ADVISORY_SYNC", "ADVISORY_SOURCES", "NVD_API_KEY_FILE", "OSV_PACKAGES", "ADVISORY_SYNC_INTERVAL"} {
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
		"unknown source":       {"ADVISORY_SOURCES": "nvd,vendor"},
		"osv without packages": {"ADVISORY_SOURCES": "osv"},
		"osv entry invalid":    {"ADVISORY_SOURCES": "osv", "OSV_PACKAGES": "lodash"},
		"osv empty name":       {"ADVISORY_SOURCES": "osv", "OSV_PACKAGES": "npm="},
		"only separators":      {"ADVISORY_SOURCES": " , "},
		"interval too low":     {"ADVISORY_SYNC_INTERVAL": "30m"},
		"interval invalid":     {"ADVISORY_SYNC_INTERVAL": "often"},
		"interval negative":    {"ADVISORY_SYNC_INTERVAL": "-1h"},
		"bad switch":           {"ADVISORY_SYNC": "maybe"},
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

func TestLoadAdvisoryFeedsOSV(t *testing.T) {
	t.Setenv("ADVISORY_SYNC", "")
	t.Setenv("ADVISORY_SOURCES", "cisa_kev, OSV, nvd")
	t.Setenv("OSV_PACKAGES", " npm=lodash, Maven=org.apache.logging.log4j:log4j-core,Debian:12=openssl, npm=lodash ,")
	t.Setenv("ADVISORY_SYNC_INTERVAL", "")
	c, err := LoadAdvisoryFeeds()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Sources, []string{"nvd", "osv", "cisa_kev"}) {
		t.Fatalf("run order: %v", c.Sources)
	}
	want := []OSVPackage{{"npm", "lodash"}, {"Maven", "org.apache.logging.log4j:log4j-core"}, {"Debian:12", "openssl"}}
	if !reflect.DeepEqual(c.OSVPackages, want) {
		t.Fatalf("packages: %+v", c.OSVPackages)
	}
}

func TestLoadAdvisoryFeedsIgnoresPackagesWithoutOSV(t *testing.T) {
	t.Setenv("ADVISORY_SOURCES", "nvd")
	t.Setenv("OSV_PACKAGES", "")
	if c, err := LoadAdvisoryFeeds(); err != nil || len(c.OSVPackages) != 0 {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestLoadAdvisoryFeedsAcceptsMSRCAndOrdersIt(t *testing.T) {
	t.Setenv("ADVISORY_SOURCES", "cisa_kev, msrc, nvd")
	t.Setenv("OSV_PACKAGES", "")
	c, err := LoadAdvisoryFeeds()
	if err != nil || !reflect.DeepEqual(c.Sources, []string{"nvd", "msrc", "cisa_kev"}) {
		t.Fatalf("%+v %v", c, err)
	}
}
