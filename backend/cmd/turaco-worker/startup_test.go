package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

// goldenJobTypes is every job type the worker registers with all optional features switched on (SMTP, software
// provider sync, deploy write, Autotask sync, LDAP). A new or removed registration changes this list on purpose:
// update it together with the docs (docs/product/current-status.md) and make sure the new job's timeout and
// schedule are intended.
var goldenJobTypes = []string{
	"ai.retention.purge",
	"ai.sessions.expire",
	"changes.reminders",
	"endpoints.deployment_correlation",
	"endpoints.deployment_tick",
	"endpoints.software_package_sync",
	"notifications.email.send",
	"organization.directory_sync",
	"presence.purge",
	"remoteaccess.expire_sessions",
	"remoteaccess.observe",
	"security.advisory_sync",
	"security.match",
	"security.match_all",
	"security.risk_review_reminders",
	"servicedesk.external.push",
	"services.vm_link_backfill",
	"tasks.recurrence.generate",
}

func newSmokeRunner(t *testing.T, ldap config.LDAPConfig) (*jobs.Runner, *events.Dispatcher, jobDeps, time.Duration) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	categories, err := notifications.NewRegistry(allCategories()...)
	if err != nil {
		t.Fatal(err)
	}
	lock := runnerLockTimeout(ldap.Enabled(), ldap.SyncTimeout)
	return jobs.NewRunner(nil, jobs.RunnerOptions{LockTimeout: lock}, logger),
		events.NewDispatcher(nil, events.DispatcherOptions{}, logger),
		jobDeps{
			LDAP:       ldap,
			SMTP:       config.SMTPConfig{Host: "localhost", Port: 25, Security: "none", From: "turaco@example.test", Timeout: 1, BaseURL: "https://turaco.example.test", DefaultLocale: "en"},
			Categories: categories, Logger: logger,
			SoftwareProviderSync: true, SoftwareDeployWrite: true, AutotaskSync: true,
		}, lock
}

func registeredTypes(m map[string]time.Duration) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestWorkerRegistersEveryJob registers every job of main() (through the variable main calls) with every optional
// feature switched on, and compares the registered types with the golden list. A Register error (for example a timeout
// not shorter than the lock timeout) keeps the worker from starting, so it must fail here.
func TestWorkerRegistersEveryJob(t *testing.T) {
	dir := t.TempDir()
	pw := filepath.Join(dir, "bind-password")
	if err := os.WriteFile(pw, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ldap := config.LDAPConfig{
		ProviderKey: "corp", URL: "ldaps://dir.example.test", BindDN: "cn=turaco,dc=example,dc=test", BindPasswordFile: pw, DirectoryType: config.DirectoryTypeOpenLDAP,
		UserBaseDN: "ou=people,dc=example,dc=test", UserFilter: "(objectClass=person)", GroupBaseDN: "ou=groups,dc=example,dc=test", GroupFilter: "(objectClass=groupOfNames)",
		SyncInterval: time.Hour, SyncTimeout: 90 * time.Minute, MaxMissingPercent: 10,
	}
	for _, tc := range []struct {
		name string
		ldap config.LDAPConfig
		want []string
	}{
		{"without LDAP", config.LDAPConfig{}, slices.DeleteFunc(slices.Clone(goldenJobTypes), func(s string) bool { return s == "organization.directory_sync" })},
		{"with LDAP", ldap, goldenJobTypes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, dispatcher, deps, lock := newSmokeRunner(t, tc.ldap)
			if err := registerJobsFn(runner, dispatcher, nil, deps); err != nil {
				t.Fatalf("registerJobs: %v", err)
			}
			registered := runner.RegisteredJobs()
			if got := registeredTypes(registered); !slices.Equal(got, tc.want) {
				t.Fatalf("registered job types differ from the golden list in startup_test.go (a job was added, removed or renamed: update goldenJobTypes and the documentation deliberately)\n got: %v\nwant: %v", got, tc.want)
			}
			for jobType, timeout := range registered {
				if timeout <= 0 || timeout >= lock {
					t.Errorf("job %s timeout %s must be positive and shorter than the lock timeout %s", jobType, timeout, lock)
				}
			}
			if tc.ldap.Enabled() {
				// The directory sync outlives the default lock: the runner's lock timeout is extended for it.
				if base := runnerLockTimeout(false, 0); lock <= base {
					t.Errorf("lock timeout %s not extended beyond %s for the directory sync", lock, base)
				}
				if registered["organization.directory_sync"] >= lock {
					t.Errorf("directory sync timeout %s must stay below the extended lock %s", registered["organization.directory_sync"], lock)
				}
			}
		})
	}
}
