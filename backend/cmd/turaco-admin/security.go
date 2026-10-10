package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories"
	securityapp "github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

// runSecurity accepts the same bounded JSON envelope as POST /api/v1/security/advisories/import.
// Database access to turaco-admin is an operator capability; the mutation is audited as the CLI.
func runSecurity(ctx context.Context, e env, command string, args []string) error {
	if command == "sync-feeds" {
		return runSyncFeeds(ctx, e, args)
	}
	if command != "import" || len(args) != 0 {
		return errUsage
	}
	const maxBody = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(e.stdin, maxBody+1))
	if err != nil {
		return fmt.Errorf("read advisory import: %w", err)
	}
	if len(raw) > maxBody {
		return fmt.Errorf("advisory import exceeds 1 MB")
	}
	var body struct {
		Records []advisories.AdvisoryRecord `json:"records"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		return fmt.Errorf("decode advisory import: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("advisory import must contain one JSON object")
	}
	if len(body.Records) == 0 || len(body.Records) > securityapp.MaxImportRecords {
		return fmt.Errorf("advisory import must contain 1-%d records", securityapp.MaxImportRecords)
	}
	inputs := make([]securityapp.AdvisoryInput, 0, len(body.Records))
	for _, r := range body.Records {
		inputs = append(inputs, securityapp.FromRecord(r))
	}
	result, err := wiring.Security(e.pool).Import(ctx, securityapp.Caller{Actor: e.auditActor(),
		CorrelationID: fmt.Sprintf("security-import:%d", time.Now().UnixNano())}, securityapp.Principal{Manage: true}, inputs)
	if err != nil {
		return err
	}
	return json.NewEncoder(e.stdout).Encode(result)
}

// syncFeedsTimeout bounds the foreground sync run of the admin command.
const syncFeedsTimeout = 30 * time.Minute

// runSyncFeeds runs the advisory feed synchronization once, in the foreground, with the same code and
// bounds as the worker job: turaco-admin security sync-feeds [--source nvd|osv|msrc|cisa_kev] [--since YYYY-MM-DD].
// Sources come from ADVISORY_SOURCES; NVD_API_KEY_FILE is honored. The result is printed as JSON.
func runSyncFeeds(ctx context.Context, e env, args []string) error {
	fs := newFlagSet("security sync-feeds")
	source := fs.String("source", "", "nvd, osv, msrc or cisa_kev (default: every configured source)")
	since := fs.String("since", "", "read NVD changes since this date (YYYY-MM-DD); never moves the stored cursor backwards")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errUsage
	}
	payload := securityapp.AdvisorySyncPayload{}
	if *source != "" {
		payload.Sources = []string{*source}
	}
	if *since != "" {
		t, err := time.Parse(time.DateOnly, *since)
		if err != nil {
			return fmt.Errorf("--since must be a date like 2026-09-01")
		}
		payload.Since = &t
	}
	cfg, err := config.LoadAdvisoryFeeds()
	if err != nil {
		return err
	}
	service, err := wiring.SecurityWithFeeds(e.pool, cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, syncFeedsTimeout)
	defer cancel()
	results, err := service.SyncFeeds(ctx, securityapp.Caller{Actor: e.auditActor(), CorrelationID: fmt.Sprintf("security-sync:%d", time.Now().UnixNano())}, payload)
	if encErr := json.NewEncoder(e.stdout).Encode(results); encErr != nil && err == nil {
		err = encErr
	}
	return err
}
