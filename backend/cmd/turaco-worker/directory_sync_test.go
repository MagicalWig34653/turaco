package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

type fakeSource struct{}

func (fakeSource) ProviderKey() string { return "ad" }
func (fakeSource) Fetch(context.Context) (orgpublic.DirectorySnapshot, error) {
	return orgpublic.DirectorySnapshot{}, nil
}

type fakeSync struct {
	calls   int
	trigger orgpublic.SyncTrigger
	err     error
}

func (f *fakeSync) Run(_ context.Context, _ orgpublic.DirectorySource, trigger orgpublic.SyncTrigger) (orgpublic.SyncRunResult, error) {
	f.calls++
	f.trigger = trigger
	return orgpublic.SyncRunResult{RunID: "r", Outcome: orgpublic.SyncOutcomeSucceeded}, f.err
}

func job(t *testing.T, payload any) jobs.Job {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return jobs.Job{ID: "j", Type: orgpublic.DirectorySyncJobType, Payload: raw}
}

func TestDirectorySyncHandlerRejectsInvalidJobsPermanently(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := map[string]jobs.Job{
		"bad json":       {Payload: json.RawMessage(`{`)},
		"other provider": job(t, orgpublic.DirectorySyncJobPayload{ProviderKey: "other", Trigger: orgpublic.SyncTriggerManual}),
		"bad trigger":    job(t, orgpublic.DirectorySyncJobPayload{ProviderKey: "ad", Trigger: "cron"}),
	}
	for name, j := range tests {
		t.Run(name, func(t *testing.T) {
			sync := &fakeSync{}
			err := directorySyncHandler(sync, fakeSource{}, logger)(context.Background(), j)
			if !jobs.IsPermanent(err) || sync.calls != 0 {
				t.Fatalf("err = %v (permanent=%v), calls = %d", err, jobs.IsPermanent(err), sync.calls)
			}
		})
	}
}

func TestDirectorySyncHandlerRunsAndPropagatesErrors(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sync := &fakeSync{}
	h := directorySyncHandler(sync, fakeSource{}, logger)
	if err := h(context.Background(), job(t, orgpublic.DirectorySyncJobPayload{ProviderKey: "ad", Trigger: orgpublic.SyncTriggerManual})); err != nil {
		t.Fatal(err)
	}
	if sync.calls != 1 || sync.trigger != orgpublic.SyncTriggerManual {
		t.Fatalf("calls = %d, trigger = %q", sync.calls, sync.trigger)
	}
	sync.err = orgpublic.ErrSyncAlreadyRunning
	err := h(context.Background(), job(t, orgpublic.DirectorySyncJobPayload{ProviderKey: "ad", Trigger: orgpublic.SyncTriggerScheduled}))
	if !errors.Is(err, orgpublic.ErrSyncAlreadyRunning) || jobs.IsPermanent(err) {
		t.Fatalf("err = %v; want retryable ErrSyncAlreadyRunning", err)
	}
}
