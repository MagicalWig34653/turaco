package wiring

import (
	"context"
	"sync"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/attachments"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/health"
)

// storageProbeTTL keeps the admin health page from probing the storage on every refresh.
const storageProbeTTL = 30 * time.Second

// storageCheck reports the attachment storage (ADR-0037). Unlike the other integration checks it probes the
// backend (write, read, delete of a probe object, three seconds at most), because reachability and write
// permission are the facts an administrator needs after changing STORAGE_PATH or the bucket.
func storageCheck(svc *attachments.Service) func(context.Context) health.Result {
	var mu sync.Mutex
	var at time.Time
	var cached health.Result
	return func(ctx context.Context) health.Result {
		if svc == nil {
			return notConfigured("STORAGE_DRIVER", "STORAGE_PATH", "STORAGE_MASTER_KEY_FILE")
		}
		mu.Lock()
		defer mu.Unlock()
		if !at.IsZero() && time.Since(at) < storageProbeTTL {
			return cached
		}
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		now := time.Now()
		res := health.Result{Status: health.StatusOK, Mode: health.ModeReal, LastSuccessAt: &now, Detail: map[string]any{"driver": svc.StorageDriver()}}
		if err := svc.CheckStorage(pctx); err != nil {
			res = health.Result{Status: health.StatusFailing, Mode: health.ModeReal, ErrorCode: "storage_unreachable", Detail: map[string]any{"driver": svc.StorageDriver()},
				NextStep: &health.NextStep{Kind: "config", ConfigKeys: []string{"STORAGE_DRIVER", "STORAGE_PATH", "S3_ENDPOINT", "S3_BUCKET"}}}
		}
		at, cached = time.Now(), res
		return res
	}
}

// scannerCheck reports the virus scan gate from stored facts: how many attachments wait for a scan, for how long,
// and whether the worker recorded scanner errors. An unreachable scanner leaves attachments pending, so the
// backlog is what shows it.
func scannerCheck(svc *attachments.Service) func(context.Context) health.Result {
	return func(ctx context.Context) health.Result {
		if svc == nil {
			return notConfigured("CLAMAV_ADDRESS")
		}
		st, err := svc.Stats(ctx)
		if err != nil {
			return health.Result{Status: health.StatusFailing, ErrorCode: "scan_status_unreadable"}
		}
		res := health.Result{Status: health.StatusOK, Mode: health.ModeReal, LastSuccessAt: st.LastScannedAt,
			Counts:   map[string]int{"pending": st.Pending, "failed": st.Failed, "quarantined": st.Infected, "scannerErrors": st.ScannerErrors},
			NextStep: &health.NextStep{Kind: "config", ConfigKeys: []string{"CLAMAV_ADDRESS"}, DocsPath: "docs/operations/installation.md"}}
		if st.OldestPending != nil && time.Since(*st.OldestPending) > attachments.PendingStaleAfter {
			if st.ScannerErrors > 0 {
				res.Status, res.ErrorCode = health.StatusFailing, "scanner_unreachable"
			} else {
				res.Status, res.ErrorCode = health.StatusStale, "scan_backlog"
			}
		}
		return res
	}
}
