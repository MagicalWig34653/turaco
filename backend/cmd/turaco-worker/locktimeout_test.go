package main

import (
	"testing"
	"time"

	securityapp "github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
)

// The runner refuses to register a job whose timeout is not shorter than its lock timeout. A
// regression here keeps the worker from starting at all, so the lock must cover every job.
func TestRunnerLockTimeoutCoversTheLongestJobs(t *testing.T) {
	if got := runnerLockTimeout(false, 0); got <= securityapp.AdvisorySyncJobTimeout {
		t.Errorf("lock %s does not cover the advisory sync job %s", got, securityapp.AdvisorySyncJobTimeout)
	}
	ldapTimeout := 90 * time.Minute
	if got := runnerLockTimeout(true, ldapTimeout); got <= ldapTimeout+directorySyncMargin {
		t.Errorf("lock %s does not cover the directory sync job", got)
	}
	if got := runnerLockTimeout(true, time.Minute); got <= securityapp.AdvisorySyncJobTimeout {
		t.Errorf("a short LDAP timeout must not lower the lock below the advisory job: %s", got)
	}
}
